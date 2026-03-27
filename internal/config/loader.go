package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type appConfigYAML struct {
	DeploymentMode string              `yaml:"deployment_mode"`
	Paths          *pathsYAML          `yaml:"paths"`
	Storage        *storageYAML        `yaml:"storage"`
	Runtime        *runtimeYAML        `yaml:"runtime"`
	ReverseProxy   string              `yaml:"reverse_proxy"`
	Public         *publicEndpointYAML `yaml:"public"`
}

type pathsYAML struct {
	BaseDir      *string `yaml:"base_dir"`
	ConfigDir    *string `yaml:"config_dir"`
	BinDir       *string `yaml:"bin_dir"`
	StateDir     *string `yaml:"state_dir"`
	RuntimeDir   *string `yaml:"runtime_dir"`
	CaddyDir     *string `yaml:"caddy_dir"`
	NginxDir     *string `yaml:"nginx_dir"`
	DecoySiteDir *string `yaml:"decoy_site_dir"`
	RevisionsDir *string `yaml:"revisions_dir"`
	ActiveLink   *string `yaml:"active_link"`
	StagingDir   *string `yaml:"staging_dir"`
	LogsDir      *string `yaml:"logs_dir"`
	ConfigFile   *string `yaml:"config_file"`
	BackupsDir   *string `yaml:"backups_dir"`
	TemplatesDir *string `yaml:"templates_dir"`
	ExamplesDir  *string `yaml:"examples_dir"`
	SystemdUnits *string `yaml:"systemd_units"`
	Subscription *string `yaml:"subscription"`
}

type storageYAML struct {
	SQLitePath *string `yaml:"sqlite_path"`
}

type runtimeYAML struct {
	SingBoxUnit *string `yaml:"singbox_unit"`
	XrayUnit    *string `yaml:"xray_unit"`
	CaddyUnit   *string `yaml:"caddy_unit"`
	NginxUnit   *string `yaml:"nginx_unit"`
}

type publicEndpointYAML struct {
	Domain           *string `yaml:"domain"`
	HTTPS            *bool   `yaml:"https"`
	ContactEmail     *string `yaml:"contact_email"`
	DefaultSelfSteal *bool   `yaml:"default_self_steal"`
	SelfStealPort    *int    `yaml:"self_steal_port"`
}

// Load reads app config from YAML and overlays it on default values.
// When file does not exist, defaults are returned.
func Load(path string) (AppConfig, error) {
	cfg := DefaultAppConfig()
	path = strings.TrimSpace(path)
	if path == "" {
		return cfg, nil
	}

	rawData, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return AppConfig{}, fmt.Errorf("read app config %q: %w", path, err)
	}

	var raw appConfigYAML
	if err := yaml.Unmarshal(rawData, &raw); err != nil {
		return AppConfig{}, fmt.Errorf("parse app config %q: %w", path, err)
	}
	applyYAMLToConfig(&cfg, raw)
	cfg.Paths.ConfigFile = path

	if err := cfg.Validate(); err != nil {
		return AppConfig{}, err
	}
	return cfg, nil
}

