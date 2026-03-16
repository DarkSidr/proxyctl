package apply

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyctl/internal/domain"
	"proxyctl/internal/renderer"
	"proxyctl/internal/runtime/layout"
	"proxyctl/internal/storage"
)

func TestApplySuccessWritesAndRestarts(t *testing.T) {
	t.Parallel()

	store := fakeStore{
		nodes: []domain.Node{{ID: "node-1", Enabled: true, Role: domain.NodeRolePrimary, Host: "example.com"}},
		inbounds: []domain.Inbound{
			{ID: "in-sb", NodeID: "node-1", Enabled: true, Engine: domain.EngineSingBox},
			{ID: "in-xr", NodeID: "node-1", Enabled: true, Engine: domain.EngineXray},
		},
	}
	root := t.TempDir()
	rtDir := filepath.Join(root, "runtime")
	bkDir := filepath.Join(root, "backups")
	if err := os.MkdirAll(rtDir, 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rtDir, singBoxConfigName), []byte(`{"old":"sing"}`), 0o640); err != nil {
		t.Fatalf("seed sing-box config: %v", err)
	}

	svc := &fakeServiceManager{}
	orch := newTestOrchestrator(t, store, rtDir, bkDir, svc)

	result, err := orch.Apply(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if result.RolledBack {
		t.Fatalf("result.RolledBack = true, want false")
	}
	if len(result.Validated) != 2 {
		t.Fatalf("validated count = %d, want 2", len(result.Validated))
	}
	if len(svc.restarts) != 2 {
		t.Fatalf("restart calls = %d, want 2", len(svc.restarts))
	}

	assertFileContains(t, filepath.Join(rtDir, singBoxConfigName), `"version":"new-sing"`)
	assertFileContains(t, filepath.Join(rtDir, xrayConfigName), `"version":"new-xray"`)
}

