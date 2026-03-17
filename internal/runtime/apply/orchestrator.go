package apply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"proxyctl/internal/domain"
	"proxyctl/internal/renderer"
	"proxyctl/internal/runtime/layout"
	"proxyctl/internal/storage"
)

const (
	singBoxConfigName = "sing-box.json"
	xrayConfigName    = "xray.json"
)

type ProcessValidator interface {
	Name() string
	Validate(ctx context.Context, artifact ConfigArtifact) error
}

type ServiceManager interface {
	Restart(ctx context.Context, unit string) error
	Reload(ctx context.Context, unit string) error
}

type ServiceAction string

const (
	ServiceActionRestart ServiceAction = "restart"
	ServiceActionReload  ServiceAction = "reload"
)

type ServiceOperation struct {
	Unit   string
	Action ServiceAction
}

type ConfigArtifact struct {
	Name    string
	Path    string
	Content []byte
}

type RuntimeUnitSet struct {
	SingBox string
	Xray    string
}

type Options struct {
	DryRun bool
}

type FileWrite struct {
	Path       string
	BackupPath string
}

type Result struct {
	ArtifactsBuilt []string
	Validated      []string
	Writes         []FileWrite
	ServiceOps     []ServiceOperation
	RolledBack     bool
	DryRun         bool
}

type Orchestrator struct {
	store        storage.Store
	layout       *layout.Manager
	singRenderer renderer.Service
	xrayRenderer renderer.Service
	validators   []ProcessValidator
	services     ServiceManager
	units        RuntimeUnitSet
}

func NewOrchestrator(
	store storage.Store,
	layoutManager *layout.Manager,
	singRenderer renderer.Service,
	xrayRenderer renderer.Service,
	validators []ProcessValidator,
	services ServiceManager,
	units RuntimeUnitSet,
) (*Orchestrator, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if layoutManager == nil {
		return nil, fmt.Errorf("layout manager is required")
	}
	if singRenderer == nil {
		return nil, fmt.Errorf("sing-box renderer is required")
	}
	if xrayRenderer == nil {
		return nil, fmt.Errorf("xray renderer is required")
	}
	if len(validators) == 0 {
		validators = []ProcessValidator{JSONValidator{}}
	}
	if units.SingBox == "" {
		return nil, fmt.Errorf("sing-box unit is required")
	}
	if units.Xray == "" {
		return nil, fmt.Errorf("xray unit is required")
	}

	return &Orchestrator{
		store:        store,
		layout:       layoutManager,
		singRenderer: singRenderer,
		xrayRenderer: xrayRenderer,
		validators:   validators,
		services:     services,
		units:        units,
	}, nil
}

func (o *Orchestrator) Validate(ctx context.Context) (Result, error) {
	return o.execute(ctx, Options{DryRun: true})
}

func (o *Orchestrator) Apply(ctx context.Context, opts Options) (Result, error) {
	return o.execute(ctx, opts)
}

func (o *Orchestrator) execute(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := collectBuildRequest(ctx, o.store)
	if err != nil {
		return Result{}, fmt.Errorf("collect config state: %w", err)
	}

	artifacts, err := o.buildArtifacts(ctx, req)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		DryRun:         opts.DryRun,
		ArtifactsBuilt: artifactNames(artifacts),
		ServiceOps:     o.serviceOps(req),
	}

	if opts.DryRun {
		if err := o.runValidators(ctx, artifacts); err != nil {
			return result, err
		}
		result.Validated = artifactNames(artifacts)
		return result, nil
	}

	// Stage-8 ordering: backup current runtime files before validation hooks.
	rollbackState, writes, err := o.backupCurrent(artifacts)
	if err != nil {
		return result, err
	}
	result.Writes = writes

	if err := o.runValidators(ctx, artifacts); err != nil {
		return result, err
	}
	result.Validated = artifactNames(artifacts)
	if o.services == nil {
		return result, fmt.Errorf("service manager is required for apply")
	}

	if err := o.writeRuntimeFiles(artifacts); err != nil {
		return result, err
	}

	if err := o.runServiceOps(ctx, result.ServiceOps); err != nil {
		result.RolledBack = true
		rollbackErr := o.rollback(ctx, rollbackState, result.ServiceOps)
		if rollbackErr != nil {
			return result, fmt.Errorf("restart services failed: %v; rollback failed: %w", err, rollbackErr)
		}
		return result, fmt.Errorf("restart services failed: %w; runtime files restored from backups", err)
	}

	return result, nil
}