func applyYAMLToConfig(cfg *AppConfig, raw appConfigYAML) {
	if strings.TrimSpace(raw.DeploymentMode) != "" {
		cfg.DeploymentMode = DeploymentMode(strings.ToLower(strings.TrimSpace(raw.DeploymentMode)))
	}

	if raw.Paths != nil {
		if raw.Paths.BaseDir != nil {
			cfg.Paths.BaseDir = *raw.Paths.BaseDir
		}
		if raw.Paths.ConfigDir != nil {
			cfg.Paths.ConfigDir = *raw.Paths.ConfigDir
		}
		if raw.Paths.BinDir != nil {
			cfg.Paths.BinDir = *raw.Paths.BinDir
		}
		if raw.Paths.StateDir != nil {
			cfg.Paths.StateDir = *raw.Paths.StateDir
		}
		if raw.Paths.RuntimeDir != nil {
			cfg.Paths.RuntimeDir = *raw.Paths.RuntimeDir
		}
		if raw.Paths.CaddyDir != nil {
			cfg.Paths.CaddyDir = *raw.Paths.CaddyDir
		}
		if raw.Paths.NginxDir != nil {
			cfg.Paths.NginxDir = *raw.Paths.NginxDir
		}
		if raw.Paths.DecoySiteDir != nil {
			cfg.Paths.DecoySiteDir = *raw.Paths.DecoySiteDir
		}
		if raw.Paths.RevisionsDir != nil {
			cfg.Paths.RevisionsDir = *raw.Paths.RevisionsDir
		}
		if raw.Paths.ActiveLink != nil {
			cfg.Paths.ActiveLink = *raw.Paths.ActiveLink
		}
		if raw.Paths.StagingDir != nil {
			cfg.Paths.StagingDir = *raw.Paths.StagingDir
		}
		if raw.Paths.LogsDir != nil {
			cfg.Paths.LogsDir = *raw.Paths.LogsDir
		}
		if raw.Paths.ConfigFile != nil {
			cfg.Paths.ConfigFile = *raw.Paths.ConfigFile
		}
		if raw.Paths.BackupsDir != nil {
			cfg.Paths.BackupsDir = *raw.Paths.BackupsDir
		}
		if raw.Paths.TemplatesDir != nil {
			cfg.Paths.TemplatesDir = *raw.Paths.TemplatesDir
		}
		if raw.Paths.ExamplesDir != nil {
			cfg.Paths.ExamplesDir = *raw.Paths.ExamplesDir
		}
		if raw.Paths.SystemdUnits != nil {
			cfg.Paths.SystemdUnits = *raw.Paths.SystemdUnits
		}
		if raw.Paths.Subscription != nil {
			cfg.Paths.Subscription = *raw.Paths.Subscription
		}

		if raw.Paths.RuntimeDir != nil {
			if raw.Paths.CaddyDir == nil {
				cfg.Paths.CaddyDir = filepath.Join(cfg.Paths.RuntimeDir, "caddy")
			}
			if raw.Paths.NginxDir == nil {
				cfg.Paths.NginxDir = filepath.Join(cfg.Paths.RuntimeDir, "nginx")
			}
			if raw.Paths.DecoySiteDir == nil {
				cfg.Paths.DecoySiteDir = filepath.Join(cfg.Paths.RuntimeDir, "decoy-site")
			}
		}
	}

	if raw.Storage != nil && raw.Storage.SQLitePath != nil {
		cfg.Storage.SQLitePath = *raw.Storage.SQLitePath
	}

	if raw.Runtime != nil {
		if raw.Runtime.SingBoxUnit != nil {
			cfg.Runtime.SingBoxUnit = *raw.Runtime.SingBoxUnit
		}
		if raw.Runtime.XrayUnit != nil {
			cfg.Runtime.XrayUnit = *raw.Runtime.XrayUnit
		}
		if raw.Runtime.CaddyUnit != nil {
			cfg.Runtime.CaddyUnit = *raw.Runtime.CaddyUnit
		}
		if raw.Runtime.NginxUnit != nil {
			cfg.Runtime.NginxUnit = *raw.Runtime.NginxUnit
		}
	}

	if raw.Public != nil {
		if raw.Public.Domain != nil {
			cfg.Public.Domain = *raw.Public.Domain
		}
		if raw.Public.HTTPS != nil {
			cfg.Public.HTTPS = *raw.Public.HTTPS
		}
		if raw.Public.ContactEmail != nil {
			cfg.Public.ContactEmail = *raw.Public.ContactEmail
		}
		if raw.Public.DefaultSelfSteal != nil {
			cfg.Public.DefaultSelfSteal = *raw.Public.DefaultSelfSteal
		}
		if raw.Public.SelfStealPort != nil && *raw.Public.SelfStealPort > 0 {
			cfg.Public.SelfStealPort = *raw.Public.SelfStealPort
		}
	}

	if strings.TrimSpace(raw.ReverseProxy) != "" {
		cfg.ReverseProxy = ReverseProxyEngine(strings.ToLower(strings.TrimSpace(raw.ReverseProxy)))
	}
}
