package caddy

import (
	"path/filepath"
	"strings"
	"testing"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
)

func TestBuildGeneratesCaddyfileAndRoutes(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	cfg.Paths.TemplatesDir = filepath.Join("..", "..", "..", "templates")
	cfg.Paths.DecoySiteDir = "/etc/proxy-orchestrator/runtime/decoy-site"
	cfg.Public.HTTPS = true

	builder := New(cfg)
	result, err := builder.Build(BuildRequest{
		// Node.Host is the per-node domain; inbound domains take priority when set.
		Node: domain.Node{Host: "public.example.com"},
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

	body := string(result.Caddyfile)
	assertContains(t, body, "public.example.com {")
	assertContains(t, body, "root * /etc/proxy-orchestrator/runtime/decoy-site")
	assertContains(t, body, "handle_path /sub/* {")
	assertContains(t, body, "root * /var/lib/proxy-orchestrator/subscriptions/public")
	assertContains(t, body, "try_files {path} {path}.txt =404")
	assertContains(t, body, "reverse_proxy @route_1 127.0.0.1:12080")
	assertContains(t, body, "versions h2c 2")
	assertContains(t, body, "reverse_proxy @route_2 127.0.0.1:11080")
	assertContains(t, body, "reverse_proxy @route_3 127.0.0.1:13080")
}

func TestBuildInboundDomainTakesPriorityOverNodeHost(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	cfg.Paths.TemplatesDir = filepath.Join("..", "..", "..", "templates")
	cfg.Public.HTTPS = true

	builder := New(cfg)
	result, err := builder.Build(BuildRequest{
		Node: domain.Node{Host: "node.internal"},
		Inbounds: []domain.Inbound{
			{ID: "in-1", Enabled: true, Transport: "ws", Port: 11080, Path: "/ws", Domain: "inbound.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(result.Domains) != 1 || result.Domains[0] != "inbound.example.com" {
		t.Fatalf("domains = %v, want [inbound.example.com]", result.Domains)
	}
	assertContains(t, string(result.Caddyfile), "inbound.example.com {")
}

func TestBuildSupportsHTTPAddress(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	cfg.Paths.TemplatesDir = filepath.Join("..", "..", "..", "templates")
	cfg.Paths.DecoySiteDir = "/tmp/decoy"
	cfg.Public.HTTPS = false

	builder := New(cfg)
	result, err := builder.Build(BuildRequest{
		Node:     domain.Node{Host: "plain.example.com"},
		Inbounds: []domain.Inbound{{ID: "in-1", Enabled: true, Transport: "ws", Port: 11080, Path: "/ws"}},
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	assertContains(t, string(result.Caddyfile), "http://plain.example.com {")
}

func TestBuildUsesFallbackTemplateWhenTemplateFilesMissing(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	cfg.Paths.TemplatesDir = filepath.Join(t.TempDir(), "missing-templates")
	cfg.Paths.DecoySiteDir = "/etc/proxy-orchestrator/runtime/decoy-site"
	cfg.Public.HTTPS = true

	builder := New(cfg)
	result, err := builder.Build(BuildRequest{
		Node:     domain.Node{Host: "fallback.example.com"},
		Inbounds: []domain.Inbound{{ID: "in-1", Enabled: true, Transport: "ws", Port: 11080, Path: "/ws"}},
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	body := string(result.Caddyfile)
	assertContains(t, body, "fallback.example.com {")
	assertContains(t, body, "handle_path /sub/* {")
	assertContains(t, body, "root * /var/lib/proxy-orchestrator/subscriptions/public")
}

func TestBuildIncludesSelfStealListenerWithTemplateFile(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultAppConfig()
	cfg.Paths.TemplatesDir = filepath.Join("..", "..", "..", "templates")
	cfg.Paths.DecoySiteDir = "/etc/proxy-orchestrator/runtime/decoy-site"
	cfg.Public.HTTPS = true

	builder := New(cfg)
	result, err := builder.Build(BuildRequest{
		Node:          domain.Node{Host: "fi.example.com"},
		SelfStealPort: 8443,
		Inbounds: []domain.Inbound{
			{
				ID:             "in-1",
				Enabled:        true,
				Type:           domain.ProtocolVLESS,
				Transport:      "tcp",
				Domain:         "fi.example.com",
				Port:           8443,
				RealityEnabled: true,
				SelfSteal:      true,
			},
			{
				ID:         "in-2",
				Enabled:    true,
				Transport:  "xhttp",
				Domain:     "fi.example.com",
				Port:       9443,
				TLSEnabled: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	body := string(result.Caddyfile)
	assertContains(t, body, "http://127.0.0.1:8443 {")
	assertNotContains(t, body, "fi.example.com:8443")
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

func assertNotContains(t *testing.T, text, needle string) {
	t.Helper()
	if strings.Contains(text, needle) {
		t.Fatalf("text %q should not contain %q", text, needle)
	}
}
