package config

import "fmt"

// ReverseProxyEngine defines which reverse proxy backend is selected.
type ReverseProxyEngine string

const (
	ReverseProxyCaddy ReverseProxyEngine = "caddy"
	ReverseProxyNginx ReverseProxyEngine = "nginx"
)

// Paths stores top-level filesystem paths used by the application.
type Paths struct {
	BaseDir      string
	ConfigDir    string
	BinDir       string
	StateDir     string
	RuntimeDir   string
	CaddyDir     string
	NginxDir     string
	DecoySiteDir string
	RevisionsDir string
	ActiveLink   string
	StagingDir   string
	LogsDir      string
	ConfigFile   string
	BackupsDir   string
	TemplatesDir string
	ExamplesDir  string
	SystemdUnits string
	Subscription string
}

// RuntimeConfig stores systemd unit names managed by proxyctl.
type RuntimeConfig struct {
	SingBoxUnit string
	XrayUnit    string
	CaddyUnit   string
	NginxUnit   string
}

// PublicEndpointConfig describes default public endpoint settings.
type PublicEndpointConfig struct {
	Domain       string
	HTTPS        bool
	ContactEmail string
}

// StorageConfig stores persistence settings for the current environment.
type StorageConfig struct {
	SQLitePath string
}

// AppConfig is the root configuration model for proxyctl.
type AppConfig struct {
	Paths        Paths
	Storage      StorageConfig
	Runtime      RuntimeConfig
	ReverseProxy ReverseProxyEngine
	Public       PublicEndpointConfig
}

const (
	DefaultConfigFile = "/etc/proxy-orchestrator/proxyctl.yaml"
)

// DefaultAppConfig returns the MVP default layout from ARCHITECTURE.md.
func DefaultAppConfig() AppConfig {
	return AppConfig{
		Paths: Paths{
			BaseDir:      "/etc/proxy-orchestrator",
			ConfigDir:    "/etc/proxy-orchestrator",
			BinDir:       "/usr/local/bin",
			StateDir:     "/var/lib/proxy-orchestrator",
			RuntimeDir:   "/etc/proxy-orchestrator/runtime",
			CaddyDir:     "/etc/proxy-orchestrator/runtime/caddy",
			NginxDir:     "/etc/proxy-orchestrator/runtime/nginx",
			DecoySiteDir: "/etc/proxy-orchestrator/runtime/decoy-site",
			RevisionsDir: "/var/lib/proxy-orchestrator/revisions",
			ActiveLink:   "/var/lib/proxy-orchestrator/active",
			StagingDir:   "/var/lib/proxy-orchestrator/staging",
			LogsDir:      "/var/lib/proxy-orchestrator/logs",
			ConfigFile:   DefaultConfigFile,
			BackupsDir:   "/var/backups/proxy-orchestrator",
			TemplatesDir: "/usr/share/proxy-orchestrator/templates",
			ExamplesDir:  "/usr/share/proxy-orchestrator/examples",
			SystemdUnits: "/etc/systemd/system",
			Subscription: "/var/lib/proxy-orchestrator/subscriptions",
		},
		Storage: StorageConfig{
			SQLitePath: "/var/lib/proxy-orchestrator/proxyctl.db",
		},
		Runtime: RuntimeConfig{
			SingBoxUnit: "proxyctl-sing-box.service",
			XrayUnit:    "proxyctl-xray.service",
			CaddyUnit:   "proxyctl-caddy.service",
			NginxUnit:   "proxyctl-nginx.service",
		},
		ReverseProxy: ReverseProxyCaddy,
		Public: PublicEndpointConfig{
			HTTPS: true,
		},
	}
}

// Validate performs minimal semantic checks for static configuration.
func (c AppConfig) Validate() error {
	if c.ReverseProxy != ReverseProxyCaddy && c.ReverseProxy != ReverseProxyNginx {
		return fmt.Errorf("unsupported reverse proxy engine %q", c.ReverseProxy)
	}
	if c.Storage.SQLitePath == "" {
		return fmt.Errorf("storage.sqlite_path is required")
	}
	return nil
}
