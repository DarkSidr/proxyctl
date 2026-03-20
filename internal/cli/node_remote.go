package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
	"proxyctl/internal/renderer"
	"proxyctl/internal/renderer/singbox"
	"proxyctl/internal/renderer/xray"
	"proxyctl/internal/reverseproxy/caddy"
	"proxyctl/internal/storage/sqlite"
)

type nodeSyncResult struct {
	NodeID   string
	Host     string
	Uploaded []string
	Restart  []string
}

func newNodeShowCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <node-id>",
		Short: "Show node details",
		Long:  "Displays detailed information for one node and attached inbounds.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			node, err := findNodeByID(cmd.Context(), store, strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}

			inbounds, err := store.Inbounds().List(cmd.Context())
			if err != nil {
				return fmt.Errorf("list inbounds: %w", err)
			}

			attached := make([]domain.Inbound, 0)
			for _, inbound := range inbounds {
				if inbound.NodeID == node.ID {
					attached = append(attached, inbound)
				}
			}
			sort.Slice(attached, func(i, j int) bool {
				return attached[i].ID < attached[j].ID
			})

			fmt.Fprintf(cmd.OutOrStdout(), "id: %s\n", node.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "name: %s\n", node.Name)
			fmt.Fprintf(cmd.OutOrStdout(), "host: %s\n", node.Host)
			fmt.Fprintf(cmd.OutOrStdout(), "role: %s\n", node.Role)
			fmt.Fprintf(cmd.OutOrStdout(), "enabled: %t\n", node.Enabled)
			fmt.Fprintf(cmd.OutOrStdout(), "created_at: %s\n", node.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
			fmt.Fprintf(cmd.OutOrStdout(), "inbounds: %d\n", len(attached))
			for _, inbound := range attached {
				fmt.Fprintf(
					cmd.OutOrStdout(),
					"  - id=%s type=%s engine=%s transport=%s port=%d enabled=%t\n",
					inbound.ID,
					inbound.Type,
					inbound.Engine,
					inbound.Transport,
					inbound.Port,
					inbound.Enabled,
				)
			}
			return nil
		},
	}
}

func newNodeTestCmd(dbPath *string) *cobra.Command {
	var (
		sshUser       string
		sshPort       int
		sshKeyPath    string
		strictHostKey bool
	)

	cmd := &cobra.Command{
		Use:   "test <node-id>",
		Short: "Test SSH connectivity to a node",
		Long:  "Checks whether panel host can connect to target node over SSH.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sshPort <= 0 || sshPort > 65535 {
				return fmt.Errorf("--ssh-port must be in range 1..65535")
			}

			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			node, err := findNodeByID(cmd.Context(), store, strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}

			host := strings.TrimSpace(node.Host)
			if host == "" {
				return fmt.Errorf("node %q has empty host", node.ID)
			}
			target := fmt.Sprintf("%s@%s", strings.TrimSpace(sshUser), host)

			sshArgs := buildSSHArgs(sshPort, sshKeyPath, strictHostKey)
			sshArgs = append(sshArgs, target, "echo proxyctl-node-test-ok")
			if out, err := runExecCombined(cmd.Context(), "ssh", sshArgs...); err != nil {
				return fmt.Errorf("ssh connectivity check failed for node %q (%s): %w\n%s", node.ID, host, err, strings.TrimSpace(string(out)))
			}

			fmt.Fprintf(cmd.OutOrStdout(), "ssh ok: node=%s host=%s user=%s port=%d\n", node.ID, host, sshUser, sshPort)
			return nil
		},
	}

	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH user")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "Path to private SSH key")
	cmd.Flags().BoolVar(&strictHostKey, "strict-host-key", false, "Use strict SSH host key checking")
	return cmd
}