func (o *Orchestrator) buildArtifacts(ctx context.Context, req renderer.BuildRequest) ([]ConfigArtifact, error) {
	singResult, err := o.singRenderer.Render(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("build sing-box runtime config: %w", err)
	}
	xrayResult, err := o.xrayRenderer.Render(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("build xray runtime config: %w", err)
	}

	runtimeDir := o.layout.Directories().RuntimeDir
	return []ConfigArtifact{
		{
			Name:    singBoxConfigName,
			Path:    filepath.Join(runtimeDir, singBoxConfigName),
			Content: selectPreviewContent(singResult),
		},
		{
			Name:    xrayConfigName,
			Path:    filepath.Join(runtimeDir, xrayConfigName),
			Content: selectPreviewContent(xrayResult),
		},
	}, nil
}

type rollbackFileState struct {
	path    string
	existed bool
	content []byte
}

func (o *Orchestrator) backupCurrent(artifacts []ConfigArtifact) ([]rollbackFileState, []FileWrite, error) {
	if err := o.layout.EnsureDirectories(); err != nil {
		return nil, nil, fmt.Errorf("prepare runtime layout: %w", err)
	}

	states := make([]rollbackFileState, 0, len(artifacts))
	writes := make([]FileWrite, 0, len(artifacts))
	for _, artifact := range artifacts {
		state, err := captureRuntimeFile(artifact.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("read existing runtime file %q: %w", artifact.Path, err)
		}
		states = append(states, state)

		backupPath, err := o.layout.BackupPreviousConfig(artifact.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("create backup for %q: %w", artifact.Path, err)
		}
		writes = append(writes, FileWrite{
			Path:       artifact.Path,
			BackupPath: backupPath,
		})
	}
	return states, writes, nil
}

func (o *Orchestrator) runValidators(ctx context.Context, artifacts []ConfigArtifact) error {
	for _, artifact := range artifacts {
		for _, validator := range o.validators {
			if err := validator.Validate(ctx, artifact); err != nil {
				return fmt.Errorf("validate %s with %s: %w", artifact.Name, validator.Name(), err)
			}
		}
	}
	return nil
}

func (o *Orchestrator) writeRuntimeFiles(artifacts []ConfigArtifact) error {
	for _, artifact := range artifacts {
		if err := layout.WriteAtomicFile(artifact.Path, artifact.Content, 0o640); err != nil {
			return fmt.Errorf("write runtime file %q: %w", artifact.Path, err)
		}
	}
	return nil
}

func (o *Orchestrator) serviceOps(req renderer.BuildRequest) []ServiceOperation {
	required := map[domain.Engine]bool{}
	for _, inbound := range req.Inbounds {
		if !inbound.Enabled {
			continue
		}
		required[inbound.Engine] = true
	}

	ops := make([]ServiceOperation, 0, 2)
	if required[domain.EngineSingBox] {
		ops = append(ops, ServiceOperation{Unit: o.units.SingBox, Action: ServiceActionRestart})
	}
	if required[domain.EngineXray] {
		ops = append(ops, ServiceOperation{Unit: o.units.Xray, Action: ServiceActionRestart})
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Unit < ops[j].Unit })
	return ops
}

func (o *Orchestrator) runServiceOps(ctx context.Context, ops []ServiceOperation) error {
	for _, op := range ops {
		switch op.Action {
		case ServiceActionRestart:
			if err := o.services.Restart(ctx, op.Unit); err != nil {
				return fmt.Errorf("restart unit %q: %w", op.Unit, err)
			}
		case ServiceActionReload:
			if err := o.services.Reload(ctx, op.Unit); err != nil {
				return fmt.Errorf("reload unit %q: %w", op.Unit, err)
			}
		default:
			return fmt.Errorf("unsupported service action %q for unit %q", op.Action, op.Unit)
		}
	}
	return nil
}

