package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
	applyruntime "proxyctl/internal/runtime/apply"
)

func TestSelectPrimaryNodePrefersPrimaryRole(t *testing.T) {
	t.Parallel()

	nodes := []domain.Node{
		{
			ID:        "node-worker",
			Role:      domain.NodeRoleNode,
			Enabled:   true,
			CreatedAt: time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC),
		},
		{
			ID:        "node-primary",
			Role:      domain.NodeRolePrimary,
			Enabled:   true,
			CreatedAt: time.Date(2026, 3, 16, 10, 1, 0, 0, time.UTC),
		},
	}

	got, err := selectPrimaryNode(nodes)
	if err != nil {
		t.Fatalf("selectPrimaryNode() error: %v", err)
	}
	if got.ID != "node-primary" {
		t.Fatalf("selected node id = %s, want node-primary", got.ID)
	}
}

func TestSelectPrimaryNodeFallsBackWhenNoPrimary(t *testing.T) {
	t.Parallel()

	nodes := []domain.Node{
		{
			ID:        "node-b",
			Role:      domain.NodeRoleNode,
			Enabled:   true,
			CreatedAt: time.Date(2026, 3, 16, 10, 2, 0, 0, time.UTC),
		},
		{
			ID:        "node-a",
			Role:      domain.NodeRoleNode,
			Enabled:   true,
			CreatedAt: time.Date(2026, 3, 16, 10, 1, 0, 0, time.UTC),
		},
	}

	got, err := selectPrimaryNode(nodes)
	if err != nil {
		t.Fatalf("selectPrimaryNode() error: %v", err)
	}
	if got.ID != "node-a" {
		t.Fatalf("selected node id = %s, want node-a", got.ID)
	}
}

func TestPromptChoiceShowsBackOnlyAsZero(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	in := bufio.NewReader(strings.NewReader("0\n"))
	got, err := promptChoice(in, &out, "Users", []string{
		"list users",
		"create user",
		"open user",
		"back",
	}, "list users")
	if err != nil {
		t.Fatalf("promptChoice() error: %v", err)
	}
	if got != "back" {
		t.Fatalf("choice = %q, want back", got)
	}
	rendered := out.String()
	if strings.Contains(rendered, "4) back") {
		t.Fatalf("menu contains duplicated numeric back option: %q", rendered)
	}
	if !strings.Contains(rendered, "0) back") {
		t.Fatalf("menu missing 0) back option: %q", rendered)
	}
}

func TestPromptChoiceNormalizesBackAliases(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	in := bufio.NewReader(strings.NewReader("0\n"))
	got, err := promptChoice(in, &out, "Users", []string{
		"list users",
		"0) back",
		"Back",
	}, "list users")
	if err != nil {
		t.Fatalf("promptChoice() error: %v", err)
	}
	if got != "back" {
		t.Fatalf("choice = %q, want back", got)
	}
	rendered := out.String()
	if strings.Contains(rendered, "2) 0) back") || strings.Contains(rendered, "3) Back") {
		t.Fatalf("menu leaked back aliases into numbered list: %q", rendered)
	}
	if !strings.Contains(rendered, "0) back") {
		t.Fatalf("menu missing normalized back option: %q", rendered)
	}
}

