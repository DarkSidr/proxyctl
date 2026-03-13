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
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown command %q for %q\nSee '%s --help'", args[0], cmd.CommandPath(), cmd.CommandPath())
			}
			if !stdinIsTerminal(cmd.InOrStdin()) {
				return cmd.Help()
			}
			return runProxyctlSubcommand(cmd, "wizard", "--config", configPath, "--db", dbPath)
		},
	}

	rootCmd.PersistentFlags().StringVar(&configPath, "config", config.DefaultConfigFile, "Path to proxyctl configuration file")
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", defaults.Storage.SQLitePath, "Path to SQLite database file")
	rootCmd.SetVersionTemplate("{{printf \"proxyctl version %s\\n\" .Version}}")
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w\nSee '%s --help'", err, cmd.CommandPath())
	})

	rootCmd.AddCommand(
		newInitCmd(&dbPath),
		newWizardCmd(&dbPath),
		newUpdateCmd(),
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