func newNodeSyncCmd(configPath, dbPath *string) *cobra.Command {
	var (
		nodeIDsCSV    string
		sshUser       string
		sshPort       int
		sshKeyPath    string
		runtimeDir    string
		restart       bool
		strictHostKey bool
		remoteUseSudo bool
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Render and sync runtime configs to remote nodes",
		Long:  "Builds node-specific sing-box/Xray configs on control-plane and pushes them to remote nodes over SSH.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if sshPort <= 0 || sshPort > 65535 {
				return fmt.Errorf("--ssh-port must be in range 1..65535")
			}
			if strings.TrimSpace(sshUser) == "" {
				return fmt.Errorf("--ssh-user is required")
			}
			if strings.TrimSpace(runtimeDir) == "" {
				return fmt.Errorf("--runtime-dir is required")
			}
			if !cmd.Flags().Changed("remote-sudo") {
				remoteUseSudo = sshUser != "root"
			}

			appCfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			store, err := openStoreWithInit(cmd.Context(), resolveDBPath(cmd, appCfg, *dbPath))
			if err != nil {
				return err
			}
			defer store.Close()

			selectedNodeIDs := parseCSV(nodeIDsCSV)
			requests, err := buildRenderRequestsByNode(cmd.Context(), store, selectedNodeIDs)
			if err != nil {
				return err
			}

			if _, err := lookPath("ssh"); err != nil {
				return fmt.Errorf("ssh client is required: %w", err)
			}
			if _, err := lookPath("scp"); err != nil {
				return fmt.Errorf("scp client is required: %w", err)
			}

			results := make([]nodeSyncResult, 0, len(requests))
			for _, req := range requests {
				result, syncErr := syncSingleNode(cmd.Context(), req, nodeSyncOptions{
					sshUser:       sshUser,
					sshPort:       sshPort,
					sshKeyPath:    sshKeyPath,
					runtimeDir:    runtimeDir,
					restart:       restart,
					strictHostKey: strictHostKey,
					remoteUseSudo: remoteUseSudo,
				}, appCfg)
				if syncErr != nil {
					return syncErr
				}
				results = append(results, result)
			}

			for _, res := range results {
				fmt.Fprintf(cmd.OutOrStdout(), "node synced: id=%s host=%s\n", res.NodeID, res.Host)
				for _, uploaded := range res.Uploaded {
					fmt.Fprintf(cmd.OutOrStdout(), "  uploaded: %s\n", uploaded)
				}
				for _, unit := range res.Restart {
					fmt.Fprintf(cmd.OutOrStdout(), "  restarted: %s\n", unit)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&nodeIDsCSV, "node-ids", "", "Comma-separated node IDs; default is all enabled nodes with inbounds")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH user")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "Path to private SSH key")
	cmd.Flags().StringVar(&runtimeDir, "runtime-dir", "/etc/proxy-orchestrator/runtime", "Remote runtime directory for sing-box/xray configs")
	cmd.Flags().BoolVar(&restart, "restart", true, "Restart required runtime services on remote nodes")
	cmd.Flags().BoolVar(&strictHostKey, "strict-host-key", false, "Use strict SSH host key checking")
	cmd.Flags().BoolVar(&remoteUseSudo, "remote-sudo", false, "Use sudo for remote file install and systemctl restart (default: auto, false when --ssh-user=root)")
	return cmd
}

type nodeSyncOptions struct {
	sshUser       string
	sshPort       int
	sshKeyPath    string
	sshPassword   string
	runtimeDir    string
	restart       bool
	strictHostKey bool
	remoteUseSudo bool
}

func syncSingleNode(ctx context.Context, req renderer.BuildRequest, opts nodeSyncOptions, appCfg config.AppConfig) (nodeSyncResult, error) {
	host := strings.TrimSpace(req.Node.Host)
	if host == "" {
		return nodeSyncResult{}, fmt.Errorf("node %q has empty host", req.Node.ID)
	}

	singResult, err := singbox.New(nil).Render(ctx, req)
	if err != nil {
		return nodeSyncResult{}, fmt.Errorf("render sing-box for node %q: %w", req.Node.ID, err)
	}
	xrayResult, err := xray.New(nil).Render(ctx, req)
	if err != nil {
		return nodeSyncResult{}, fmt.Errorf("render xray for node %q: %w", req.Node.ID, err)
	}

	// Build Caddyfile for ACME cert management. Errors here are non-fatal:
	// if the node has no inbounds that need caddy, we simply skip caddy sync.
	caddyResult, caddyBuildErr := caddy.New(appCfg).Build(caddy.BuildRequest{
		Node:     req.Node,
		Inbounds: req.Inbounds,
	})
	hasCaddyConfig := caddyBuildErr == nil && len(caddyResult.Caddyfile) > 0

	tmpDir, err := os.MkdirTemp("", "proxyctl-node-sync-")
	if err != nil {
		return nodeSyncResult{}, fmt.Errorf("create temp dir for node %q: %w", req.Node.ID, err)
	}
	defer os.RemoveAll(tmpDir)

	singLocal := filepath.Join(tmpDir, "sing-box.json")
	xrayLocal := filepath.Join(tmpDir, "xray.json")
	syncedInboundsLocal := filepath.Join(tmpDir, syncedInboundsFileName)
	if err := os.WriteFile(singLocal, selectPreviewContent(singResult), 0o600); err != nil {
		return nodeSyncResult{}, fmt.Errorf("write temp sing-box for node %q: %w", req.Node.ID, err)
	}
	if err := os.WriteFile(xrayLocal, selectPreviewContent(xrayResult), 0o600); err != nil {
		return nodeSyncResult{}, fmt.Errorf("write temp xray for node %q: %w", req.Node.ID, err)
	}
	syncedPayload, err := buildSyncedInboundsSnapshot(req)
	if err != nil {
		return nodeSyncResult{}, fmt.Errorf("build synced inbounds snapshot for node %q: %w", req.Node.ID, err)
	}
	if err := os.WriteFile(syncedInboundsLocal, syncedPayload, 0o600); err != nil {
		return nodeSyncResult{}, fmt.Errorf("write temp synced inbounds snapshot for node %q: %w", req.Node.ID, err)
	}

	caddyfileLocal := ""
	if hasCaddyConfig {
		caddyfileLocal = filepath.Join(tmpDir, "Caddyfile")
		if err := os.WriteFile(caddyfileLocal, caddyResult.Caddyfile, 0o644); err != nil {
			return nodeSyncResult{}, fmt.Errorf("write temp Caddyfile for node %q: %w", req.Node.ID, err)
		}
	}

	target := fmt.Sprintf("%s@%s", opts.sshUser, host)
	singRemoteTmp := fmt.Sprintf("/tmp/proxyctl-%s-sing-box.json", req.Node.ID)
	xrayRemoteTmp := fmt.Sprintf("/tmp/proxyctl-%s-xray.json", req.Node.ID)
	syncedInboundsRemoteTmp := fmt.Sprintf("/tmp/proxyctl-%s-%s", req.Node.ID, syncedInboundsFileName)
	caddyfileRemoteTmp := fmt.Sprintf("/tmp/proxyctl-%s-Caddyfile", req.Node.ID)

	scpBase := buildSCPArgs(opts.sshPort, opts.sshKeyPath, opts.strictHostKey)
	singSCP := append(append([]string{}, scpBase...), singLocal, fmt.Sprintf("%s:%s", target, singRemoteTmp))
	if out, err := runRemoteExecCombined(ctx, "scp", singSCP, opts.sshPassword); err != nil {
		return nodeSyncResult{}, fmt.Errorf("upload sing-box config to node %q (%s): %w\n%s", req.Node.ID, host, err, strings.TrimSpace(string(out)))
	}
	xraySCP := append(append([]string{}, scpBase...), xrayLocal, fmt.Sprintf("%s:%s", target, xrayRemoteTmp))
	if out, err := runRemoteExecCombined(ctx, "scp", xraySCP, opts.sshPassword); err != nil {
		return nodeSyncResult{}, fmt.Errorf("upload xray config to node %q (%s): %w\n%s", req.Node.ID, host, err, strings.TrimSpace(string(out)))
	}
	syncedSCP := append(append([]string{}, scpBase...), syncedInboundsLocal, fmt.Sprintf("%s:%s", target, syncedInboundsRemoteTmp))
	if out, err := runRemoteExecCombined(ctx, "scp", syncedSCP, opts.sshPassword); err != nil {
		return nodeSyncResult{}, fmt.Errorf("upload synced inbounds snapshot to node %q (%s): %w\n%s", req.Node.ID, host, err, strings.TrimSpace(string(out)))
	}
	if hasCaddyConfig {
		caddyfileSCP := append(append([]string{}, scpBase...), caddyfileLocal, fmt.Sprintf("%s:%s", target, caddyfileRemoteTmp))
		if out, err := runRemoteExecCombined(ctx, "scp", caddyfileSCP, opts.sshPassword); err != nil {
			return nodeSyncResult{}, fmt.Errorf("upload Caddyfile to node %q (%s): %w\n%s", req.Node.ID, host, err, strings.TrimSpace(string(out)))
		}
	}

	prefix := ""
	if opts.remoteUseSudo {
		prefix = "sudo "
	}

	remoteCaddyDir := strings.TrimSpace(appCfg.Paths.CaddyDir)
	if remoteCaddyDir == "" {
		remoteCaddyDir = "/etc/proxy-orchestrator/runtime/caddy"
	}

	installCmdParts := []string{
		"set -e",
		prefix + "mkdir -p " + shellQuote(opts.runtimeDir),
		prefix + "install -m 640 " + shellQuote(singRemoteTmp) + " " + shellQuote(filepath.Join(opts.runtimeDir, "sing-box.json")),
		prefix + "install -m 640 " + shellQuote(xrayRemoteTmp) + " " + shellQuote(filepath.Join(opts.runtimeDir, "xray.json")),
		prefix + "install -m 640 " + shellQuote(syncedInboundsRemoteTmp) + " " + shellQuote(filepath.Join(opts.runtimeDir, syncedInboundsFileName)),
	}
	cleanupTmps := []string{shellQuote(singRemoteTmp), shellQuote(xrayRemoteTmp), shellQuote(syncedInboundsRemoteTmp)}
	if hasCaddyConfig {
		installCmdParts = append(installCmdParts,
			prefix+"mkdir -p "+shellQuote(remoteCaddyDir),
			prefix+"install -m 644 "+shellQuote(caddyfileRemoteTmp)+" "+shellQuote(filepath.Join(remoteCaddyDir, "Caddyfile")),
		)
		cleanupTmps = append(cleanupTmps, shellQuote(caddyfileRemoteTmp))
	}
	installCmdParts = append(installCmdParts, "rm -f "+strings.Join(cleanupTmps, " "))
	installCmd := strings.Join(installCmdParts, "; ")

	sshArgs := buildSSHArgs(opts.sshPort, opts.sshKeyPath, opts.strictHostKey)
	sshArgs = append(sshArgs, target, installCmd)
	if out, err := runRemoteExecCombined(ctx, "ssh", sshArgs, opts.sshPassword); err != nil {
		return nodeSyncResult{}, fmt.Errorf("install configs on node %q (%s): %w\n%s", req.Node.ID, host, err, strings.TrimSpace(string(out)))
	}

	uploadedFiles := []string{
		filepath.Join(opts.runtimeDir, "sing-box.json"),
		filepath.Join(opts.runtimeDir, "xray.json"),
		filepath.Join(opts.runtimeDir, syncedInboundsFileName),
	}
	if hasCaddyConfig {
		uploadedFiles = append(uploadedFiles, filepath.Join(remoteCaddyDir, "Caddyfile"))
	}

	restartedUnits := make([]string, 0, 3)
	if opts.restart {
		// Reload caddy BEFORE restarting other services so that ACME cert
		// acquisition starts as early as possible. sing-box/xray may need
		// the cert on startup; reloading caddy first gives it the best
		// chance of already having the cert on subsequent syncs.
		if hasCaddyConfig && strings.TrimSpace(appCfg.Runtime.CaddyUnit) != "" {
			caddyUnit := strings.TrimSpace(appCfg.Runtime.CaddyUnit)
			reloadCmd := strings.TrimSpace(prefix + "systemctl reload-or-restart " + shellQuote(caddyUnit))
			sshReloadArgs := buildSSHArgs(opts.sshPort, opts.sshKeyPath, opts.strictHostKey)
			sshReloadArgs = append(sshReloadArgs, target, reloadCmd)
			if out, err := runRemoteExecCombined(ctx, "ssh", sshReloadArgs, opts.sshPassword); err != nil {
				return nodeSyncResult{}, fmt.Errorf("reload caddy on node %q (%s): %w\n%s", req.Node.ID, host, err, strings.TrimSpace(string(out)))
			}
			restartedUnits = append(restartedUnits, caddyUnit)
		}
		units := requiredRuntimeUnits(req, appCfg)
		for _, unit := range units {
			restartCmd := strings.TrimSpace(prefix + "systemctl restart " + shellQuote(unit))
			sshRestartArgs := buildSSHArgs(opts.sshPort, opts.sshKeyPath, opts.strictHostKey)
			sshRestartArgs = append(sshRestartArgs, target, restartCmd)
			if out, err := runRemoteExecCombined(ctx, "ssh", sshRestartArgs, opts.sshPassword); err != nil {
				return nodeSyncResult{}, fmt.Errorf("restart unit %q on node %q (%s): %w\n%s", unit, req.Node.ID, host, err, strings.TrimSpace(string(out)))
			}
			restartedUnits = append(restartedUnits, unit)
		}
	}

	return nodeSyncResult{
		NodeID:   req.Node.ID,
		Host:     host,
		Uploaded: uploadedFiles,
		Restart:  restartedUnits,
	}, nil
}

// syncPrimaryNodeCaddy builds the Caddyfile for the primary node's inbounds,
// writes it to the configured caddy directory, and triggers a caddy reload so
// ACME cert acquisition starts before sing-box/xray are restarted.
// Returns nil when no caddy configuration is needed (non-fatal errors are
// suppressed so that caddy issues do not block the overall apply pipeline).
func syncPrimaryNodeCaddy(ctx context.Context, primaryNode domain.Node, inbounds []domain.Inbound, appCfg config.AppConfig) error {
	buildReq := caddy.BuildRequest{
		Node:     primaryNode,
		Inbounds: inbounds,
	}
	// Preserve panel route: read panel-admin.env and inject into Caddyfile so
	// sync does not wipe the reverse-proxy route that install.sh added.
	if panelInfo, panelErr := readPanelAccessInfo(panelCredentialsPathFromConfig(appCfg.Paths.ConfigFile)); panelErr == nil {
		if p := strings.TrimSpace(panelInfo.Path); p != "" {
			buildReq.PanelPath = p
		}
		if port := strings.TrimSpace(panelInfo.Port); port != "" {
			buildReq.PanelPort = port
		} else {
			buildReq.PanelPort = "20443"
		}
	}
	caddyResult, err := caddy.New(appCfg).Build(buildReq)
	if err != nil {
		// No caddy config needed for this node (e.g. no TLS inbounds).
		return nil
	}
	if len(caddyResult.Caddyfile) == 0 {
		return nil
	}

	caddyDir := strings.TrimSpace(appCfg.Paths.CaddyDir)
	if caddyDir == "" {
		caddyDir = "/etc/proxy-orchestrator/runtime/caddy"
	}
	if err := os.MkdirAll(caddyDir, 0o755); err != nil {
		return fmt.Errorf("create caddy config dir for primary node: %w", err)
	}
	caddyfilePath := filepath.Join(caddyDir, "Caddyfile")
	if err := os.WriteFile(caddyfilePath, caddyResult.Caddyfile, 0o644); err != nil {
		return fmt.Errorf("write Caddyfile for primary node: %w", err)
	}

	caddyUnit := strings.TrimSpace(appCfg.Runtime.CaddyUnit)
	if caddyUnit == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "systemctl", "reload-or-restart", caddyUnit)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reload caddy for primary node: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Wait for caddy to obtain TLS certificates before returning, so that
	// xray/sing-box (started immediately after) can load them successfully.
	waitForCaddyCerts(ctx, caddyResult.Domains)
	return nil
}