func TestInboundAddRejectsPort443ByDefault(t *testing.T) {
	t.Parallel()

	configPath := "/tmp/proxyctl-test.yaml"
	dbPath := "/tmp/proxyctl-test.db"
	cmd := newInboundAddCmd(&configPath, &dbPath)
	cmd.SetArgs([]string{
		"--type", "vless",
		"--transport", "tcp",
		"--node-id", "node-1",
		"--port", "443",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for reserved port 443, got nil")
	}
	if !strings.Contains(err.Error(), "reserved by default") {
		t.Fatalf("error = %q, want reserved-port guidance", err)
	}
}

func TestSuggestWizardPortSkipsConfiguredBusyPort(t *testing.T) {
	t.Parallel()

	used := map[int]struct{}{8443: {}}
	got := suggestWizardPort("vless", "tcp", used, func(network string, port int) bool {
		return false
	})
	if got != 9443 {
		t.Fatalf("suggested port = %d, want 9443", got)
	}
}

func TestSuggestWizardPortSkipsHostBusyPort(t *testing.T) {
	t.Parallel()

	got := suggestWizardPort("vless", "tcp", map[int]struct{}{}, func(network string, port int) bool {
		return port == 8443
	})
	if got != 9443 {
		t.Fatalf("suggested port = %d, want 9443 when 8443 is busy", got)
	}
}

func TestBuildWizardUserMenuOptions(t *testing.T) {
	t.Parallel()

	options := buildWizardUserMenuOptions()
	got := strings.Join(options, "|")
	if !strings.Contains(got, "subscriptions") {
		t.Fatalf("options do not contain subscriptions: %v", options)
	}
}

func TestParseIndexCSV(t *testing.T) {
	t.Parallel()

	got, err := parseIndexCSV("3,1,1,2", 3)
	if err != nil {
		t.Fatalf("parseIndexCSV() error: %v", err)
	}
	if strings.Join([]string{fmt.Sprint(got[0]), fmt.Sprint(got[1]), fmt.Sprint(got[2])}, ",") != "1,2,3" {
		t.Fatalf("indexes = %v, want [1 2 3]", got)
	}
}

func TestWizardNormalizeProfileName(t *testing.T) {
	t.Parallel()

	got := wizardNormalizeProfileName(" Super Test !!! ")
	if got != "super-test" {
		t.Fatalf("normalized profile = %q, want %q", got, "super-test")
	}
}

func TestEnsureCaddyServiceHealthyStartsInactiveService(t *testing.T) {
	origLookPath := lookPath
	origRun := runCommandOutput
	t.Cleanup(func() {
		lookPath = origLookPath
		runCommandOutput = origRun
	})

	lookPath = func(file string) (string, error) {
		if file == "systemctl" {
			return "/bin/systemctl", nil
		}
		return "", fmt.Errorf("not found")
	}

	active := false
	runCommandOutput = func(ctx context.Context, name string, args ...string) (string, error) {
		if name != "systemctl" {
			return "", fmt.Errorf("unexpected command: %s", name)
		}
		key := strings.Join(args, " ")
		switch key {
		case "show proxyctl-caddy.service --property=LoadState --value":
			return "loaded", nil
		case "is-active proxyctl-caddy.service":
			if active {
				return "active", nil
			}
			return "inactive", fmt.Errorf("inactive")
		case "enable --now proxyctl-caddy.service":
			active = true
			return "", nil
		default:
			return "", fmt.Errorf("unexpected args: %s", key)
		}
	}

	var out bytes.Buffer
	if err := ensureCaddyServiceHealthy(context.Background(), &out); err != nil {
		t.Fatalf("ensureCaddyServiceHealthy() error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "caddy service is inactive") {
		t.Fatalf("output = %q, expected inactive message", text)
	}
	if !strings.Contains(text, "caddy service started and active") {
		t.Fatalf("output = %q, expected started message", text)
	}
}

func TestEnsureCaddyServiceHealthySkipsWithoutSystemctl(t *testing.T) {
	origLookPath := lookPath
	origRun := runCommandOutput
	t.Cleanup(func() {
		lookPath = origLookPath
		runCommandOutput = origRun
	})

	lookPath = func(file string) (string, error) {
		return "", fmt.Errorf("not found")
	}
	runCommandOutput = func(ctx context.Context, name string, args ...string) (string, error) {
		t.Fatalf("runCommandOutput should not be called when systemctl is unavailable")
		return "", nil
	}

	var out bytes.Buffer
	if err := ensureCaddyServiceHealthy(context.Background(), &out); err != nil {
		t.Fatalf("ensureCaddyServiceHealthy() error: %v", err)
	}
	if !strings.Contains(out.String(), "systemctl not found") {
		t.Fatalf("output = %q, expected skip message", out.String())
	}
}

func TestWizardMainOptionsWithoutNodes(t *testing.T) {
	t.Parallel()

	options, def := wizardMainOptions(false)
	joined := strings.Join(options, ",")
	if strings.Contains(joined, "inbounds") || strings.Contains(joined, "users") {
		t.Fatalf("options should hide inbounds/users when no nodes, got %v", options)
	}
	if !strings.Contains(joined, "settings") {
		t.Fatalf("options should include settings, got %v", options)
	}
	if !strings.Contains(joined, "uninstall proxyctl") {
		t.Fatalf("options should include uninstall proxyctl, got %v", options)
	}
	if !strings.Contains(joined, "uninstall all") {
		t.Fatalf("options should include uninstall all, got %v", options)
	}
	if def != "nodes" {
		t.Fatalf("default action = %q, want nodes", def)
	}
}

func TestWizardMainOptionsWithNodes(t *testing.T) {
	t.Parallel()

	options, def := wizardMainOptions(true)
	joined := strings.Join(options, ",")
	if !strings.Contains(joined, "inbounds") || !strings.Contains(joined, "users") {
		t.Fatalf("options should include inbounds/users when nodes exist, got %v", options)
	}
	if !strings.Contains(joined, "settings") {
		t.Fatalf("options should include settings, got %v", options)
	}
	if !strings.Contains(joined, "uninstall proxyctl") {
		t.Fatalf("options should include uninstall proxyctl, got %v", options)
	}
	if !strings.Contains(joined, "uninstall all") {
		t.Fatalf("options should include uninstall all, got %v", options)
	}
	if def != "inbounds" {
		t.Fatalf("default action = %q, want inbounds", def)
	}
}

func TestWizardMainOptionsByModePanel(t *testing.T) {
	t.Parallel()

	options, def := wizardMainOptionsByMode(false, config.DeploymentModePanel)
	joined := strings.Join(options, ",")
	if strings.Contains(joined, "inbounds") {
		t.Fatalf("panel mode should not include inbounds, got %v", options)
	}
	if !strings.Contains(joined, "nodes") || !strings.Contains(joined, "users") {
		t.Fatalf("panel mode should include nodes/users, got %v", options)
	}
	if !strings.Contains(joined, "uninstall all") {
		t.Fatalf("panel mode should include uninstall all, got %v", options)
	}
	if def != "nodes" {
		t.Fatalf("default action = %q, want nodes", def)
	}
}

func TestWizardMainOptionsByModeNode(t *testing.T) {
	t.Parallel()

	options, def := wizardMainOptionsByMode(false, config.DeploymentModeNode)
	joined := strings.Join(options, ",")
	if strings.Contains(joined, "nodes") {
		t.Fatalf("node mode should not include nodes, got %v", options)
	}
	if !strings.Contains(joined, "inbounds") || !strings.Contains(joined, "users") {
		t.Fatalf("node mode should include inbounds/users, got %v", options)
	}
	if !strings.Contains(joined, "uninstall all") {
		t.Fatalf("node mode should include uninstall all, got %v", options)
	}
	if def != "inbounds" {
		t.Fatalf("default action = %q, want inbounds", def)
	}
}

func TestWizardMainOptionsByModeEmptyFallsBack(t *testing.T) {
	t.Parallel()

	options, def := wizardMainOptionsByMode(false, "")
	joined := strings.Join(options, ",")
	if strings.Contains(joined, "inbounds") || strings.Contains(joined, "users") {
		t.Fatalf("empty mode should fallback to panel+node/no-nodes behavior, got %v", options)
	}
	if def != "nodes" {
		t.Fatalf("default action = %q, want nodes", def)
	}
}

func TestWizardNodeRoleOptionsForCreateWithoutPrimary(t *testing.T) {
	t.Parallel()

	options, def := wizardNodeRoleOptionsForCreate(false)
	if len(options) != 2 {
		t.Fatalf("options len = %d, want 2 (%v)", len(options), options)
	}
	if options[0] != string(domain.NodeRolePrimary) || options[1] != string(domain.NodeRoleNode) {
		t.Fatalf("options = %v, want [primary node]", options)
	}
	if def != string(domain.NodeRolePrimary) {
		t.Fatalf("default role = %q, want primary", def)
	}
}

func TestWizardNodeRoleOptionsForCreateWithPrimary(t *testing.T) {
	t.Parallel()

	options, def := wizardNodeRoleOptionsForCreate(true)
	if len(options) != 1 {
		t.Fatalf("options len = %d, want 1 (%v)", len(options), options)
	}
	if options[0] != string(domain.NodeRoleNode) {
		t.Fatalf("options = %v, want [node]", options)
	}
	if def != string(domain.NodeRoleNode) {
		t.Fatalf("default role = %q, want node", def)
	}
}

func TestCollectInstalledVersions(t *testing.T) {
	origLookPath := lookPath
	origRun := runCommandOutput
	origVersion := Version
	t.Cleanup(func() {
		lookPath = origLookPath
		runCommandOutput = origRun
		Version = origVersion
	})

	Version = "v0.9.0"
	lookPath = func(file string) (string, error) {
		switch file {
		case "sing-box", "xray", "nginx", "systemctl":
			return "/usr/bin/" + file, nil
		default:
			return "", fmt.Errorf("not found")
		}
	}
	runCommandOutput = func(ctx context.Context, name string, args ...string) (string, error) {
		switch name {
		case "sing-box":
			return "sing-box version 1.13.2\n", nil
		case "xray":
			return "Xray 26.2.6 (Xray, Penetrates Everything.)\n", nil
		case "nginx":
			return "nginx version: nginx/1.22.1\n", nil
		case "systemctl":
			return "systemd 252 (252.30-1~deb12u2)\n", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", name)
		}
	}

	got := collectInstalledVersions(context.Background())
	gotMap := make(map[string]string, len(got))
	for _, item := range got {
		gotMap[item.Name] = item.Version
	}

	if gotMap["proxyctl"] != "v0.9.0" {
		t.Fatalf("proxyctl version = %q, want v0.9.0", gotMap["proxyctl"])
	}
	if gotMap["sing-box"] != "sing-box version 1.13.2" {
		t.Fatalf("sing-box version = %q", gotMap["sing-box"])
	}
	if gotMap["xray"] != "Xray 26.2.6 (Xray, Penetrates Everything.)" {
		t.Fatalf("xray version = %q", gotMap["xray"])
	}
	if gotMap["caddy"] != "not installed" {
		t.Fatalf("caddy version = %q, want not installed", gotMap["caddy"])
	}
	if gotMap["sqlite3"] != "not installed" {
		t.Fatalf("sqlite3 version = %q, want not installed", gotMap["sqlite3"])
	}
	if gotMap["nginx"] != "nginx version: nginx/1.22.1" {
		t.Fatalf("nginx version = %q", gotMap["nginx"])
	}
	if gotMap["systemd"] != "systemd 252 (252.30-1~deb12u2)" {
		t.Fatalf("systemd version = %q", gotMap["systemd"])
	}
}

func TestSetConfigDecoySiteDirCreatesPathsSection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "proxyctl.yaml")
	err := os.WriteFile(cfgPath, []byte("reverse_proxy: caddy\n"), 0o640)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := setConfigDecoySiteDir(cfgPath, "/srv/decoys/site-a"); err != nil {
		t.Fatalf("setConfigDecoySiteDir() error: %v", err)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "decoy_site_dir: /srv/decoys/site-a") {
		t.Fatalf("config missing decoy_site_dir, got:\n%s", text)
	}
}

