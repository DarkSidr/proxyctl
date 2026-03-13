package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"proxyctl/internal/domain"
	applyruntime "proxyctl/internal/runtime/apply"
)

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

	dbPath := "/tmp/proxyctl-test.db"
	cmd := newInboundAddCmd(&dbPath)
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
	if def != "inbounds" {
		t.Fatalf("default action = %q, want inbounds", def)
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