func (o *Orchestrator) rollback(ctx context.Context, states []rollbackFileState, ops []ServiceOperation) error {
	for _, state := range states {
		if state.existed {
			if err := layout.WriteAtomicFile(state.path, state.content, 0o640); err != nil {
				return fmt.Errorf("restore runtime file %q: %w", state.path, err)
			}
			continue
		}
		if err := os.Remove(state.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove runtime file %q during rollback: %w", state.path, err)
		}
	}

	if len(ops) == 0 {
		return nil
	}
	if o.services == nil {
		return fmt.Errorf("service manager is required for rollback")
	}
	if err := o.runServiceOps(ctx, ops); err != nil {
		return fmt.Errorf("restart services after rollback: %w", err)
	}
	return nil
}

func captureRuntimeFile(path string) (rollbackFileState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rollbackFileState{path: path, existed: false}, nil
		}
		return rollbackFileState{}, err
	}
	return rollbackFileState{path: path, existed: true, content: data}, nil
}

func collectBuildRequest(ctx context.Context, store storage.Store) (renderer.BuildRequest, error) {
	nodes, err := store.Nodes().List(ctx)
	if err != nil {
		return renderer.BuildRequest{}, fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return renderer.BuildRequest{}, fmt.Errorf("no nodes found")
	}

	selectedNode, err := selectPrimaryNode(nodes)
	if err != nil {
		return renderer.BuildRequest{}, err
	}

	inbounds, err := store.Inbounds().List(ctx)
	if err != nil {
		return renderer.BuildRequest{}, fmt.Errorf("list inbounds: %w", err)
	}
	credentials, err := store.Credentials().List(ctx)
	if err != nil {
		return renderer.BuildRequest{}, fmt.Errorf("list credentials: %w", err)
	}

	inboundByID := make(map[string]struct{})
	filteredInbounds := make([]domain.Inbound, 0)
	for _, inbound := range inbounds {
		if !inbound.Enabled || inbound.NodeID != selectedNode.ID {
			continue
		}
		inboundByID[inbound.ID] = struct{}{}
		filteredInbounds = append(filteredInbounds, inbound)
	}
	if len(filteredInbounds) == 0 {
		return renderer.BuildRequest{}, fmt.Errorf("no enabled inbounds found for node %q", selectedNode.ID)
	}
	sort.Slice(filteredInbounds, func(i, j int) bool {
		return filteredInbounds[i].ID < filteredInbounds[j].ID
	})

	filteredCredentials := make([]domain.Credential, 0)
	for _, cred := range credentials {
		if _, ok := inboundByID[cred.InboundID]; ok {
			filteredCredentials = append(filteredCredentials, cred)
		}
	}

	return renderer.BuildRequest{
		Node:        selectedNode,
		Inbounds:    filteredInbounds,
		Credentials: filteredCredentials,
	}, nil
}

func selectPrimaryNode(nodes []domain.Node) (domain.Node, error) {
	enabled := make([]domain.Node, 0, len(nodes))
	enabledPrimary := make([]domain.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Enabled {
			enabled = append(enabled, node)
			if node.Role == domain.NodeRolePrimary {
				enabledPrimary = append(enabledPrimary, node)
			}
		}
	}
	if len(enabled) == 0 {
		return domain.Node{}, fmt.Errorf("no enabled nodes found")
	}

	candidates := enabled
	if len(enabledPrimary) > 0 {
		candidates = enabledPrimary
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt != candidates[j].CreatedAt {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0], nil
}

func selectPreviewContent(result renderer.RenderResult) []byte {
	if len(result.PreviewJSON) > 0 {
		return result.PreviewJSON
	}
	if len(result.Artifacts) > 0 {
		return result.Artifacts[0].Content
	}
	return []byte("{}\n")
}

func artifactNames(artifacts []ConfigArtifact) []string {
	names := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		names = append(names, strings.TrimSpace(artifact.Name))
	}
	sort.Strings(names)
	return names
}
