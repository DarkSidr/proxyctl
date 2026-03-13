package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultConfigDir        = "/etc/proxy-orchestrator"
	DefaultRuntimeDir       = "/etc/proxy-orchestrator/runtime"
	DefaultCaddyDir         = "/etc/proxy-orchestrator/runtime/caddy"
	DefaultNginxDir         = "/etc/proxy-orchestrator/runtime/nginx"
	DefaultDecoySiteDir     = "/etc/proxy-orchestrator/runtime/decoy-site"
	DefaultStateDir         = "/var/lib/proxy-orchestrator"
	DefaultSubscriptionsDir = "/var/lib/proxy-orchestrator/subscriptions"
	DefaultBackupsDir       = "/var/backups/proxy-orchestrator"

	singBoxConfigName = "sing-box.json"
	xrayConfigName    = "xray.json"
	caddyConfigName   = "Caddyfile"
	nginxConfigName   = "nginx.conf"
)

// Directories defines runtime layout folders.
type Directories struct {
	ConfigDir        string
	RuntimeDir       string
	CaddyDir         string
	NginxDir         string
	DecoySiteDir     string
	StateDir         string
	SubscriptionsDir string
	BackupsDir       string
}

// DefaultDirectories returns stage-7 runtime directories.
func DefaultDirectories() Directories {
	return Directories{
		ConfigDir:        DefaultConfigDir,
		RuntimeDir:       DefaultRuntimeDir,
		CaddyDir:         DefaultCaddyDir,
		NginxDir:         DefaultNginxDir,
		DecoySiteDir:     DefaultDecoySiteDir,
		StateDir:         DefaultStateDir,
		SubscriptionsDir: DefaultSubscriptionsDir,
		BackupsDir:       DefaultBackupsDir,
	}
}

// SubscriptionFiles is the subscription payload set to persist.
type SubscriptionFiles struct {
	TXT    []byte
	Base64 []byte
	JSON   []byte
}

// SubscriptionPaths contains filesystem locations for persisted subscription files.
type SubscriptionPaths struct {
	TXTPath    string
	Base64Path string
	JSONPath   string
}

// ConfigWriteResult describes config write output path and optional backup path.
type ConfigWriteResult struct {
	Path       string
	BackupPath string
}

// StaticAsset is one static file in runtime layout.
type StaticAsset struct {
	RelativePath string
	Content      []byte
}

// Manager writes generated files into runtime layout directories.
type Manager struct {
	dirs Directories
	now  func() time.Time
}