func TestSetConfigDecoySiteDirUpdatesExistingValue(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "proxyctl.yaml")
	err := os.WriteFile(cfgPath, []byte("paths:\n    decoy_site_dir: /old/path\n"), 0o640)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := setConfigDecoySiteDir(cfgPath, "/new/path"); err != nil {
		t.Fatalf("setConfigDecoySiteDir() error: %v", err)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	if strings.Contains(text, "/old/path") {
		t.Fatalf("old path still present, got:\n%s", text)
	}
	if !strings.Contains(text, "decoy_site_dir: /new/path") {
		t.Fatalf("new path missing, got:\n%s", text)
	}
}

func TestResolveDecoyTemplateLibraryPath(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	got := resolveDecoyTemplateLibraryPath(cfg)
	if got != "/usr/share/proxy-orchestrator/decoy-templates" {
		t.Fatalf("resolveDecoyTemplateLibraryPath() = %q", got)
	}
}

func TestListDecoyTemplatesFiltersInvalid(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	valid := filepath.Join(root, "valid")
	if err := os.MkdirAll(filepath.Join(valid, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir valid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(valid, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write valid index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(valid, "assets", "style.css"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write valid style: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "invalid"), 0o755); err != nil {
		t.Fatalf("mkdir invalid: %v", err)
	}

	items, err := listDecoyTemplates(root)
	if err != nil {
		t.Fatalf("listDecoyTemplates() error: %v", err)
	}
	if len(items) != 1 || items[0] != "valid" {
		t.Fatalf("templates = %v, want [valid]", items)
	}
}

func TestFindCredentialByUserAndInbound(t *testing.T) {
	t.Parallel()

	credentials := []domain.Credential{
		{ID: "c1", UserID: "u1", InboundID: "i1"},
		{ID: "c2", UserID: "u2", InboundID: "i1"},
	}

	found, ok := findCredentialByUserAndInbound(credentials, "u1", "i1")
	if !ok {
		t.Fatalf("expected credential match, got none")
	}
	if found.ID != "c1" {
		t.Fatalf("credential id = %q, want c1", found.ID)
	}

	_, ok = findCredentialByUserAndInbound(credentials, "u1", "i2")
	if ok {
		t.Fatalf("expected no match for different inbound")
	}
}

func TestEnableRuntimeUnitsDeduplicates(t *testing.T) {
	origLookPath := lookPath
	origRun := runCommandOutput
	t.Cleanup(func() {
		lookPath = origLookPath
		runCommandOutput = origRun
	})

	lookPath = func(file string) (string, error) {
		if file == "systemctl" {
			return "/bin/systemctl", nil
		}
		return "", fmt.Errorf("not found")
	}

	calls := make([]string, 0)
	runCommandOutput = func(ctx context.Context, name string, args ...string) (string, error) {
		if name != "systemctl" {
			return "", fmt.Errorf("unexpected command: %s", name)
		}
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}

	enabled, err := enableRuntimeUnits(context.Background(), []applyruntime.ServiceOperation{
		{Unit: "proxyctl-xray.service"},
		{Unit: "proxyctl-xray.service"},
		{Unit: "proxyctl-sing-box.service"},
	})
	if err != nil {
		t.Fatalf("enableRuntimeUnits() error: %v", err)
	}
	if len(enabled) != 2 {
		t.Fatalf("enabled units = %v, want 2 unique units", enabled)
	}
	if len(calls) != 2 {
		t.Fatalf("systemctl enable calls = %v, want 2", calls)
	}
}
