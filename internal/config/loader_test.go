package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReturnsDefaultsWhenFileMissing(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	defaults := DefaultAppConfig()
	if cfg.ReverseProxy != defaults.ReverseProxy {
		t.Fatalf("reverse proxy = %q, want %q", cfg.ReverseProxy, defaults.ReverseProxy)
	}
}

func TestLoadOverridesReverseProxyAndRuntimeDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfgPath := filepath.Join(root, "proxyctl.yaml")
	content := []byte(`
reverse_proxy: nginx
paths:
  runtime_dir: /tmp/runtime-proxy
public:
  domain: example.com
  https: false
`)
	if err := os.WriteFile(cfgPath, content, 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ReverseProxy != ReverseProxyNginx {
		t.Fatalf("reverse proxy = %q, want %q", cfg.ReverseProxy, ReverseProxyNginx)
	}
	if cfg.Paths.RuntimeDir != "/tmp/runtime-proxy" {
		t.Fatalf("runtime dir = %q, want %q", cfg.Paths.RuntimeDir, "/tmp/runtime-proxy")
	}
	if cfg.Paths.CaddyDir != "/tmp/runtime-proxy/caddy" {
		t.Fatalf("caddy dir = %q, want %q", cfg.Paths.CaddyDir, "/tmp/runtime-proxy/caddy")
	}
	if cfg.Paths.NginxDir != "/tmp/runtime-proxy/nginx" {
		t.Fatalf("nginx dir = %q, want %q", cfg.Paths.NginxDir, "/tmp/runtime-proxy/nginx")
	}
	if cfg.Paths.DecoySiteDir != "/tmp/runtime-proxy/decoy-site" {
		t.Fatalf("decoy dir = %q, want %q", cfg.Paths.DecoySiteDir, "/tmp/runtime-proxy/decoy-site")
	}
	if cfg.Public.Domain != "example.com" {
		t.Fatalf("public domain = %q, want example.com", cfg.Public.Domain)
	}
	if cfg.Public.HTTPS {
		t.Fatalf("public https = true, want false")
	}
}

func TestLoadFailsOnUnsupportedReverseProxy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfgPath := filepath.Join(root, "proxyctl.yaml")
	if err := os.WriteFile(cfgPath, []byte("reverse_proxy: apache\n"), 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("expected error")
	}
}