// New constructs a runtime layout manager.
func New(dirs Directories) *Manager {
	if dirs.ConfigDir == "" &&
		dirs.RuntimeDir == "" &&
		dirs.CaddyDir == "" &&
		dirs.NginxDir == "" &&
		dirs.DecoySiteDir == "" &&
		dirs.StateDir == "" &&
		dirs.SubscriptionsDir == "" &&
		dirs.BackupsDir == "" {
		dirs = DefaultDirectories()
	}
	if dirs.ConfigDir == "" {
		dirs.ConfigDir = DefaultConfigDir
	}
	if dirs.RuntimeDir == "" {
		dirs.RuntimeDir = DefaultRuntimeDir
	}
	if dirs.CaddyDir == "" {
		dirs.CaddyDir = filepath.Join(dirs.RuntimeDir, "caddy")
	}
	if dirs.NginxDir == "" {
		dirs.NginxDir = filepath.Join(dirs.RuntimeDir, "nginx")
	}
	if dirs.DecoySiteDir == "" {
		dirs.DecoySiteDir = filepath.Join(dirs.RuntimeDir, "decoy-site")
	}
	if dirs.StateDir == "" {
		dirs.StateDir = DefaultStateDir
	}
	if dirs.SubscriptionsDir == "" {
		dirs.SubscriptionsDir = DefaultSubscriptionsDir
	}
	if dirs.BackupsDir == "" {
		dirs.BackupsDir = DefaultBackupsDir
	}

	return &Manager{
		dirs: dirs,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

// Directories returns the active runtime directory set.
func (m *Manager) Directories() Directories {
	return m.dirs
}

// EnsureDirectories creates runtime layout folders.
func (m *Manager) EnsureDirectories() error {
	for _, dir := range []string{
		m.dirs.ConfigDir,
		m.dirs.RuntimeDir,
		m.dirs.CaddyDir,
		m.dirs.NginxDir,
		m.dirs.DecoySiteDir,
		m.dirs.StateDir,
		m.dirs.SubscriptionsDir,
		m.dirs.BackupsDir,
	} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}
	return nil
}

// BackupPreviousConfig copies an existing config file to backup dir.
func (m *Manager) BackupPreviousConfig(configPath string) (string, error) {
	if strings.TrimSpace(configPath) == "" {
		return "", fmt.Errorf("config path is required")
	}
	if strings.TrimSpace(m.dirs.BackupsDir) == "" {
		return "", fmt.Errorf("backups directory is required")
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read existing config %q: %w", configPath, err)
	}

	if err := os.MkdirAll(m.dirs.BackupsDir, 0o755); err != nil {
		return "", fmt.Errorf("create backups directory %q: %w", m.dirs.BackupsDir, err)
	}

	ts := m.now().UTC().Format("20060102T150405Z")
	backupPath := filepath.Join(m.dirs.BackupsDir, fmt.Sprintf("%s.%s.bak", filepath.Base(configPath), ts))
	if err := WriteAtomicFile(backupPath, content, 0o640); err != nil {
		return "", fmt.Errorf("write backup file %q: %w", backupPath, err)
	}
	return backupPath, nil
}

// WriteRenderedSingBoxConfig writes rendered sing-box config to runtime.
func (m *Manager) WriteRenderedSingBoxConfig(content []byte) (ConfigWriteResult, error) {
	return m.writeRenderedConfig(filepath.Join(m.dirs.RuntimeDir, singBoxConfigName), content)
}

// WriteRenderedXrayConfig writes rendered xray config to runtime.
func (m *Manager) WriteRenderedXrayConfig(content []byte) (ConfigWriteResult, error) {
	return m.writeRenderedConfig(filepath.Join(m.dirs.RuntimeDir, xrayConfigName), content)
}

// WriteRenderedCaddyConfig writes rendered caddy config to runtime.
func (m *Manager) WriteRenderedCaddyConfig(content []byte) (ConfigWriteResult, error) {
	return m.writeRenderedConfig(filepath.Join(m.dirs.CaddyDir, caddyConfigName), content)
}

// WriteRenderedCaddyPreview writes rendered caddy preview config without backup.
func (m *Manager) WriteRenderedCaddyPreview(content []byte) (string, error) {
	target := filepath.Join(m.dirs.CaddyDir, caddyConfigName+".preview")
	if err := WriteAtomicFile(target, content, 0o640); err != nil {
		return "", fmt.Errorf("write caddy preview file %q: %w", target, err)
	}
	return target, nil
}

// WriteRenderedNginxConfig writes rendered nginx config to runtime.
func (m *Manager) WriteRenderedNginxConfig(content []byte) (ConfigWriteResult, error) {
	return m.writeRenderedConfig(filepath.Join(m.dirs.NginxDir, nginxConfigName), content)
}

// WriteRenderedNginxPreview writes rendered nginx preview config without backup.
func (m *Manager) WriteRenderedNginxPreview(content []byte) (string, error) {
	target := filepath.Join(m.dirs.NginxDir, nginxConfigName+".preview")
	if err := WriteAtomicFile(target, content, 0o640); err != nil {
		return "", fmt.Errorf("write nginx preview file %q: %w", target, err)
	}
	return target, nil
}

func (m *Manager) writeRenderedConfig(target string, content []byte) (ConfigWriteResult, error) {
	if strings.TrimSpace(target) == "" {
		return ConfigWriteResult{}, fmt.Errorf("target path is required")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return ConfigWriteResult{}, fmt.Errorf("create runtime directory: %w", err)
	}

	backupPath, err := m.BackupPreviousConfig(target)
	if err != nil {
		return ConfigWriteResult{}, err
	}
	if err := WriteAtomicFile(target, content, 0o640); err != nil {
		return ConfigWriteResult{}, fmt.Errorf("write config file %q: %w", target, err)
	}
	return ConfigWriteResult{Path: target, BackupPath: backupPath}, nil
}

// WriteSubscriptionFiles writes txt/base64/json payloads for one user.
func (m *Manager) WriteSubscriptionFiles(userID string, files SubscriptionFiles) (SubscriptionPaths, error) {
	return m.WriteSubscriptionFilesWithSuffix(userID, files, "")
}

// WriteSubscriptionFilesWithSuffix writes subscription payloads with optional suffix before extension.
func (m *Manager) WriteSubscriptionFilesWithSuffix(userID string, files SubscriptionFiles, suffix string) (SubscriptionPaths, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return SubscriptionPaths{}, fmt.Errorf("user id is required")
	}
	if strings.TrimSpace(m.dirs.SubscriptionsDir) == "" {
		return SubscriptionPaths{}, fmt.Errorf("subscriptions directory is required")
	}

	if err := os.MkdirAll(m.dirs.SubscriptionsDir, 0o755); err != nil {
		return SubscriptionPaths{}, fmt.Errorf("create subscriptions directory: %w", err)
	}

	suffix = strings.TrimSpace(suffix)
	if suffix != "" {
		suffix = "." + strings.TrimPrefix(suffix, ".")
	}

	paths := SubscriptionPaths{
		TXTPath:    filepath.Join(m.dirs.SubscriptionsDir, userID+suffix+".txt"),
		Base64Path: filepath.Join(m.dirs.SubscriptionsDir, userID+suffix+".base64"),
		JSONPath:   filepath.Join(m.dirs.SubscriptionsDir, userID+suffix+".json"),
	}
	if err := WriteAtomicFile(paths.TXTPath, files.TXT, 0o640); err != nil {
		return SubscriptionPaths{}, fmt.Errorf("write txt subscription: %w", err)
	}
	if err := WriteAtomicFile(paths.Base64Path, files.Base64, 0o640); err != nil {
		return SubscriptionPaths{}, fmt.Errorf("write base64 subscription: %w", err)
	}
	if err := WriteAtomicFile(paths.JSONPath, files.JSON, 0o640); err != nil {
		return SubscriptionPaths{}, fmt.Errorf("write json subscription: %w", err)
	}
	return paths, nil
}