// waitForCaddyCerts polls /caddy/certificates/*/domain/domain.crt for each
// domain until all certs exist or the deadline is reached (60 s).
func waitForCaddyCerts(ctx context.Context, domains []string) {
	if len(domains) == 0 {
		return
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		allFound := true
		for _, domain := range domains {
			pattern := filepath.Join("/caddy/certificates/*", domain, domain+".crt")
			matches, err := filepath.Glob(pattern)
			if err != nil || len(matches) == 0 {
				allFound = false
				break
			}
		}
		if allFound {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func cleanupSingleNodeRuntime(ctx context.Context, node domain.Node, opts nodeSyncOptions, appCfg config.AppConfig) (nodeSyncResult, error) {
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return nodeSyncResult{}, fmt.Errorf("node %q has empty host", node.ID)
	}

	target := fmt.Sprintf("%s@%s", opts.sshUser, host)
	prefix := ""
	if opts.remoteUseSudo {
		prefix = "sudo "
	}

	cleanupCmdParts := []string{
		"set -e",
		prefix + "mkdir -p " + shellQuote(opts.runtimeDir),
		prefix + "rm -f " + shellQuote(filepath.Join(opts.runtimeDir, "sing-box.json")),
		prefix + "rm -f " + shellQuote(filepath.Join(opts.runtimeDir, "xray.json")),
		prefix + "rm -f " + shellQuote(filepath.Join(opts.runtimeDir, syncedInboundsFileName)),
	}
	cleanupCmd := strings.Join(cleanupCmdParts, "; ")

	sshArgs := buildSSHArgs(opts.sshPort, opts.sshKeyPath, opts.strictHostKey)
	sshArgs = append(sshArgs, target, cleanupCmd)
	if out, err := runRemoteExecCombined(ctx, "ssh", sshArgs, opts.sshPassword); err != nil {
		return nodeSyncResult{}, fmt.Errorf("cleanup runtime configs on node %q (%s): %w\n%s", node.ID, host, err, strings.TrimSpace(string(out)))
	}

	stoppedUnits := make([]string, 0, 2)
	if opts.restart {
		units := []string{}
		if strings.TrimSpace(appCfg.Runtime.SingBoxUnit) != "" {
			units = append(units, strings.TrimSpace(appCfg.Runtime.SingBoxUnit))
		}
		if strings.TrimSpace(appCfg.Runtime.XrayUnit) != "" {
			units = append(units, strings.TrimSpace(appCfg.Runtime.XrayUnit))
		}
		sort.Strings(units)
		for _, unit := range units {
			stopCmd := strings.TrimSpace(prefix + "systemctl stop " + shellQuote(unit))
			sshStopArgs := buildSSHArgs(opts.sshPort, opts.sshKeyPath, opts.strictHostKey)
			sshStopArgs = append(sshStopArgs, target, stopCmd)
			if out, err := runRemoteExecCombined(ctx, "ssh", sshStopArgs, opts.sshPassword); err != nil {
				return nodeSyncResult{}, fmt.Errorf("stop unit %q on node %q (%s): %w\n%s", unit, node.ID, host, err, strings.TrimSpace(string(out)))
			}
			stoppedUnits = append(stoppedUnits, unit)
		}
	}

	return nodeSyncResult{
		NodeID: node.ID,
		Host:   host,
		Uploaded: []string{
			filepath.Join(opts.runtimeDir, "sing-box.json") + " (removed)",
			filepath.Join(opts.runtimeDir, "xray.json") + " (removed)",
			filepath.Join(opts.runtimeDir, syncedInboundsFileName) + " (removed)",
		},
		Restart: stoppedUnits,
	}, nil
}

// uninstallSingleNode fully removes proxyctl and all its data from a remote node via SSH.
// This is called only when permanently deleting a node; for disable/update use cleanupSingleNodeRuntime.
func uninstallSingleNode(ctx context.Context, node domain.Node, opts nodeSyncOptions, appCfg config.AppConfig) error {
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return fmt.Errorf("node %q has empty host", node.ID)
	}

	target := fmt.Sprintf("%s@%s", opts.sshUser, host)
	prefix := ""
	if opts.remoteUseSudo {
		prefix = "sudo "
	}

	units := []string{}
	for _, u := range []string{
		strings.TrimSpace(appCfg.Runtime.SingBoxUnit),
		strings.TrimSpace(appCfg.Runtime.XrayUnit),
		strings.TrimSpace(appCfg.Runtime.CaddyUnit),
		strings.TrimSpace(appCfg.Runtime.NginxUnit),
	} {
		if u != "" {
			units = append(units, u)
		}
	}

	parts := []string{"set -e"}

	systemdDir := strings.TrimSpace(appCfg.Paths.SystemdUnits)
	for _, unit := range units {
		parts = append(parts,
			prefix+"systemctl stop "+shellQuote(unit)+" 2>/dev/null || true",
			prefix+"systemctl disable "+shellQuote(unit)+" 2>/dev/null || true",
		)
		if systemdDir != "" {
			parts = append(parts, prefix+"rm -f "+shellQuote(filepath.Join(systemdDir, unit)))
		}
	}
	if len(units) > 0 {
		parts = append(parts, prefix+"systemctl daemon-reload 2>/dev/null || true")
	}

	if baseDir := strings.TrimSpace(appCfg.Paths.BaseDir); baseDir != "" && baseDir != "/" {
		parts = append(parts, prefix+"rm -rf "+shellQuote(baseDir))
	}
	if stateDir := strings.TrimSpace(appCfg.Paths.StateDir); stateDir != "" && stateDir != "/" {
		parts = append(parts, prefix+"rm -rf "+shellQuote(stateDir))
	}

	// Caddy data directory contains ACME certificates
	parts = append(parts, prefix+"rm -rf /caddy 2>/dev/null || true")

	// Remove proxyctl and proxy engine binaries installed by proxyctl
	if binDir := strings.TrimSpace(appCfg.Paths.BinDir); binDir != "" {
		for _, bin := range []string{"proxyctl", "xray", "sing-box"} {
			parts = append(parts, prefix+"rm -f "+shellQuote(filepath.Join(binDir, bin))+" 2>/dev/null || true")
		}
	}

	cmd := strings.Join(parts, "; ")
	sshArgs := buildSSHArgs(opts.sshPort, opts.sshKeyPath, opts.strictHostKey)
	sshArgs = append(sshArgs, target, cmd)
	if out, err := runRemoteExecCombined(ctx, "ssh", sshArgs, opts.sshPassword); err != nil {
		return fmt.Errorf("uninstall node %q (%s): %w\n%s", node.ID, host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func requiredRuntimeUnits(req renderer.BuildRequest, appCfg config.AppConfig) []string {
	// Always restart all configured runtime units so that services whose
	// engine is no longer used reload the (now-empty) config and release
	// any ports they were holding from a previous deployment.
	units := make([]string, 0, 2)
	if strings.TrimSpace(appCfg.Runtime.SingBoxUnit) != "" {
		units = append(units, appCfg.Runtime.SingBoxUnit)
	}
	if strings.TrimSpace(appCfg.Runtime.XrayUnit) != "" {
		units = append(units, appCfg.Runtime.XrayUnit)
	}
	sort.Strings(units)
	return units
}

func buildRenderRequestsByNode(ctx context.Context, store *sqlite.Store, nodeIDs []string) ([]renderer.BuildRequest, error) {
	nodes, err := store.Nodes().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes found")
	}

	nodeFilter := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		id := strings.TrimSpace(nodeID)
		if id == "" {
			continue
		}
		nodeFilter[id] = struct{}{}
	}

	enabledNodeByID := make(map[string]domain.Node, len(nodes))
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		if len(nodeFilter) > 0 {
			if _, ok := nodeFilter[node.ID]; !ok {
				continue
			}
		}
		enabledNodeByID[node.ID] = node
	}
	if len(enabledNodeByID) == 0 {
		if len(nodeFilter) > 0 {
			return nil, fmt.Errorf("no enabled nodes found for requested IDs")
		}
		return nil, fmt.Errorf("no enabled nodes found")
	}

	inbounds, err := store.Inbounds().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list inbounds: %w", err)
	}
	credentials, err := store.Credentials().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}

	inboundsByNode := make(map[string][]domain.Inbound)
	inboundNodeIndex := make(map[string]string)
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		if _, ok := enabledNodeByID[inbound.NodeID]; !ok {
			continue
		}
		inboundsByNode[inbound.NodeID] = append(inboundsByNode[inbound.NodeID], inbound)
		inboundNodeIndex[inbound.ID] = inbound.NodeID
	}

	credsByNode := make(map[string][]domain.Credential)
	for _, cred := range credentials {
		nodeID, ok := inboundNodeIndex[cred.InboundID]
		if !ok {
			continue
		}
		credsByNode[nodeID] = append(credsByNode[nodeID], cred)
	}

	nodeIDsSorted := make([]string, 0, len(inboundsByNode))
	for nodeID := range inboundsByNode {
		nodeIDsSorted = append(nodeIDsSorted, nodeID)
	}
	sort.Strings(nodeIDsSorted)

	requests := make([]renderer.BuildRequest, 0, len(nodeIDsSorted))
	for _, nodeID := range nodeIDsSorted {
		node := enabledNodeByID[nodeID]
		nodeInbounds := inboundsByNode[nodeID]
		sort.Slice(nodeInbounds, func(i, j int) bool { return nodeInbounds[i].ID < nodeInbounds[j].ID })
		nodeCreds := credsByNode[nodeID]
		sort.Slice(nodeCreds, func(i, j int) bool { return nodeCreds[i].ID < nodeCreds[j].ID })
		requests = append(requests, renderer.BuildRequest{
			Node:        node,
			Inbounds:    nodeInbounds,
			Credentials: nodeCreds,
		})
	}

	if len(requests) == 0 {
		if len(nodeFilter) > 0 {
			return nil, fmt.Errorf("no enabled inbounds found for requested node IDs")
		}
		return nil, fmt.Errorf("no enabled inbounds found for enabled nodes")
	}

	return requests, nil
}

