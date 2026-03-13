package layout

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureDirectoriesCreatesAll(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	m := New(Directories{
		ConfigDir:        filepath.Join(root, "etc", "proxy-orchestrator"),
		RuntimeDir:       filepath.Join(root, "etc", "proxy-orchestrator", "runtime"),
		CaddyDir:         filepath.Join(root, "etc", "proxy-orchestrator", "runtime", "caddy"),
		NginxDir:         filepath.Join(root, "etc", "proxy-orchestrator", "runtime", "nginx"),
		DecoySiteDir:     filepath.Join(root, "etc", "proxy-orchestrator", "runtime", "decoy-site"),
		StateDir:         filepath.Join(root, "var", "lib", "proxy-orchestrator"),
		SubscriptionsDir: filepath.Join(root, "var", "lib", "proxy-orchestrator", "subscriptions"),
		BackupsDir:       filepath.Join(root, "var", "backups", "proxy-orchestrator"),
	})

	if err := m.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error: %v", err)
	}

	for _, dir := range []string{m.dirs.ConfigDir, m.dirs.RuntimeDir, m.dirs.CaddyDir, m.dirs.NginxDir, m.dirs.DecoySiteDir, m.dirs.StateDir, m.dirs.SubscriptionsDir, m.dirs.BackupsDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("os.Stat(%q) error: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", dir)
		}
	}
}

func TestWriteRenderedCaddyConfigCreatesBackup(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	m := New(Directories{
		RuntimeDir: filepath.Join(root, "runtime"),
		CaddyDir:   filepath.Join(root, "runtime", "caddy"),
		BackupsDir: filepath.Join(root, "backups"),
	})
	m.now = func() time.Time {
		return time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
	}

	target := filepath.Join(m.dirs.CaddyDir, caddyConfigName)
	if err := os.MkdirAll(m.dirs.CaddyDir, 0o755); err != nil {
		t.Fatalf("mkdir caddy dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("old-caddy"), 0o640); err != nil {
		t.Fatalf("seed old caddy config: %v", err)
	}

	result, err := m.WriteRenderedCaddyConfig([]byte("new-caddy"))
	if err != nil {
		t.Fatalf("WriteRenderedCaddyConfig() error: %v", err)
	}
	if result.Path != target {
		t.Fatalf("path = %q, want %q", result.Path, target)
	}
	if result.BackupPath == "" {
		t.Fatalf("backup path should not be empty")
	}

	newContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(newContent) != "new-caddy" {
		t.Fatalf("target content = %q, want %q", string(newContent), "new-caddy")
	}
}

func TestWriteRenderedSingBoxConfigCreatesBackup(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	m := New(Directories{
		RuntimeDir: filepath.Join(root, "runtime"),
		BackupsDir: filepath.Join(root, "backups"),
	})
	m.now = func() time.Time {
		return time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
	}

	target := filepath.Join(m.dirs.RuntimeDir, singBoxConfigName)
	if err := os.MkdirAll(m.dirs.RuntimeDir, 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatalf("seed old config: %v", err)
	}

	result, err := m.WriteRenderedSingBoxConfig([]byte("new"))
	if err != nil {
		t.Fatalf("WriteRenderedSingBoxConfig() error: %v", err)
	}

	if result.Path != target {
		t.Fatalf("path = %q, want %q", result.Path, target)
	}
	if result.BackupPath == "" {
		t.Fatalf("backup path should not be empty")
	}

	backupContent, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupContent) != "old" {
		t.Fatalf("backup content = %q, want %q", string(backupContent), "old")
	}

	newContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(newContent) != "new" {
		t.Fatalf("target content = %q, want %q", string(newContent), "new")
	}
}

func TestWriteRenderedNginxConfigCreatesBackup(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	m := New(Directories{
		RuntimeDir: filepath.Join(root, "runtime"),
		NginxDir:   filepath.Join(root, "runtime", "nginx"),
		BackupsDir: filepath.Join(root, "backups"),
	})
	m.now = func() time.Time {
		return time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
	}

	target := filepath.Join(m.dirs.NginxDir, nginxConfigName)
	if err := os.MkdirAll(m.dirs.NginxDir, 0o755); err != nil {
		t.Fatalf("mkdir nginx dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("old-nginx"), 0o640); err != nil {
		t.Fatalf("seed old nginx config: %v", err)
	}

	result, err := m.WriteRenderedNginxConfig([]byte("new-nginx"))
	if err != nil {
		t.Fatalf("WriteRenderedNginxConfig() error: %v", err)
	}
	if result.Path != target {
		t.Fatalf("path = %q, want %q", result.Path, target)
	}
	if result.BackupPath == "" {
		t.Fatalf("backup path should not be empty")
	}

	newContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(newContent) != "new-nginx" {
		t.Fatalf("target content = %q, want %q", string(newContent), "new-nginx")
	}
}

func TestWriteSubscriptionFilesWithSuffix(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	m := New(Directories{SubscriptionsDir: filepath.Join(root, "subscriptions")})

	paths, err := m.WriteSubscriptionFilesWithSuffix("user-1", SubscriptionFiles{
		TXT:    []byte("a\n"),
		Base64: []byte("YQo="),
		JSON:   []byte("{}"),
	}, "preview")
	if err != nil {
		t.Fatalf("WriteSubscriptionFilesWithSuffix() error: %v", err)
	}

	if filepath.Base(paths.TXTPath) != "user-1.preview.txt" {
		t.Fatalf("unexpected txt path: %q", paths.TXTPath)
	}
	if filepath.Base(paths.Base64Path) != "user-1.preview.base64" {
		t.Fatalf("unexpected base64 path: %q", paths.Base64Path)
	}
	if filepath.Base(paths.JSONPath) != "user-1.preview.json" {
		t.Fatalf("unexpected json path: %q", paths.JSONPath)
	}

	for _, tc := range []struct {
		path string
		want string
	}{
		{path: paths.TXTPath, want: "a\n"},
		{path: paths.Base64Path, want: "YQo="},
		{path: paths.JSONPath, want: "{}"},
	} {
		got, readErr := os.ReadFile(tc.path)
		if readErr != nil {
			t.Fatalf("read %q: %v", tc.path, readErr)
		}
		if string(got) != tc.want {
			t.Fatalf("content(%q)=%q, want %q", tc.path, string(got), tc.want)
		}
	}
}

func TestWriteAtomicFileOverwritesExisting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "atomic.txt")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	if err := WriteAtomicFile(path, []byte("new"), 0o640); err != nil {
		t.Fatalf("WriteAtomicFile() error: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("content = %q, want %q", string(content), "new")
	}
}

func TestWriteDecoySiteAssets(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	m := New(Directories{DecoySiteDir: filepath.Join(root, "runtime", "decoy-site")})

	written, err := m.WriteDecoySiteAssets([]StaticAsset{
		{RelativePath: "index.html", Content: []byte("<html></html>")},
		{RelativePath: "assets/style.css", Content: []byte("body{}")},
	})
	if err != nil {
		t.Fatalf("WriteDecoySiteAssets() error: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("written count = %d, want 2", len(written))
	}

	for _, tc := range []struct {
		rel  string
		want string
	}{
		{rel: "index.html", want: "<html></html>"},
		{rel: "assets/style.css", want: "body{}"},
	} {
		full := filepath.Join(m.dirs.DecoySiteDir, tc.rel)
		got, readErr := os.ReadFile(full)
		if readErr != nil {
			t.Fatalf("read %q: %v", full, readErr)
		}
		if string(got) != tc.want {
			t.Fatalf("content(%q)=%q, want %q", full, string(got), tc.want)
		}
	}
}