// WriteDecoySiteAssets writes static decoy assets to runtime decoy site directory.
func (m *Manager) WriteDecoySiteAssets(assets []StaticAsset) ([]string, error) {
	if strings.TrimSpace(m.dirs.DecoySiteDir) == "" {
		return nil, fmt.Errorf("decoy site directory is required")
	}
	if err := os.MkdirAll(m.dirs.DecoySiteDir, 0o755); err != nil {
		return nil, fmt.Errorf("create decoy site directory: %w", err)
	}

	written := make([]string, 0, len(assets))
	for _, asset := range assets {
		rel := strings.TrimSpace(asset.RelativePath)
		if rel == "" {
			return nil, fmt.Errorf("decoy asset path is required")
		}
		cleanRel := filepath.Clean(rel)
		if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanRel) {
			return nil, fmt.Errorf("invalid decoy asset path %q", rel)
		}
		target := filepath.Join(m.dirs.DecoySiteDir, cleanRel)
		if err := WriteAtomicFile(target, asset.Content, 0o644); err != nil {
			return nil, fmt.Errorf("write decoy asset %q: %w", target, err)
		}
		written = append(written, target)
	}
	sort.Strings(written)
	return written, nil
}

// WriteAtomicFile writes content through temp file + rename in the same directory.
func WriteAtomicFile(path string, content []byte, perm os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is required")
	}
	if perm == 0 {
		perm = 0o640
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create target directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	cleanup = false

	dirFD, err := os.Open(dir)
	if err == nil {
		_ = dirFD.Sync()
		_ = dirFD.Close()
	}

	return nil
}