func buildSSHArgs(port int, keyPath string, strictHostKey bool) []string {
	args := []string{"-p", fmt.Sprintf("%d", port)}
	if strings.TrimSpace(keyPath) != "" {
		args = append(args, "-i", strings.TrimSpace(keyPath))
	}
	if !strictHostKey {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	return args
}

func buildSCPArgs(port int, keyPath string, strictHostKey bool) []string {
	args := []string{"-P", fmt.Sprintf("%d", port)}
	if strings.TrimSpace(keyPath) != "" {
		args = append(args, "-i", strings.TrimSpace(keyPath))
	}
	if !strictHostKey {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	return args
}

func runExecCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	return command.CombinedOutput()
}

func runRemoteExecCombined(ctx context.Context, binary string, args []string, sshPassword string) ([]byte, error) {
	password := strings.TrimSpace(sshPassword)
	if password == "" {
		return runExecCombined(ctx, binary, args...)
	}
	if _, err := lookPath("sshpass"); err != nil {
		if installErr := ensureSSHPassInstalled(ctx); installErr != nil {
			return nil, fmt.Errorf("ssh password auth requested, but sshpass is not installed (install: apt-get update && apt-get install -y sshpass): %w", installErr)
		}
	}
	sshpassArgs := make([]string, 0, len(args)+3)
	sshpassArgs = append(sshpassArgs, "-p", password, binary)
	sshpassArgs = append(sshpassArgs, args...)
	return runExecCombined(ctx, "sshpass", sshpassArgs...)
}

func ensureSSHPassInstalled(ctx context.Context) error {
	if _, err := lookPath("sshpass"); err == nil {
		return nil
	}
	if _, err := lookPath("apt-get"); err != nil {
		return fmt.Errorf("sshpass missing and apt-get is not available")
	}

	asRoot := os.Geteuid() == 0
	if asRoot {
		if out, err := runExecCombined(ctx, "apt-get", "update"); err != nil {
			return fmt.Errorf("apt-get update failed: %w | %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := runExecCombined(ctx, "apt-get", "install", "-y", "sshpass"); err != nil {
			return fmt.Errorf("apt-get install sshpass failed: %w | %s", err, strings.TrimSpace(string(out)))
		}
	} else {
		if _, err := lookPath("sudo"); err != nil {
			return fmt.Errorf("sshpass missing and panel process is not root; install manually: apt-get update && apt-get install -y sshpass")
		}
		if out, err := runExecCombined(ctx, "sudo", "apt-get", "update"); err != nil {
			return fmt.Errorf("sudo apt-get update failed: %w | %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := runExecCombined(ctx, "sudo", "apt-get", "install", "-y", "sshpass"); err != nil {
			return fmt.Errorf("sudo apt-get install sshpass failed: %w | %s", err, strings.TrimSpace(string(out)))
		}
	}
	if _, err := lookPath("sshpass"); err != nil {
		return fmt.Errorf("sshpass still not found in PATH after install")
	}
	return nil
}
