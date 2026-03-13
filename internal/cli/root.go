package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"proxyctl/internal/config"
)

// Version is injected at build time via -ldflags "-X proxyctl/internal/cli.Version=<value>".
var Version = "dev"

// NewRootCmd constructs the root CLI command for proxyctl.
func NewRootCmd() *cobra.Command {
	defaults := config.DefaultAppConfig()
	configPath := defaults.Paths.ConfigFile
	dbPath := defaults.Storage.SQLitePath

	rootCmd := &cobra.Command{
		Use:           "proxyctl",
		Short:         "Self-hosted VPS proxy orchestrator",
		Long:          "proxyctl is a CLI orchestrator for single-node VPS proxy runtimes (sing-box/Xray) with safe revision lifecycle.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}

	rootCmd.PersistentFlags().StringVar(&configPath, "config", config.DefaultConfigFile, "Path to proxyctl configuration file")
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", defaults.Storage.SQLitePath, "Path to SQLite database file")
	rootCmd.SetVersionTemplate("{{printf \"proxyctl version %s\\n\" .Version}}")
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w\nSee '%s --help'", err, cmd.CommandPath())
	})

	rootCmd.AddCommand(
		newInitCmd(&dbPath),
		newStatusCmd(&configPath, &dbPath),
		newUserCmd(&dbPath),
		newNodeCmd(&dbPath),
		newInboundCmd(&dbPath),
		newPreviewCmd(&configPath, &dbPath),
		newRenderCmd(&configPath, &dbPath),
		newValidateCmd(&configPath, &dbPath),
		newApplyCmd(&configPath, &dbPath),
		newSubscriptionCmd(&dbPath),
		newLogsCmd(&configPath),
		newDoctorCmd(&configPath, &dbPath),
	)

	return rootCmd
}