func TestApplyRollbackWhenRestartFails(t *testing.T) {
	t.Parallel()

	store := fakeStore{
		nodes: []domain.Node{{ID: "node-1", Enabled: true, Role: domain.NodeRolePrimary, Host: "example.com"}},
		inbounds: []domain.Inbound{
			{ID: "in-sb", NodeID: "node-1", Enabled: true, Engine: domain.EngineSingBox},
		},
	}
	root := t.TempDir()
	rtDir := filepath.Join(root, "runtime")
	bkDir := filepath.Join(root, "backups")
	if err := os.MkdirAll(rtDir, 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	singPath := filepath.Join(rtDir, singBoxConfigName)
	if err := os.WriteFile(singPath, []byte(`{"version":"old-sing"}`), 0o640); err != nil {
		t.Fatalf("seed sing-box config: %v", err)
	}

	svc := &fakeServiceManager{failFirstRestart: true}
	orch := newTestOrchestrator(t, store, rtDir, bkDir, svc)

	result, err := orch.Apply(context.Background(), Options{})
	if err == nil {
		t.Fatalf("Apply() expected error, got nil")
	}
	if !result.RolledBack {
		t.Fatalf("result.RolledBack = false, want true")
	}
	if !strings.Contains(err.Error(), "runtime files restored from backups") {
		t.Fatalf("error = %q, want rollback message", err)
	}
	assertFileContains(t, singPath, `"version":"old-sing"`)
}

func TestValidateDryRunHasNoSideEffects(t *testing.T) {
	t.Parallel()

	store := fakeStore{
		nodes: []domain.Node{{ID: "node-1", Enabled: true, Role: domain.NodeRolePrimary, Host: "example.com"}},
		inbounds: []domain.Inbound{
			{ID: "in-sb", NodeID: "node-1", Enabled: true, Engine: domain.EngineSingBox},
		},
	}
	root := t.TempDir()
	rtDir := filepath.Join(root, "runtime")
	bkDir := filepath.Join(root, "backups")
	svc := &fakeServiceManager{}
	orch := newTestOrchestrator(t, store, rtDir, bkDir, svc)

	result, err := orch.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
	if !result.DryRun {
		t.Fatalf("result.DryRun = false, want true")
	}
	if len(result.Writes) != 0 {
		t.Fatalf("writes count = %d, want 0 in dry-run", len(result.Writes))
	}
	if len(svc.restarts) != 0 {
		t.Fatalf("restart calls = %d, want 0", len(svc.restarts))
	}
	if _, err := os.Stat(rtDir); !os.IsNotExist(err) {
		t.Fatalf("runtime dir should not be created in dry-run")
	}
}

func newTestOrchestrator(t *testing.T, store fakeStore, runtimeDir, backupsDir string, svc *fakeServiceManager) *Orchestrator {
	t.Helper()

	root := filepath.Dir(runtimeDir)
	layoutManager := layout.New(layout.Directories{
		ConfigDir:        filepath.Join(root, "etc"),
		RuntimeDir:       runtimeDir,
		StateDir:         filepath.Join(root, "state"),
		SubscriptionsDir: filepath.Join(root, "subscriptions"),
		BackupsDir:       backupsDir,
	})
	orch, err := NewOrchestrator(
		store,
		layoutManager,
		fakeRenderer{name: singBoxConfigName, payload: []byte(`{"version":"new-sing"}`)},
		fakeRenderer{name: xrayConfigName, payload: []byte(`{"version":"new-xray"}`)},
		[]ProcessValidator{JSONValidator{}},
		svc,
		RuntimeUnitSet{
			SingBox: "proxyctl-sing-box.service",
			Xray:    "proxyctl-xray.service",
		},
	)
	if err != nil {
		t.Fatalf("NewOrchestrator() error: %v", err)
	}
	return orch
}

func assertFileContains(t *testing.T, path, needle string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if !strings.Contains(string(data), needle) {
		t.Fatalf("file %q content=%q, want to contain %q", path, string(data), needle)
	}
}

type fakeStore struct {
	nodes       []domain.Node
	inbounds    []domain.Inbound
	credentials []domain.Credential
}

func (f fakeStore) Init(context.Context) error { return nil }
func (f fakeStore) Close() error               { return nil }
func (f fakeStore) Users() storage.UserRepository {
	return fakeUsersRepo{}
}
func (f fakeStore) Nodes() storage.NodeRepository {
	return fakeNodesRepo{items: f.nodes}
}
func (f fakeStore) Inbounds() storage.InboundRepository {
	return fakeInboundsRepo{items: f.inbounds}
}
func (f fakeStore) Credentials() storage.CredentialRepository {
	return fakeCredentialsRepo{items: f.credentials}
}
func (f fakeStore) Subscriptions() storage.SubscriptionRepository {
	return fakeSubscriptionsRepo{}
}

var _ storage.Store = fakeStore{}

type fakeUsersRepo struct{}

func (fakeUsersRepo) Create(context.Context, domain.User) (domain.User, error) {
	return domain.User{}, nil
}
func (fakeUsersRepo) List(context.Context) ([]domain.User, error) { return nil, nil }
func (fakeUsersRepo) Delete(context.Context, string) (bool, error) {
	return false, nil
}

type fakeNodesRepo struct{ items []domain.Node }

func (fakeNodesRepo) Create(context.Context, domain.Node) (domain.Node, error) {
	return domain.Node{}, nil
}
func (r fakeNodesRepo) List(context.Context) ([]domain.Node, error) {
	return append([]domain.Node(nil), r.items...), nil
}
func (fakeNodesRepo) Update(context.Context, domain.Node) (domain.Node, error) {
	return domain.Node{}, nil
}
func (fakeNodesRepo) Delete(context.Context, string) (bool, error) {
	return false, nil
}

type fakeInboundsRepo struct{ items []domain.Inbound }

func (fakeInboundsRepo) Create(context.Context, domain.Inbound) (domain.Inbound, error) {
	return domain.Inbound{}, nil
}
func (r fakeInboundsRepo) List(context.Context) ([]domain.Inbound, error) {
	return append([]domain.Inbound(nil), r.items...), nil
}
func (fakeInboundsRepo) Delete(context.Context, string) (bool, error) {
	return false, nil
}

type fakeCredentialsRepo struct{ items []domain.Credential }

func (fakeCredentialsRepo) Create(context.Context, domain.Credential) (domain.Credential, error) {
	return domain.Credential{}, nil
}
func (r fakeCredentialsRepo) List(context.Context) ([]domain.Credential, error) {
	return append([]domain.Credential(nil), r.items...), nil
}
func (fakeCredentialsRepo) Update(context.Context, domain.Credential) (domain.Credential, error) {
	return domain.Credential{}, nil
}
func (fakeCredentialsRepo) Delete(context.Context, string) (bool, error) {
	return false, nil
}
func (fakeCredentialsRepo) DeleteByUserID(context.Context, string) (int, error) {
	return 0, nil
}

type fakeSubscriptionsRepo struct{}

func (fakeSubscriptionsRepo) Upsert(context.Context, domain.Subscription) (domain.Subscription, error) {
	return domain.Subscription{}, nil
}
func (fakeSubscriptionsRepo) GetByUserID(context.Context, string) (domain.Subscription, error) {
	return domain.Subscription{}, nil
}
func (fakeSubscriptionsRepo) DeleteByUserID(context.Context, string) (bool, error) {
	return false, nil
}

type fakeRenderer struct {
	name    string
	payload []byte
}

func (r fakeRenderer) Render(context.Context, renderer.BuildRequest) (renderer.RenderResult, error) {
	return renderer.RenderResult{
		Artifacts:   []renderer.Artifact{{Name: r.name, Content: r.payload}},
		PreviewJSON: r.payload,
	}, nil
}

type fakeServiceManager struct {
	restarts         []string
	reloads          []string
	failFirstRestart bool
}

func (m *fakeServiceManager) Restart(_ context.Context, unit string) error {
	m.restarts = append(m.restarts, unit)
	if m.failFirstRestart {
		m.failFirstRestart = false
		return fmt.Errorf("simulated restart failure for %s", unit)
	}
	return nil
}

func (m *fakeServiceManager) Reload(_ context.Context, unit string) error {
	m.reloads = append(m.reloads, unit)
	return nil
}
