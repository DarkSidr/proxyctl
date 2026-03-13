package nginx

import (
	"path/filepath"
	"strings"
	"testing"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
)

func TestBuildGeneratesNginxConfigAndRoutes(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	cfg.Paths.TemplatesDir = filepath.Join("..", "..", "..", "templates")
	cfg.Paths.DecoySiteDir = "/etc/proxy-orchestrator/runtime/decoy-site"
	cfg.Public.Domain = "public.example.com"

	builder := New(cfg)
	result, err := builder.Build(BuildRequest{
		Node: domain.Node{Host: "node.internal"},
		Inbounds: []domain.Inbound{
			{ID: "in-1", Enabled: true, Transport: "ws", Port: 11080, Path: "/ws"},
			{ID: "in-2", Enabled: true, Transport: "grpc", Port: 12080, Path: "grpc-api"},
			{ID: "in-3", Enabled: true, Transport: "xhttp", Port: 13080, Path: "/xhttp"},
			{ID: "skip", Enabled: true, Transport: "tcp", Port: 14080, Path: "/tcp"},
		},
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(result.Routes) != 3 {
		t.Fatalf("routes count = %d, want 3", len(result.Routes))
	}
	if len(result.Domains) != 1 || result.Domains[0] != "public.example.com" {
		t.Fatalf("domains = %v, want [public.example.com]", result.Domains)
	}

	body := string(result.NginxConfig)
	assertContains(t, body, "server_name public.example.com;")
	assertContains(t, body, "root /etc/proxy-orchestrator/runtime/decoy-site;")
	assertContains(t, body, "location = /grpc-api {")
	assertContains(t, body, "grpc_pass grpc://127.0.0.1:12080;")
	assertContains(t, body, "location = /ws {")
	assertContains(t, body, "proxy_pass http://127.0.0.1:11080;")
	assertContains(t, body, "location = /xhttp {")
	assertContains(t, body, "proxy_pass http://127.0.0.1:13080;")
}

func TestLoadDecoyAssets(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	cfg.Paths.TemplatesDir = filepath.Join("..", "..", "..", "templates")

	assets, err := LoadDecoyAssets(cfg)
	if err != nil {
		t.Fatalf("LoadDecoyAssets() error: %v", err)
	}
	if len(assets) == 0 {
		t.Fatalf("expected non-empty decoy assets")
	}

	seen := map[string]bool{}
	for _, asset := range assets {
		seen[asset.RelativePath] = true
	}
	if !seen["index.html"] {
		t.Fatalf("index.html asset is missing")
	}
	if !seen["assets/style.css"] {
		t.Fatalf("assets/style.css asset is missing")
	}
}

func TestBuildFailsWithoutProxyableInbounds(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	cfg.Paths.TemplatesDir = filepath.Join("..", "..", "..", "templates")

	builder := New(cfg)
	_, err := builder.Build(BuildRequest{
		Node:     domain.Node{Host: "node.internal"},
		Inbounds: []domain.Inbound{{ID: "in-1", Enabled: true, Transport: "udp", Port: 10000}},
	})
	if err == nil {
		t.Fatalf("expected Build() error")
	}
}

func assertContains(t *testing.T, text, needle string) {
	t.Helper()
	if !strings.Contains(text, needle) {
		t.Fatalf("text %q does not contain %q", text, needle)
	}
}
