package cli

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
	"proxyctl/internal/engine"
	"proxyctl/internal/renderer"
	"proxyctl/internal/renderer/singbox"
	"proxyctl/internal/renderer/xray"
	caddyproxy "proxyctl/internal/reverseproxy/caddy"
	nginxproxy "proxyctl/internal/reverseproxy/nginx"
	applyruntime "proxyctl/internal/runtime/apply"
	"proxyctl/internal/runtime/layout"
	"proxyctl/internal/runtime/systemd"
	"proxyctl/internal/storage/sqlite"
	subscriptionservice "proxyctl/internal/subscription/service"
)

const defaultUpdateInstallURL = "https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh"
const defaultLatestReleaseAPIURL = "https://api.github.com/repos/DarkSidr/proxyctl/releases/latest"
const defaultUninstallScriptPath = "/usr/local/sbin/proxyctl-uninstall"

var lookPath = exec.LookPath
var runCommandOutput = func(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func newGroupCmd(use, short, long string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
	}
}

func newStubLeafCmd(use, short, long string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("command %q is not implemented in MVP", cmd.CommandPath())
		},
	}
}

func newInitCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize local proxyctl workspace",
		Long:  "Creates/initializes SQLite storage schema for proxyctl MVP.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			store, err := sqlite.Open(*dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			if err := store.Init(ctx); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "initialized database: %s\n", *dbPath)
			return nil
		},
	}
}

func newWizardCmd(configPath, dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "wizard",
		Short: "Run interactive setup wizard",
		Long:  "Starts an interactive wizard for common proxyctl flows (user management and self-update).",
		RunE: func(cmd *cobra.Command, args []string) error {
			in := bufio.NewReader(cmd.InOrStdin())
			out := cmd.OutOrStdout()

			fmt.Fprintln(out, "proxyctl wizard")
			fmt.Fprintf(out, "=== VERSION: %s ===\n", strings.TrimSpace(Version))
			for {
				appCfg, cfgErr := loadAppConfig(*configPath)
				if cfgErr != nil {
					return cfgErr
				}
				fmt.Fprintf(out, "deployment mode: %s\n", appCfg.DeploymentMode)

				hasNodes, err := wizardHasNodes(cmd, *dbPath)
				if err != nil {
					return err
				}
				options, defaultAction := wizardMainOptionsByMode(hasNodes, appCfg.DeploymentMode)
				action, err := promptChoice(in, out, "Action", options, defaultAction)
				if err != nil {
					if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
						fmt.Fprintln(out, "wizard cancelled")
						return nil
					}
					return err
				}

				switch action {
				case "nodes":
					if err := runWizardNodesMenu(cmd, *dbPath); err != nil {
						return err
					}
				case "inbounds":
					if err := runWizardInboundsMenu(cmd, *configPath, *dbPath); err != nil {
						return err
					}
				case "users":
					if err := runWizardUsersMenu(cmd, *configPath, *dbPath); err != nil {
						return err
					}
				case "settings":
					if err := runWizardSettingsMenu(cmd, *configPath); err != nil {
						return err
					}
				case "update proxyctl":
					if err := runProxyctlSubcommand(cmd, "update"); err != nil {
						return err
					}
					fmt.Fprintln(out, "relaunching wizard from updated binary")
					return runProxyctlSubcommand(cmd, "wizard", "--config", *configPath, "--db", *dbPath)
				case "uninstall proxyctl":
					if err := runWizardUninstall(cmd, in, out); err != nil {
						return err
					}
				default:
					fmt.Fprintln(out, "wizard finished")
					return nil
				}
			}
		},
	}
}

func newUpdateCmd() *cobra.Command {
	installURL := defaultUpdateInstallURL
	latestReleaseAPIURL := defaultLatestReleaseAPIURL
	channel := "auto"
	reinstallBinary := true
	force := false
	ensureCaddy := true

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update proxyctl from repository",
		Long:  "Checks upstream release version and updates proxyctl only when a newer version exists.",
		RunE: func(cmd *cobra.Command, args []string) error {
			installURL = strings.TrimSpace(installURL)
			latestReleaseAPIURL = strings.TrimSpace(latestReleaseAPIURL)
			channel = strings.TrimSpace(channel)
			if installURL == "" {
				return fmt.Errorf("--install-url is required")
			}
			if latestReleaseAPIURL == "" {
				return fmt.Errorf("--latest-release-api-url is required")
			}
			if channel == "" {
				channel = "auto"
			}

			latestTag, err := fetchLatestReleaseTag(cmd.Context(), latestReleaseAPIURL)
			if err != nil {
				return err
			}

			current, currentParseErr := parseSemVersion(strings.TrimSpace(Version))
			if currentParseErr != nil && !force {
				return fmt.Errorf("current binary version %q is not a semantic release tag; use --force to bypass check", Version)
			}

			latest, latestParseErr := parseSemVersion(latestTag)
			if latestParseErr != nil {
				return fmt.Errorf("parse latest release tag %q: %w", latestTag, latestParseErr)
			}

			if currentParseErr == nil && !force {
				if compareSemVersion(latest, current) <= 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "proxyctl is up to date: current=%s latest=%s\n", Version, latestTag)
					return nil
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "updating proxyctl: current=%s latest=%s\n", Version, latestTag)
			updateExpr := fmt.Sprintf(
				"curl -fsSL %s | PROXYCTL_INSTALL_CHANNEL=%s PROXYCTL_REINSTALL_BINARY=%s bash",
				shellQuote(installURL),
				shellQuote(channel),
				shellQuote(boolToEnv(reinstallBinary)),
			)

			updateCmd := exec.CommandContext(cmd.Context(), "bash", "-lc", updateExpr)
			updateCmd.Stdout = cmd.OutOrStdout()
			updateCmd.Stderr = cmd.ErrOrStderr()
			updateCmd.Stdin = cmd.InOrStdin()

			if err := updateCmd.Run(); err != nil {
				return fmt.Errorf("self-update failed: %w", err)
			}
			if ensureCaddy {
				if err := ensureCaddyServiceHealthy(cmd.Context(), cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "proxyctl update completed")
			return nil
		},
	}

	cmd.Flags().StringVar(&installURL, "install-url", installURL, "Installer URL (defaults to upstream install.sh)")
	cmd.Flags().StringVar(&latestReleaseAPIURL, "latest-release-api-url", latestReleaseAPIURL, "GitHub API URL for latest release metadata")
	cmd.Flags().StringVar(&channel, "channel", channel, "Install channel passed to installer (auto|release|source|url|local)")
	cmd.Flags().BoolVar(&reinstallBinary, "reinstall-binary", reinstallBinary, "Force proxyctl binary reinstall")
	cmd.Flags().BoolVar(&force, "force", force, "Bypass version comparison and force update")
	cmd.Flags().BoolVar(&ensureCaddy, "ensure-caddy", ensureCaddy, "Check proxyctl-caddy.service state after update and auto-start it when inactive")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	yes := false
	removeRuntimePackages := false
	scriptPath := defaultUninstallScriptPath

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Completely remove proxyctl from host",
		Long:  "Runs uninstall script that removes proxyctl services, binaries, configs and state from the current host.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("uninstall requires root privileges")
			}
			scriptPath = strings.TrimSpace(scriptPath)
			if scriptPath == "" {
				return fmt.Errorf("--script-path is required")
			}
			if _, err := os.Stat(scriptPath); err != nil {
				return fmt.Errorf("uninstall script not found at %q", scriptPath)
			}
			if !yes {
				return fmt.Errorf("refusing to run uninstall without --yes")
			}

			uninstallArgs := []string{"--yes"}
			if removeRuntimePackages {
				uninstallArgs = append(uninstallArgs, "--remove-runtime-packages")
			}

			uninstallCmd := exec.CommandContext(cmd.Context(), scriptPath, uninstallArgs...)
			uninstallCmd.Stdout = cmd.OutOrStdout()
			uninstallCmd.Stderr = cmd.ErrOrStderr()
			uninstallCmd.Stdin = cmd.InOrStdin()
			if err := uninstallCmd.Run(); err != nil {
				return fmt.Errorf("run uninstall script: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm destructive uninstall")
	cmd.Flags().BoolVar(&removeRuntimePackages, "remove-runtime-packages", false, "Also purge caddy/nginx apt packages")
	cmd.Flags().StringVar(&scriptPath, "script-path", scriptPath, "Path to uninstall script")
	return cmd
}

func ensureCaddyServiceHealthy(ctx context.Context, out io.Writer) error {
	if _, err := lookPath("systemctl"); err != nil {
		fmt.Fprintln(out, "caddy service check skipped: systemctl not found")
		return nil
	}

	loadState, err := runCommandOutput(ctx, "systemctl", "show", "proxyctl-caddy.service", "--property=LoadState", "--value")
	if err != nil {
		return fmt.Errorf("check proxyctl-caddy.service load state: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(loadState), "not-found") {
		fmt.Fprintln(out, "caddy service check skipped: proxyctl-caddy.service is not installed")
		return nil
	}

	activeState, err := runCommandOutput(ctx, "systemctl", "is-active", "proxyctl-caddy.service")
	if err == nil && strings.EqualFold(strings.TrimSpace(activeState), "active") {
		fmt.Fprintln(out, "caddy service is active")
		return nil
	}

	fmt.Fprintln(out, "caddy service is inactive, enabling and starting...")
	if _, err := runCommandOutput(ctx, "systemctl", "enable", "--now", "proxyctl-caddy.service"); err != nil {
		return fmt.Errorf("enable/start proxyctl-caddy.service: %w", err)
	}

	activeState, err = runCommandOutput(ctx, "systemctl", "is-active", "proxyctl-caddy.service")
	if err != nil {
		return fmt.Errorf("verify proxyctl-caddy.service active state: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(activeState), "active") {
		return fmt.Errorf("proxyctl-caddy.service is %q after start attempt", strings.TrimSpace(activeState))
	}
	fmt.Fprintln(out, "caddy service started and active")
	return nil
}

func newUserCmd(dbPath *string) *cobra.Command {
	cmd := newGroupCmd(
		"user",
		"Manage users",
		"Provides user management operations for the control plane.",
	)
	cmd.AddCommand(
		newUserListCmd(dbPath),
		newUserAddCmd(dbPath),
		newStubLeafCmd("remove", "Remove a user", "Removes an existing user and related runtime credentials."),
	)
	return cmd
}

func newUserAddCmd(dbPath *string) *cobra.Command {
	var (
		name    string
		enabled bool
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a user",
		Long:  "Creates a new user for access management.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}

			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			created, err := store.Users().Create(cmd.Context(), domain.User{
				Name:    name,
				Enabled: enabled,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "added user: id=%s name=%s enabled=%t created_at=%s\n", created.ID, created.Name, created.Enabled, created.CreatedAt.Format(time.RFC3339))
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "User name")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Whether user is enabled")
	return cmd
}

func newUserListCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List users",
		Long:  "Lists configured users.",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			users, err := store.Users().List(cmd.Context())
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tENABLED\tCREATED_AT")
			for _, user := range users {
				fmt.Fprintf(w, "%s\t%s\t%t\t%s\n", user.ID, user.Name, user.Enabled, user.CreatedAt.Format(time.RFC3339))
			}
			return w.Flush()
		},
	}
}

func newNodeCmd(dbPath *string) *cobra.Command {
	cmd := newGroupCmd(
		"node",
		"Manage nodes",
		"Provides node management operations for the control plane.",
	)
	cmd.AddCommand(
		newNodeListCmd(dbPath),
		newNodeAddCmd(dbPath),
		newStubLeafCmd("show", "Show node details", "Displays detailed information for one node."),
	)
	return cmd
}

func newNodeAddCmd(dbPath *string) *cobra.Command {
	var (
		name    string
		host    string
		role    string
		enabled bool
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a node",
		Long:  "Creates a new managed node.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if host == "" {
				return fmt.Errorf("--host is required")
			}
			if role == "" {
				role = string(domain.NodeRolePrimary)
			}

			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			created, err := store.Nodes().Create(cmd.Context(), domain.Node{
				Name:    name,
				Host:    host,
				Role:    domain.NodeRole(role),
				Enabled: enabled,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "added node: id=%s name=%s host=%s role=%s enabled=%t created_at=%s\n", created.ID, created.Name, created.Host, created.Role, created.Enabled, created.CreatedAt.Format(time.RFC3339))
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Node name")
	cmd.Flags().StringVar(&host, "host", "", "Node host or IP")
	cmd.Flags().StringVar(&role, "role", string(domain.NodeRolePrimary), "Node role")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Whether node is enabled")
	return cmd
}

func newNodeListCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List nodes",
		Long:  "Lists known nodes and their runtime roles.",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			nodes, err := store.Nodes().List(cmd.Context())
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tHOST\tROLE\tENABLED\tCREATED_AT")
			for _, node := range nodes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%s\n", node.ID, node.Name, node.Host, node.Role, node.Enabled, node.CreatedAt.Format(time.RFC3339))
			}
			return w.Flush()
		},
	}
}

func newInboundCmd(dbPath *string) *cobra.Command {
	cmd := newGroupCmd(
		"inbound",
		"Manage inbound listeners",
		"Provides inbound listener configuration operations.",
	)
	cmd.AddCommand(
		newInboundListCmd(dbPath),
		newInboundAddCmd(dbPath),
		newStubLeafCmd("disable", "Disable inbound", "Disables one inbound profile without deleting it."),
	)
	return cmd
}

func newInboundAddCmd(dbPath *string) *cobra.Command {
	var (
		protocol           string
		transport          string
		engineRaw          string
		nodeID             string
		domainRaw          string
		port               int
		tls                bool
		tlsCertPath        string
		tlsKeyPath         string
		path               string
		sni                string
		reality            bool
		realityPublicKey   string
		realityPrivateKey  string
		realityShortID     string
		realityFingerprint string
		realitySpiderX     string
		realityServer      string
		realityServerPort  int
		vlessFlow          string
		enabled            bool
		linkUserID         string
		allowPort443       bool
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add inbound",
		Long:  "Creates a new inbound profile for one protocol/port.",
		RunE: func(cmd *cobra.Command, args []string) error {
			usedWizard := false
			if strings.TrimSpace(protocol) == "" {
				if !stdinIsTerminal(cmd.InOrStdin()) {
					return fmt.Errorf("--type is required")
				}
				prompted, err := promptInboundAddWizard(cmd, *dbPath, strings.TrimSpace(linkUserID))
				if err != nil {
					return err
				}
				usedWizard = true
				protocol = prompted.protocol
				transport = prompted.transport
				engineRaw = prompted.engineRaw
				nodeID = prompted.nodeID
				domainRaw = prompted.domainRaw
				port = prompted.port
				tls = prompted.tls
				tlsCertPath = prompted.tlsCertPath
				tlsKeyPath = prompted.tlsKeyPath
				path = prompted.path
				sni = prompted.sni
				reality = prompted.reality
				realityPublicKey = prompted.realityPublicKey
				realityPrivateKey = prompted.realityPrivateKey
				realityShortID = prompted.realityShortID
				realityFingerprint = prompted.realityFingerprint
				realitySpiderX = prompted.realitySpiderX
				realityServer = prompted.realityServer
				realityServerPort = prompted.realityServerPort
				vlessFlow = prompted.vlessFlow
				enabled = prompted.enabled
				linkUserID = prompted.linkUserID
			}

			if strings.TrimSpace(protocol) == "" {
				return fmt.Errorf("--type is required")
			}
			if strings.TrimSpace(transport) == "" {
				return fmt.Errorf("--transport is required")
			}
			if strings.TrimSpace(nodeID) == "" {
				return fmt.Errorf("--node-id is required")
			}
			if port <= 0 || port > 65535 {
				return fmt.Errorf("--port must be in range 1..65535")
			}
			if port == 443 && !allowPort443 {
				return fmt.Errorf("--port 443 is reserved by default; use --allow-port-443 for advanced/custom setup")
			}

			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			if reality {
				if strings.ToLower(strings.TrimSpace(protocol)) != string(domain.ProtocolVLESS) {
					return fmt.Errorf("--reality is supported only for --type vless")
				}
				if strings.ToLower(strings.TrimSpace(transport)) != "tcp" {
					return fmt.Errorf("--reality requires --transport tcp")
				}
				if strings.TrimSpace(engineRaw) == "" {
					engineRaw = string(domain.EngineXray)
				}
				if strings.ToLower(strings.TrimSpace(engineRaw)) != string(domain.EngineXray) {
					return fmt.Errorf("--reality requires --engine xray")
				}
				if strings.TrimSpace(realityPublicKey) == "" {
					return fmt.Errorf("--reality-public-key is required when --reality is enabled")
				}
				if strings.TrimSpace(realityPrivateKey) == "" {
					return fmt.Errorf("--reality-private-key is required when --reality is enabled")
				}
				if strings.TrimSpace(realityServer) == "" {
					return fmt.Errorf("--reality-server is required when --reality is enabled")
				}
				if realityServerPort <= 0 || realityServerPort > 65535 {
					return fmt.Errorf("--reality-server-port must be in range 1..65535 when --reality is enabled")
				}
				if strings.TrimSpace(realityFingerprint) == "" {
					realityFingerprint = "chrome"
				}
				if strings.TrimSpace(vlessFlow) == "" {
					vlessFlow = "xtls-rprx-vision"
				}
				if strings.TrimSpace(realityShortID) == "" {
					realityShortID, err = randomHex(4)
					if err != nil {
						return fmt.Errorf("generate reality short id: %w", err)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "reality short id generated automatically: %s\n", realityShortID)
				}
			}

			resolvedEngine, err := engine.Resolve(engine.ResolutionRequest{
				Protocol:        domain.Protocol(strings.ToLower(strings.TrimSpace(protocol))),
				Transport:       transport,
				PreferredEngine: domain.Engine(strings.ToLower(strings.TrimSpace(engineRaw))),
			})
			if err != nil {
				return err
			}

			created, err := store.Inbounds().Create(cmd.Context(), domain.Inbound{
				Type:               domain.Protocol(strings.ToLower(strings.TrimSpace(protocol))),
				Engine:             resolvedEngine,
				NodeID:             strings.TrimSpace(nodeID),
				Domain:             strings.TrimSpace(domainRaw),
				Port:               port,
				TLSEnabled:         tls,
				TLSCertPath:        strings.TrimSpace(tlsCertPath),
				TLSKeyPath:         strings.TrimSpace(tlsKeyPath),
				Transport:          strings.ToLower(strings.TrimSpace(transport)),
				Path:               strings.TrimSpace(path),
				SNI:                strings.TrimSpace(sni),
				RealityEnabled:     reality,
				RealityPublicKey:   strings.TrimSpace(realityPublicKey),
				RealityPrivateKey:  strings.TrimSpace(realityPrivateKey),
				RealityShortID:     strings.TrimSpace(realityShortID),
				RealityFingerprint: strings.TrimSpace(realityFingerprint),
				RealitySpiderX:     strings.TrimSpace(realitySpiderX),
				RealityServer:      strings.TrimSpace(realityServer),
				RealityServerPort:  realityServerPort,
				VLESSFlow:          strings.TrimSpace(vlessFlow),
				Enabled:            enabled,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(
				cmd.OutOrStdout(),
				"added inbound: id=%s type=%s transport=%s engine=%s node_id=%s port=%d tls=%t reality=%t flow=%s enabled=%t created_at=%s\n",
				created.ID,
				created.Type,
				created.Transport,
				created.Engine,
				created.NodeID,
				created.Port,
				created.TLSEnabled,
				created.RealityEnabled,
				created.VLESSFlow,
				created.Enabled,
				created.CreatedAt.Format(time.RFC3339),
			)

			if strings.TrimSpace(linkUserID) != "" {
				node, err := findNodeByID(cmd.Context(), store, created.NodeID)
				if err != nil {
					return err
				}
				defaultLabel := linkUserID
				if linkedUser, linkedErr := findUserByID(cmd.Context(), store, linkUserID); linkedErr == nil {
					defaultLabel = linkedUser.Name
				}
				cred, err := createCredentialForInbound(created, linkUserID, defaultLabel)
				if err != nil {
					return err
				}
				createdCred, err := store.Credentials().Create(cmd.Context(), cred)
				if err != nil {
					return fmt.Errorf("create credential for immediate client URI: %w", err)
				}
				if usedWizard {
					uri, err := renderSingleClientURI(cmd.Context(), node, created, createdCred)
					if err != nil {
						return fmt.Errorf("build client URI: %w", err)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "client uri: %s\n", uri)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&protocol, "type", "", "Inbound protocol type (vless|hysteria2|xhttp)")
	cmd.Flags().StringVar(&transport, "transport", "", "Inbound transport (protocol-specific)")
	cmd.Flags().StringVar(&engineRaw, "engine", "", "Optional engine preference (sing-box|xray)")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "Node ID to attach this inbound")
	cmd.Flags().StringVar(&domainRaw, "domain", "", "Inbound domain")
	cmd.Flags().IntVar(&port, "port", 0, "Inbound listen port")
	cmd.Flags().BoolVar(&tls, "tls", false, "Enable TLS for this inbound")
	cmd.Flags().StringVar(&tlsCertPath, "tls-cert-path", "", "TLS certificate path for protocols that terminate TLS directly (e.g. hysteria2)")
	cmd.Flags().StringVar(&tlsKeyPath, "tls-key-path", "", "TLS key path for protocols that terminate TLS directly (e.g. hysteria2)")
	cmd.Flags().StringVar(&path, "path", "", "Transport path")
	cmd.Flags().StringVar(&sni, "sni", "", "TLS SNI override")
	cmd.Flags().BoolVar(&reality, "reality", false, "Enable VLESS Reality mode (requires --type vless --transport tcp --engine xray)")
	cmd.Flags().StringVar(&realityPublicKey, "reality-public-key", "", "Reality public key (used in subscription URI as pbk)")
	cmd.Flags().StringVar(&realityPrivateKey, "reality-private-key", "", "Reality private key (used in Xray inbound realitySettings.privateKey)")
	cmd.Flags().StringVar(&realityShortID, "reality-short-id", "", "Reality short ID (sid)")
	cmd.Flags().StringVar(&realityFingerprint, "reality-fingerprint", "", "Reality client fingerprint (fp), default: chrome")
	cmd.Flags().StringVar(&realitySpiderX, "reality-spider-x", "", "Reality spiderX path (spx)")
	cmd.Flags().StringVar(&realityServer, "reality-server", "", "Reality handshake destination server (dest host)")
	cmd.Flags().IntVar(&realityServerPort, "reality-server-port", 0, "Reality handshake destination server port (dest port)")
	cmd.Flags().StringVar(&vlessFlow, "vless-flow", "", "VLESS flow (for Reality typically xtls-rprx-vision)")
	cmd.Flags().StringVar(&linkUserID, "link-user-id", "", "Optional user ID to auto-create credential for this inbound")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Whether inbound is enabled")
	cmd.Flags().BoolVar(&allowPort443, "allow-port-443", false, "Allow using TCP/UDP port 443 (reserved by default)")

	return cmd
}

type inboundAddPromptResult struct {
	protocol           string
	transport          string
	engineRaw          string
	nodeID             string
	domainRaw          string
	port               int
	tls                bool
	tlsCertPath        string
	tlsKeyPath         string
	path               string
	sni                string
	reality            bool
	realityPublicKey   string
	realityPrivateKey  string
	realityShortID     string
	realityFingerprint string
	realitySpiderX     string
	realityServer      string
	realityServerPort  int
	vlessFlow          string
	enabled            bool
	linkUserID         string
}

func promptInboundAddWizard(cmd *cobra.Command, dbPath, linkedUserID string) (inboundAddPromptResult, error) {
	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Interactive inbound setup")

	protocol, err := promptChoice(in, out, "Inbound type", []string{"vless", "hysteria2", "xhttp"}, "vless")
	if err != nil {
		return inboundAddPromptResult{}, err
	}

	transport := ""
	switch protocol {
	case "vless":
		transport, err = promptChoice(in, out, "Transport", []string{"tcp", "ws", "grpc"}, "tcp")
		if err != nil {
			return inboundAddPromptResult{}, err
		}
	case "hysteria2":
		transport = "udp"
		fmt.Fprintf(out, "Transport: %s\n", transport)
	case "xhttp":
		transport = "xhttp"
		fmt.Fprintf(out, "Transport: %s\n", transport)
	}

	engineChoice, err := promptChoice(in, out, "Engine", []string{"auto", "sing-box", "xray"}, "auto")
	if err != nil {
		return inboundAddPromptResult{}, err
	}
	engineRaw := ""
	if engineChoice != "auto" {
		engineRaw = engineChoice
	}

	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return inboundAddPromptResult{}, err
	}
	defer store.Close()

	nodes, err := store.Nodes().List(cmd.Context())
	if err != nil {
		return inboundAddPromptResult{}, err
	}
	if len(nodes) == 0 {
		return inboundAddPromptResult{}, fmt.Errorf("no nodes found; add a node first with `proxyctl node add`")
	}
	inbounds, err := store.Inbounds().List(cmd.Context())
	if err != nil {
		return inboundAddPromptResult{}, err
	}
	usedPorts := make(map[int]struct{}, len(inbounds))
	for _, item := range inbounds {
		if !item.Enabled || item.Port <= 0 {
			continue
		}
		usedPorts[item.Port] = struct{}{}
	}

	nodeOptions := make([]string, 0, len(nodes))
	nodeByOption := make(map[string]domain.Node, len(nodes))
	for _, n := range nodes {
		item := fmt.Sprintf("%s (%s)", n.ID, n.Host)
		nodeOptions = append(nodeOptions, item)
		nodeByOption[item] = n
	}
	nodeChoice, err := promptChoice(in, out, "Node", nodeOptions, nodeOptions[0])
	if err != nil {
		return inboundAddPromptResult{}, err
	}
	nodeID := nodeByOption[nodeChoice].ID

	defaultDomain := strings.TrimSpace(nodeByOption[nodeChoice].Host)
	domainRaw, err := promptLine(in, out, "Domain", defaultDomain)
	if err != nil {
		return inboundAddPromptResult{}, err
	}

	defaultPort := suggestWizardPort(protocol, transport, usedPorts, hostPortBusy)
	port, err := promptInt(in, out, "Port", defaultPort)
	if err != nil {
		return inboundAddPromptResult{}, err
	}
	network := wizardPortNetwork(protocol, transport)
	if isWizardPortBusy(port, usedPorts, network, hostPortBusy) {
		suggested := suggestWizardPort(protocol, transport, usedPorts, hostPortBusy)
		if suggested != port {
			switchNow, err := promptBool(in, out, fmt.Sprintf("Port %d appears occupied. Switch to %d (y/n)", port, suggested), true)
			if err != nil {
				return inboundAddPromptResult{}, err
			}
			if switchNow {
				port = suggested
				fmt.Fprintf(out, "Using suggested free port: %d\n", port)
			}
		}
	}
	if port == 443 {
		use443, err := promptBool(in, out, "Port 443 is reserved by default. Use it anyway for advanced setup (y/n)", false)
		if err != nil {
			return inboundAddPromptResult{}, err
		}
		if !use443 {
			port = defaultPort
			fmt.Fprintf(out, "Using safer default port: %d\n", port)
		}
	}

	defaultTLS := protocol == "hysteria2" || protocol == "xhttp" || transport == "ws" || transport == "grpc"
	tls, err := promptBool(in, out, "Enable TLS (y/n)", defaultTLS)
	if err != nil {
		return inboundAddPromptResult{}, err
	}

	tlsCertPath := ""
	tlsKeyPath := ""
	if tls {
		tlsCertPath, err = promptLine(in, out, "TLS cert path (optional)", "")
		if err != nil {
			return inboundAddPromptResult{}, err
		}
		tlsKeyPath, err = promptLine(in, out, "TLS key path (optional)", "")
		if err != nil {
			return inboundAddPromptResult{}, err
		}
	}

	path := ""
	if transport == "ws" {
		path, err = promptLine(in, out, "WS path", "/ws")
		if err != nil {
			return inboundAddPromptResult{}, err
		}
	}
	if transport == "grpc" {
		path, err = promptLine(in, out, "gRPC service name", "grpc")
		if err != nil {
			return inboundAddPromptResult{}, err
		}
	}
	if transport == "xhttp" {
		path, err = promptLine(in, out, "XHTTP path", "/xhttp")
		if err != nil {
			return inboundAddPromptResult{}, err
		}
	}

	sni, err := promptLine(in, out, "SNI (optional)", "")
	if err != nil {
		return inboundAddPromptResult{}, err
	}

	reality := false
	realityPublicKey := ""
	realityPrivateKey := ""
	realityShortID := ""
	realityFingerprint := ""
	realitySpiderX := ""
	realityServer := ""
	realityServerPort := 0
	vlessFlow := ""
	if protocol == "vless" && transport == "tcp" {
		reality, err = promptBool(in, out, "Enable Reality (y/n)", true)
		if err != nil {
			return inboundAddPromptResult{}, err
		}
		if reality {
			realityMode, err := promptChoice(in, out, "Reality setup mode", []string{
				"quick (recommended)",
				"advanced (manual overrides)",
			}, "quick (recommended)")
			if err != nil {
				return inboundAddPromptResult{}, err
			}
			if realityMode == "quick (recommended)" {
				realityPublicKey, realityPrivateKey, err = generateRealityKeyPair()
				if err != nil {
					return inboundAddPromptResult{}, fmt.Errorf("generate reality keys: %w", err)
				}
				fmt.Fprintln(out, "Reality keys generated automatically")
				realityShortID, err = randomHex(4)
				if err != nil {
					return inboundAddPromptResult{}, fmt.Errorf("generate reality short id: %w", err)
				}
				fmt.Fprintf(out, "Reality short id generated automatically: %s\n", realityShortID)
				realityServer = "www.cloudflare.com"
				realityServerPort = 443
				realityFingerprint = "chrome"
				vlessFlow = "xtls-rprx-vision"
				if strings.TrimSpace(sni) == "" {
					sni = realityServer
					fmt.Fprintf(out, "SNI auto-set to Reality server: %s\n", sni)
				}
			} else {
				keyMode, err := promptChoice(in, out, "Reality keys", []string{
					"generate automatically",
					"enter manually",
				}, "generate automatically")
				if err != nil {
					return inboundAddPromptResult{}, err
				}
				if keyMode == "generate automatically" {
					realityPublicKey, realityPrivateKey, err = generateRealityKeyPair()
					if err != nil {
						return inboundAddPromptResult{}, fmt.Errorf("generate reality keys: %w", err)
					}
					fmt.Fprintln(out, "Reality keys generated automatically")
				} else {
					realityPublicKey, err = promptLineRequired(in, out, "Reality public key")
					if err != nil {
						return inboundAddPromptResult{}, err
					}
					realityPrivateKey, err = promptLineRequired(in, out, "Reality private key")
					if err != nil {
						return inboundAddPromptResult{}, err
					}
				}
				realityServer, err = promptRealityServer(in, out)
				if err != nil {
					return inboundAddPromptResult{}, err
				}
				if strings.TrimSpace(sni) == "" {
					sni = strings.TrimSpace(realityServer)
					fmt.Fprintf(out, "SNI auto-set to Reality server: %s\n", sni)
				}
				realityServerPort, err = promptInt(in, out, "Reality server port", 443)
				if err != nil {
					return inboundAddPromptResult{}, err
				}
				realityShortID, err = promptLine(in, out, "Reality short id (optional, auto-generated when empty)", "")
				if err != nil {
					return inboundAddPromptResult{}, err
				}
				if strings.TrimSpace(realityShortID) == "" {
					realityShortID, err = randomHex(4)
					if err != nil {
						return inboundAddPromptResult{}, fmt.Errorf("generate reality short id: %w", err)
					}
					fmt.Fprintf(out, "Reality short id generated automatically: %s\n", realityShortID)
				}
				realityFingerprint, err = promptRealityFingerprint(in, out, "chrome")
				if err != nil {
					return inboundAddPromptResult{}, err
				}
				realitySpiderX, err = promptLine(in, out, "Reality spiderX path (optional)", "")
				if err != nil {
					return inboundAddPromptResult{}, err
				}
				vlessFlow, err = promptLine(in, out, "VLESS flow", "xtls-rprx-vision")
				if err != nil {
					return inboundAddPromptResult{}, err
				}
			}
		}
	}

	enabled, err := promptBool(in, out, "Enable inbound (y/n)", true)
	if err != nil {
		return inboundAddPromptResult{}, err
	}

	linkUserID := ""
	if strings.TrimSpace(linkedUserID) != "" {
		linkUserID = strings.TrimSpace(linkedUserID)
		fmt.Fprintf(out, "Create client link for user ID: %s\n", linkUserID)
	} else {
		users, err := store.Users().List(cmd.Context())
		if err != nil {
			return inboundAddPromptResult{}, err
		}
		linkOptions := []string{"skip"}
		userByOption := map[string]string{}
		for _, user := range users {
			if !user.Enabled {
				continue
			}
			item := fmt.Sprintf("%s (%s)", user.ID, user.Name)
			linkOptions = append(linkOptions, item)
			userByOption[item] = user.ID
		}
		if len(linkOptions) > 1 {
			linkChoice, err := promptChoice(in, out, "Create client link for user", linkOptions, "skip")
			if err != nil {
				return inboundAddPromptResult{}, err
			}
			if linkChoice != "skip" {
				linkUserID = userByOption[linkChoice]
			}
		} else {
			fmt.Fprintln(out, "No enabled users found, skipping immediate client link creation")
		}
	}

	return inboundAddPromptResult{
		protocol:           protocol,
		transport:          transport,
		engineRaw:          engineRaw,
		nodeID:             nodeID,
		domainRaw:          domainRaw,
		port:               port,
		tls:                tls,
		tlsCertPath:        tlsCertPath,
		tlsKeyPath:         tlsKeyPath,
		path:               path,
		sni:                sni,
		reality:            reality,
		realityPublicKey:   realityPublicKey,
		realityPrivateKey:  realityPrivateKey,
		realityShortID:     realityShortID,
		realityFingerprint: realityFingerprint,
		realitySpiderX:     realitySpiderX,
		realityServer:      realityServer,
		realityServerPort:  realityServerPort,
		vlessFlow:          vlessFlow,
		enabled:            enabled,
		linkUserID:         linkUserID,
	}, nil
}

func promptChoice(in *bufio.Reader, out io.Writer, label string, options []string, defaultValue string) (string, error) {
	displayOptions := make([]string, 0, len(options))
	backOption := ""
	for _, opt := range options {
		if isBackOptionLabel(opt) {
			if backOption == "" {
				backOption = "back"
			}
			continue
		}
		displayOptions = append(displayOptions, opt)
	}

	fmt.Fprintf(out, "\n== %s ==\n", label)
	for i, opt := range displayOptions {
		suffix := ""
		if opt == defaultValue {
			suffix = " [default]"
		}
		fmt.Fprintf(out, "  %d) %s%s\n", i+1, opt, suffix)
	}
	if backOption != "" {
		fmt.Fprintln(out, "  0) back")
	}

	optionMap := make(map[string]string, len(options))
	for _, opt := range options {
		optionMap[strings.ToLower(opt)] = opt
	}

	for {
		fmt.Fprintf(out, "select %s [%s]: ", strings.ToLower(label), defaultValue)
		line, err := in.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultValue, nil
		}
		if idx, err := strconv.Atoi(line); err == nil {
			if idx == 0 && backOption != "" {
				return backOption, nil
			}
			if idx >= 1 && idx <= len(displayOptions) {
				return displayOptions[idx-1], nil
			}
		}
		if resolved, ok := optionMap[strings.ToLower(line)]; ok {
			if isBackOptionLabel(resolved) && backOption != "" {
				return backOption, nil
			}
			return resolved, nil
		}
		allowed := make([]string, 0, len(displayOptions)+1)
		allowed = append(allowed, displayOptions...)
		if backOption != "" {
			allowed = append(allowed, "0 (back)")
		}
		fmt.Fprintf(out, "invalid value; choose one of: %s\n", strings.Join(allowed, ", "))
	}
}

func isBackOptionLabel(value string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "back" || trimmed == "0" || trimmed == "0) back" {
		return true
	}
	if strings.HasPrefix(trimmed, "0)") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "0)")) == "back"
	}
	return false
}

func promptLine(in *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	fmt.Fprintf(out, "%s", label)
	if defaultValue != "" {
		fmt.Fprintf(out, " [%s]", defaultValue)
	}
	fmt.Fprint(out, ": ")
	line, err := in.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue, nil
	}
	return line, nil
}

func promptLineRequired(in *bufio.Reader, out io.Writer, label string) (string, error) {
	for {
		value, err := promptLine(in, out, label, "")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) != "" {
			return value, nil
		}
		fmt.Fprintln(out, "value is required")
	}
}

func promptBool(in *bufio.Reader, out io.Writer, label string, defaultValue bool) (bool, error) {
	def := "y"
	if !defaultValue {
		def = "n"
	}
	for {
		fmt.Fprintf(out, "%s [%s]: ", label, def)
		line, err := in.ReadString('\n')
		if err != nil {
			return false, err
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			return defaultValue, nil
		}
		switch line {
		case "y", "yes", "true", "1":
			return true, nil
		case "n", "no", "false", "0":
			return false, nil
		default:
			fmt.Fprintln(out, "invalid value, use y or n")
		}
	}
}

func promptInt(in *bufio.Reader, out io.Writer, label string, defaultValue int) (int, error) {
	for {
		fmt.Fprintf(out, "%s [%d]: ", label, defaultValue)
		line, err := in.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultValue, nil
		}
		value, err := strconv.Atoi(line)
		if err != nil {
			fmt.Fprintln(out, "invalid number")
			continue
		}
		return value, nil
	}
}

func stdinIsTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func runProxyctlSubcommand(cmd *cobra.Command, args ...string) error {
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable path: %w", err)
	}
	child := exec.CommandContext(cmd.Context(), binPath, args...)
	child.Stdin = cmd.InOrStdin()
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	if err := child.Run(); err != nil {
		return fmt.Errorf("run %s: %w", strings.Join(append([]string{binPath}, args...), " "), err)
	}
	return nil
}

func runWizardUserAdd(cmd *cobra.Command, dbPath string) error {
	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()

	name, err := promptLineRequired(in, out, "User name")
	if err != nil {
		return err
	}
	enabled, err := promptBool(in, out, "Enable user (y/n)", true)
	if err != nil {
		return err
	}

	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	created, err := store.Users().Create(cmd.Context(), domain.User{
		Name:    strings.TrimSpace(name),
		Enabled: enabled,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "added user: id=%s name=%s enabled=%t created_at=%s\n", created.ID, created.Name, created.Enabled, created.CreatedAt.Format(time.RFC3339))
	return nil
}

func runWizardUsersMenu(cmd *cobra.Command, configPath, dbPath string) error {
	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()

	for {
		action, err := promptChoice(in, out, "Users", []string{
			"list users",
			"create user",
			"open user",
			"back",
		}, "list users")
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fmt.Fprintln(out, "users menu cancelled")
				return nil
			}
			return err
		}

		switch action {
		case "list users":
			if err := runWizardUsersList(cmd, dbPath); err != nil {
				return err
			}
		case "create user":
			if err := runWizardUserAdd(cmd, dbPath); err != nil {
				return err
			}
		case "open user":
			user, ok, err := promptWizardSelectUser(cmd, in, out, dbPath)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if err := runWizardUserMenu(cmd, in, out, configPath, dbPath, user); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func runWizardUsersList(cmd *cobra.Command, dbPath string) error {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	users, err := store.Users().List(cmd.Context())
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no users found")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tENABLED\tCREATED_AT")
	for _, user := range users {
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\n", user.ID, user.Name, user.Enabled, user.CreatedAt.Format(time.RFC3339))
	}
	return w.Flush()
}

func wizardHasNodes(cmd *cobra.Command, dbPath string) (bool, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return false, err
	}
	defer store.Close()

	nodes, err := store.Nodes().List(cmd.Context())
	if err != nil {
		return false, err
	}
	return len(nodes) > 0, nil
}

func wizardMainOptions(hasNodes bool) ([]string, string) {
	return wizardMainOptionsByMode(hasNodes, config.DeploymentModePanelNode)
}

func wizardMainOptionsByMode(hasNodes bool, mode config.DeploymentMode) ([]string, string) {
	if mode == "" {
		mode = config.DeploymentModePanelNode
	}

	switch mode {
	case config.DeploymentModePanel:
		return []string{
			"nodes",
			"users",
			"settings",
			"update proxyctl",
			"uninstall proxyctl",
			"exit",
		}, "nodes"
	case config.DeploymentModeNode:
		return []string{
			"inbounds",
			"users",
			"settings",
			"update proxyctl",
			"uninstall proxyctl",
			"exit",
		}, "inbounds"
	}

	if !hasNodes {
		return []string{
			"nodes",
			"settings",
			"update proxyctl",
			"uninstall proxyctl",
			"exit",
		}, "nodes"
	}
	return []string{
		"nodes",
		"inbounds",
		"users",
		"settings",
		"update proxyctl",
		"uninstall proxyctl",
		"exit",
	}, "inbounds"
}

func runWizardUninstall(cmd *cobra.Command, in *bufio.Reader, out io.Writer) error {
	confirm, err := promptBool(in, out, "Permanently uninstall proxyctl and purge all data from this VPS (y/n)", false)
	if err != nil {
		return err
	}
	if !confirm {
		fmt.Fprintln(out, "cancelled")
		return nil
	}
	purgeRuntime, err := promptBool(in, out, "Also purge caddy/nginx apt packages (y/n)", false)
	if err != nil {
		return err
	}
	args := []string{"uninstall", "--yes"}
	if purgeRuntime {
		args = append(args, "--remove-runtime-packages")
	}
	return runProxyctlSubcommand(cmd, args...)
}

func runWizardSettingsMenu(cmd *cobra.Command, configPath string) error {
	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()

	for {
		action, err := promptChoice(in, out, "Settings", []string{
			"show settings",
			"show installed versions",
			"set decoy site path",
			"switch decoy template",
			"back",
		}, "show settings")
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fmt.Fprintln(out, "settings menu cancelled")
				return nil
			}
			return err
		}

		switch action {
		case "show settings":
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "reverse_proxy: %s\n", cfg.ReverseProxy)
			fmt.Fprintf(out, "public.domain: %s\n", strings.TrimSpace(cfg.Public.Domain))
			fmt.Fprintf(out, "paths.decoy_site_dir: %s\n", strings.TrimSpace(cfg.Paths.DecoySiteDir))
		case "show installed versions":
			printInstalledVersions(cmd.Context(), out)
		case "set decoy site path":
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}

			nextPath, err := promptLine(in, out, "Decoy site directory", strings.TrimSpace(cfg.Paths.DecoySiteDir))
			if err != nil {
				return err
			}
			nextPath = strings.TrimSpace(nextPath)
			if nextPath == "" {
				fmt.Fprintln(out, "value is required")
				continue
			}
			if !filepath.IsAbs(nextPath) {
				fmt.Fprintln(out, "path must be absolute")
				continue
			}

			if err := os.MkdirAll(nextPath, 0o755); err != nil {
				return fmt.Errorf("create decoy site directory: %w", err)
			}
			if err := setConfigDecoySiteDir(configPath, nextPath); err != nil {
				return err
			}
			fmt.Fprintf(out, "updated config: paths.decoy_site_dir=%s\n", nextPath)
			fmt.Fprintln(out, "run `proxyctl render` and `proxyctl apply` to refresh runtime assets")
		case "switch decoy template":
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			templatesRoot := resolveDecoyTemplateLibraryPath(cfg)
			templates, err := listDecoyTemplates(templatesRoot)
			if err != nil {
				return err
			}
			if len(templates) == 0 {
				fmt.Fprintf(out, "no decoy templates found in %s\n", templatesRoot)
				fmt.Fprintln(out, "upload templates there as <name>/index.html + <name>/assets/style.css")
				continue
			}
			choice, err := promptChoice(in, out, "Decoy template", append(templates, "back"), templates[0])
			if err != nil {
				return err
			}
			if isBackOptionLabel(choice) {
				continue
			}
			srcDir := filepath.Join(templatesRoot, choice)
			if err := activateDecoyTemplateFromDir(cfg, srcDir); err != nil {
				return err
			}
			fmt.Fprintf(out, "activated decoy template: %s\n", choice)
			fmt.Fprintln(out, "template applied to runtime and active templates directory")
		default:
			return nil
		}
	}
}

type componentVersion struct {
	Name    string
	Version string
}

func printInstalledVersions(ctx context.Context, out io.Writer) {
	versions := collectInstalledVersions(ctx)
	fmt.Fprintln(out, "installed versions:")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "component\tversion")
	for _, item := range versions {
		fmt.Fprintf(w, "%s\t%s\n", item.Name, item.Version)
	}
	_ = w.Flush()
}

func collectInstalledVersions(ctx context.Context) []componentVersion {
	versions := []componentVersion{
		{
			Name:    "proxyctl",
			Version: strings.TrimSpace(Version),
		},
		{Name: "sing-box", Version: probeCommandVersion(ctx, "sing-box", "version")},
		{Name: "xray", Version: probeCommandVersion(ctx, "xray", "version")},
		{Name: "caddy", Version: probeCommandVersion(ctx, "caddy", "version")},
		{Name: "nginx", Version: probeCommandVersion(ctx, "nginx", "-v")},
		{Name: "sqlite3", Version: probeCommandVersion(ctx, "sqlite3", "--version")},
		{Name: "systemd", Version: probeCommandVersion(ctx, "systemctl", "--version")},
	}
	for i := range versions {
		if strings.TrimSpace(versions[i].Version) == "" {
			versions[i].Version = "unknown"
		}
	}
	return versions
}

func probeCommandVersion(ctx context.Context, name string, args ...string) string {
	if _, err := lookPath(name); err != nil {
		return "not installed"
	}
	out, err := runCommandOutput(ctx, name, args...)
	if err != nil {
		if strings.TrimSpace(out) == "" {
			return "error"
		}
		return firstNonEmptyLine(out)
	}
	return firstNonEmptyLine(out)
}

func firstNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func resolveDecoyTemplateLibraryPath(cfg config.AppConfig) string {
	base := strings.TrimSpace(filepath.Dir(cfg.Paths.TemplatesDir))
	if base == "" || base == "." || base == "/" {
		return "/usr/share/proxy-orchestrator/decoy-templates"
	}
	return filepath.Join(base, "decoy-templates")
}

func listDecoyTemplates(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read decoy template directory %q: %w", root, err)
	}

	items := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		if !decoyTemplateLooksValid(filepath.Join(root, name)) {
			continue
		}
		items = append(items, name)
	}
	sort.Strings(items)
	return items, nil
}

func decoyTemplateLooksValid(dir string) bool {
	indexPath := filepath.Join(dir, "index.html")
	stylePath := filepath.Join(dir, "assets", "style.css")
	if _, err := os.Stat(indexPath); err != nil {
		return false
	}
	if _, err := os.Stat(stylePath); err != nil {
		return false
	}
	return true
}

func activateDecoyTemplateFromDir(cfg config.AppConfig, srcDir string) error {
	indexPath := filepath.Join(srcDir, "index.html")
	stylePath := filepath.Join(srcDir, "assets", "style.css")
	indexContent, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("read template index %q: %w", indexPath, err)
	}
	styleContent, err := os.ReadFile(stylePath)
	if err != nil {
		return fmt.Errorf("read template style %q: %w", stylePath, err)
	}

	runtimeDecoyDir := strings.TrimSpace(cfg.Paths.DecoySiteDir)
	activeTemplateDir := filepath.Join(strings.TrimSpace(cfg.Paths.TemplatesDir), "decoy-site")
	if err := os.MkdirAll(filepath.Join(runtimeDecoyDir, "assets"), 0o755); err != nil {
		return fmt.Errorf("create runtime decoy directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(activeTemplateDir, "assets"), 0o755); err != nil {
		return fmt.Errorf("create active decoy template directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(runtimeDecoyDir, "index.html"), indexContent, 0o644); err != nil {
		return fmt.Errorf("write runtime decoy index: %w", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDecoyDir, "assets", "style.css"), styleContent, 0o644); err != nil {
		return fmt.Errorf("write runtime decoy style: %w", err)
	}
	if err := os.WriteFile(filepath.Join(activeTemplateDir, "index.html"), indexContent, 0o644); err != nil {
		return fmt.Errorf("write active template index: %w", err)
	}
	if err := os.WriteFile(filepath.Join(activeTemplateDir, "assets", "style.css"), styleContent, 0o644); err != nil {
		return fmt.Errorf("write active template style: %w", err)
	}
	return nil
}

func setConfigDecoySiteDir(configPath, decoyPath string) error {
	decoyPath = strings.TrimSpace(decoyPath)
	if decoyPath == "" {
		return fmt.Errorf("decoy path is required")
	}

	root := map[string]any{}
	mode := os.FileMode(0o640)
	if info, err := os.Stat(configPath); err == nil {
		mode = info.Mode().Perm()
	}

	raw, err := os.ReadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read config %q: %w", configPath, err)
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := yaml.Unmarshal(raw, &root); err != nil {
			return fmt.Errorf("parse config %q: %w", configPath, err)
		}
	}

	pathsRaw, ok := root["paths"]
	if !ok {
		root["paths"] = map[string]any{}
		pathsRaw = root["paths"]
	}
	pathsMap, ok := pathsRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("config key paths is not an object")
	}
	pathsMap["decoy_site_dir"] = decoyPath

	rendered, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("encode config %q: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, rendered, mode); err != nil {
		return fmt.Errorf("write config %q: %w", configPath, err)
	}
	return nil
}

func runWizardNodesMenu(cmd *cobra.Command, dbPath string) error {
	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()

	for {
		action, err := promptChoice(in, out, "Nodes", []string{
			"list nodes",
			"create node",
			"open node",
			"back",
		}, "list nodes")
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fmt.Fprintln(out, "nodes menu cancelled")
				return nil
			}
			return err
		}

		switch action {
		case "list nodes":
			if err := runWizardNodesList(cmd, dbPath); err != nil {
				return err
			}
		case "create node":
			if err := runWizardNodeAdd(cmd, dbPath); err != nil {
				return err
			}
		case "open node":
			node, ok, err := promptWizardSelectNode(cmd, in, out, dbPath)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if err := runWizardNodeMenu(cmd, in, out, dbPath, node); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func runWizardNodesList(cmd *cobra.Command, dbPath string) error {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	nodes, err := store.Nodes().List(cmd.Context())
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no nodes found")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tHOST\tROLE\tENABLED\tCREATED_AT")
	for _, node := range nodes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%s\n", node.ID, node.Name, node.Host, node.Role, node.Enabled, node.CreatedAt.Format(time.RFC3339))
	}
	return w.Flush()
}

func runWizardNodeAdd(cmd *cobra.Command, dbPath string) error {
	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()

	name, err := promptLineRequired(in, out, "Node name")
	if err != nil {
		return err
	}
	host, err := promptLineRequired(in, out, "Node host (domain or IP)")
	if err != nil {
		return err
	}
	role, err := promptLine(in, out, "Node role", "primary")
	if err != nil {
		return err
	}
	enabled, err := promptBool(in, out, "Enable node (y/n)", true)
	if err != nil {
		return err
	}

	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	created, err := store.Nodes().Create(cmd.Context(), domain.Node{
		Name:    strings.TrimSpace(name),
		Host:    strings.TrimSpace(host),
		Role:    domain.NodeRole(strings.TrimSpace(role)),
		Enabled: enabled,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "added node: id=%s name=%s host=%s role=%s enabled=%t created_at=%s\n", created.ID, created.Name, created.Host, created.Role, created.Enabled, created.CreatedAt.Format(time.RFC3339))
	return nil
}

func promptWizardSelectNode(cmd *cobra.Command, in *bufio.Reader, out io.Writer, dbPath string) (domain.Node, bool, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return domain.Node{}, false, err
	}
	defer store.Close()

	nodes, err := store.Nodes().List(cmd.Context())
	if err != nil {
		return domain.Node{}, false, err
	}
	if len(nodes) == 0 {
		fmt.Fprintln(out, "no nodes found")
		return domain.Node{}, false, nil
	}

	options := make([]string, 0, len(nodes))
	byOption := make(map[string]domain.Node, len(nodes))
	for _, node := range nodes {
		status := "disabled"
		if node.Enabled {
			status = "enabled"
		}
		label := fmt.Sprintf("%s (%s, %s, %s)", node.ID, node.Name, node.Host, status)
		options = append(options, label)
		byOption[label] = node
	}

	choice, err := promptChoice(in, out, "Select node", options, options[0])
	if err != nil {
		return domain.Node{}, false, err
	}
	return byOption[choice], true, nil
}

func runWizardNodeMenu(cmd *cobra.Command, in *bufio.Reader, out io.Writer, dbPath string, node domain.Node) error {
	for {
		enabledAction := "enable node"
		if node.Enabled {
			enabledAction = "disable node"
		}
		action, err := promptChoice(in, out, fmt.Sprintf("Node %s (%s)", node.Name, node.ID), []string{
			"show details",
			"edit node",
			enabledAction,
			"delete node",
			"back",
		}, "show details")
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fmt.Fprintln(out, "node menu cancelled")
				return nil
			}
			return err
		}

		switch action {
		case "show details":
			if err := runWizardShowNodeDetails(cmd, out, dbPath, node.ID); err != nil {
				return err
			}
		case "edit node":
			updated, err := runWizardEditNode(cmd, in, out, dbPath, node)
			if err != nil {
				return err
			}
			node = updated
		case "enable node", "disable node":
			updated, err := runWizardSetNodeEnabled(cmd, out, dbPath, node, action == "enable node")
			if err != nil {
				return err
			}
			node = updated
		case "delete node":
			deleted, err := runWizardDeleteNode(cmd, in, out, dbPath, node)
			if err != nil {
				return err
			}
			if deleted {
				return nil
			}
		default:
			return nil
		}
	}
}

func runWizardShowNodeDetails(cmd *cobra.Command, out io.Writer, dbPath, nodeID string) error {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	nodes, err := store.Nodes().List(cmd.Context())
	if err != nil {
		return err
	}
	inbounds, err := store.Inbounds().List(cmd.Context())
	if err != nil {
		return err
	}

	var node domain.Node
	found := false
	for _, item := range nodes {
		if item.ID == nodeID {
			node = item
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(out, "node not found: %s\n", nodeID)
		return nil
	}

	attached := 0
	for _, inbound := range inbounds {
		if inbound.NodeID == nodeID {
			attached++
		}
	}
	fmt.Fprintf(out, "id: %s\n", node.ID)
	fmt.Fprintf(out, "name: %s\n", node.Name)
	fmt.Fprintf(out, "host: %s\n", node.Host)
	fmt.Fprintf(out, "role: %s\n", node.Role)
	fmt.Fprintf(out, "enabled: %t\n", node.Enabled)
	fmt.Fprintf(out, "created_at: %s\n", node.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(out, "attached_inbounds: %d\n", attached)
	return nil
}

func runWizardEditNode(cmd *cobra.Command, in *bufio.Reader, out io.Writer, dbPath string, node domain.Node) (domain.Node, error) {
	name, err := promptLine(in, out, "Node name", node.Name)
	if err != nil {
		return domain.Node{}, err
	}
	host, err := promptLine(in, out, "Node host (domain or IP)", node.Host)
	if err != nil {
		return domain.Node{}, err
	}
	role, err := promptLine(in, out, "Node role", string(node.Role))
	if err != nil {
		return domain.Node{}, err
	}

	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return domain.Node{}, err
	}
	defer store.Close()

	updated, err := store.Nodes().Update(cmd.Context(), domain.Node{
		ID:      node.ID,
		Name:    strings.TrimSpace(name),
		Host:    strings.TrimSpace(host),
		Role:    domain.NodeRole(strings.TrimSpace(role)),
		Enabled: node.Enabled,
	})
	if err != nil {
		return domain.Node{}, err
	}
	fmt.Fprintf(out, "node updated: id=%s name=%s host=%s role=%s enabled=%t\n", updated.ID, updated.Name, updated.Host, updated.Role, updated.Enabled)
	return updated, nil
}

func runWizardSetNodeEnabled(cmd *cobra.Command, out io.Writer, dbPath string, node domain.Node, enabled bool) (domain.Node, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return domain.Node{}, err
	}
	defer store.Close()

	updated, err := store.Nodes().Update(cmd.Context(), domain.Node{
		ID:      node.ID,
		Name:    node.Name,
		Host:    node.Host,
		Role:    node.Role,
		Enabled: enabled,
	})
	if err != nil {
		return domain.Node{}, err
	}
	fmt.Fprintf(out, "node state updated: id=%s enabled=%t\n", updated.ID, updated.Enabled)
	return updated, nil
}

func runWizardDeleteNode(cmd *cobra.Command, in *bufio.Reader, out io.Writer, dbPath string, node domain.Node) (bool, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return false, err
	}
	defer store.Close()

	inbounds, err := store.Inbounds().List(cmd.Context())
	if err != nil {
		return false, err
	}
	attached := 0
	for _, inbound := range inbounds {
		if inbound.NodeID == node.ID {
			attached++
		}
	}
	if attached > 0 {
		fmt.Fprintf(out, "warning: node has %d attached inbound(s); deleting node will remove them too\n", attached)
	}
	confirm, err := promptBool(in, out, "Delete node (y/n)", false)
	if err != nil {
		return false, err
	}
	if !confirm {
		fmt.Fprintln(out, "cancelled")
		return false, nil
	}

	deleted, err := store.Nodes().Delete(cmd.Context(), node.ID)
	if err != nil {
		return false, err
	}
	fmt.Fprintf(out, "node deleted: id=%s deleted=%t\n", node.ID, deleted)
	return deleted, nil
}

func runWizardInboundsMenu(cmd *cobra.Command, configPath, dbPath string) error {
	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()

	for {
		action, err := promptChoice(in, out, "Inbounds", []string{
			"list inbounds",
			"create inbound",
			"open inbound",
			"back",
		}, "list inbounds")
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fmt.Fprintln(out, "inbounds menu cancelled")
				return nil
			}
			return err
		}

		switch action {
		case "list inbounds":
			if err := runProxyctlSubcommand(cmd, "inbound", "list", "--db", dbPath); err != nil {
				return err
			}
		case "create inbound":
			if err := runProxyctlSubcommand(cmd, "inbound", "add", "--db", dbPath); err != nil {
				return err
			}
			applyNow, err := promptBool(in, out, "Apply runtime changes now (y/n)", true)
			if err != nil {
				return err
			}
			if applyNow {
				if err := runProxyctlSubcommand(cmd, "apply", "--config", configPath, "--db", dbPath); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(out, "inbound saved; run `proxyctl apply --config /etc/proxy-orchestrator/proxyctl.yaml` to activate it")
			}
		case "open inbound":
			inbound, ok, err := promptWizardSelectInbound(cmd, in, out, dbPath)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if err := runWizardInboundMenu(cmd, in, out, configPath, dbPath, inbound); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func promptWizardSelectUser(cmd *cobra.Command, in *bufio.Reader, out io.Writer, dbPath string) (domain.User, bool, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return domain.User{}, false, err
	}
	defer store.Close()

	users, err := store.Users().List(cmd.Context())
	if err != nil {
		return domain.User{}, false, err
	}
	if len(users) == 0 {
		fmt.Fprintln(out, "no users found")
		return domain.User{}, false, nil
	}

	userChoice, userByChoice, err := promptUserChoice(in, out, users, "Select user")
	if err != nil {
		return domain.User{}, false, err
	}
	return userByChoice[userChoice], true, nil
}

func promptWizardSelectInbound(cmd *cobra.Command, in *bufio.Reader, out io.Writer, dbPath string) (domain.Inbound, bool, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return domain.Inbound{}, false, err
	}
	defer store.Close()

	inbounds, err := store.Inbounds().List(cmd.Context())
	if err != nil {
		return domain.Inbound{}, false, err
	}
	if len(inbounds) == 0 {
		fmt.Fprintln(out, "no inbounds found")
		return domain.Inbound{}, false, nil
	}

	options := make([]string, 0, len(inbounds))
	byOption := make(map[string]domain.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		status := "disabled"
		if inbound.Enabled {
			status = "enabled"
		}
		label := fmt.Sprintf("%s (%s/%s %s:%d, %s)", inbound.ID, inbound.Type, inbound.Transport, inbound.Domain, inbound.Port, status)
		options = append(options, label)
		byOption[label] = inbound
	}

	choice, err := promptChoice(in, out, "Select inbound", options, options[0])
	if err != nil {
		return domain.Inbound{}, false, err
	}
	return byOption[choice], true, nil
}

func runWizardUserMenu(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, user domain.User) error {
	for {
		hasSubscription, err := userHasSubscription(cmd, dbPath, user.ID)
		if err != nil {
			return err
		}

		action, err := promptChoice(
			in,
			out,
			fmt.Sprintf("User %s (%s)", user.Name, user.ID),
			buildWizardUserMenuOptions(hasSubscription),
			"show configs",
		)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fmt.Fprintln(out, "user menu cancelled")
				return nil
			}
			return err
		}

		switch action {
		case "attach to existing inbound":
			if err := runWizardAttachUserToInbound(cmd, in, out, configPath, dbPath, user); err != nil {
				return err
			}
		case "show configs":
			if err := runWizardShowUserConfigs(cmd, out, dbPath, user); err != nil {
				return err
			}
		case "generate subscription":
			if err := runWizardGenerateUserSubscription(cmd, in, out, configPath, dbPath, user); err != nil {
				return err
			}
		case "delete subscription":
			if err := runWizardDeleteUserSubscription(cmd, in, out, configPath, dbPath, user); err != nil {
				return err
			}
		case "open credential":
			if err := runWizardOpenCredential(cmd, in, out, dbPath, user); err != nil {
				return err
			}
		case "delete specific config":
			if err := runWizardDeleteSpecificUserConfig(cmd, in, out, configPath, dbPath, user); err != nil {
				return err
			}
		case "delete user completely":
			deleted, err := runWizardDeleteUserCompletely(cmd, in, out, configPath, dbPath, user)
			if err != nil {
				return err
			}
			if deleted {
				return nil
			}
		default:
			return nil
		}
	}
}

func buildWizardUserMenuOptions(hasSubscription bool) []string {
	options := []string{
		"attach to existing inbound",
		"show configs",
		"generate subscription",
	}
	if hasSubscription {
		options = append(options, "delete subscription")
	}
	options = append(options,
		"open credential",
		"delete specific config",
		"delete user completely",
		"back",
	)
	return options
}

func userHasSubscription(cmd *cobra.Command, dbPath, userID string) (bool, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return false, err
	}
	defer store.Close()

	_, err = store.Subscriptions().GetByUserID(cmd.Context(), userID)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
		return false, nil
	}
	return false, err
}

func runWizardGenerateUserSubscription(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, user domain.User) error {
	appCfg, err := loadAppConfig(configPath)
	if err != nil {
		return err
	}

	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	svc := subscriptionservice.New(
		store,
		appCfg.Paths.Subscription,
		subscriptionPublicDir(appCfg.Paths.Subscription),
		singbox.New(nil),
		xray.New(nil),
	)

	mode, err := promptChoice(in, out, "Subscription mode", []string{
		"default profile (all user configs)",
		"named profile (choose inbounds)",
		"back",
	}, "default profile (all user configs)")
	if err != nil {
		return err
	}
	if mode == "back" {
		return nil
	}

	profile := subscriptionservice.DefaultProfileName
	selectedInbounds := []string(nil)
	if mode == "named profile (choose inbounds)" {
		inboundChoices, listErr := wizardUserInboundChoices(cmd, dbPath, user.ID)
		if listErr != nil {
			return listErr
		}
		if len(inboundChoices) == 0 {
			fmt.Fprintln(out, "no inbounds available for this user")
			return nil
		}

		defaultName := "mobile"
		profile, err = promptLine(in, out, "Profile name", defaultName)
		if err != nil {
			return err
		}
		normalizedProfile := wizardNormalizeProfileName(profile)
		if normalizedProfile == "" {
			return fmt.Errorf("profile name is required")
		}
		if normalizedProfile != profile {
			fmt.Fprintf(out, "profile normalized: %s\n", normalizedProfile)
		}
		profile = normalizedProfile

		fmt.Fprintln(out, "available inbounds:")
		defaultIndexes := make([]string, 0, len(inboundChoices))
		for i, choice := range inboundChoices {
			idx := i + 1
			defaultIndexes = append(defaultIndexes, strconv.Itoa(idx))
			fmt.Fprintf(out, "  %d) %s\n", idx, choice.Label)
		}
		inboundLine, lineErr := promptLine(in, out, "Select inbounds (comma-separated numbers)", strings.Join(defaultIndexes, ","))
		if lineErr != nil {
			return lineErr
		}
		selectedIdx, parseErr := parseIndexCSV(inboundLine, len(inboundChoices))
		if parseErr != nil {
			return parseErr
		}
		selectedInbounds = make([]string, 0, len(selectedIdx))
		for _, idx := range selectedIdx {
			selectedInbounds = append(selectedInbounds, inboundChoices[idx-1].InboundID)
		}
		if len(selectedInbounds) == 0 {
			return fmt.Errorf("at least one inbound is required for named profile")
		}
	}

	generated, err := svc.GenerateProfile(cmd.Context(), user.ID, profile, selectedInbounds)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "generated subscription for user=%s (%s) profile=%s\n", generated.User.Name, generated.User.ID, generated.ProfileName)
	fmt.Fprintf(out, "txt: %s\n", generated.TXTPath)
	fmt.Fprintf(out, "base64: %s\n", generated.Base64Path)
	fmt.Fprintf(out, "json: %s\n", generated.JSONPath)
	if link := buildSubscriptionPublicURL(appCfg, generated.AccessToken); link != "" {
		fmt.Fprintf(out, "url: %s\n", link)
	} else if strings.TrimSpace(generated.AccessToken) != "" {
		fmt.Fprintf(out, "token: %s\n", generated.AccessToken)
		fmt.Fprintln(out, "url unavailable: set public.domain in proxyctl config")
	}
	return nil
}

type wizardInboundChoice struct {
	InboundID string
	Label     string
}

func wizardUserInboundChoices(cmd *cobra.Command, dbPath, userID string) ([]wizardInboundChoice, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	credentials, err := store.Credentials().List(cmd.Context())
	if err != nil {
		return nil, err
	}
	inbounds, err := store.Inbounds().List(cmd.Context())
	if err != nil {
		return nil, err
	}
	nodes, err := store.Nodes().List(cmd.Context())
	if err != nil {
		return nil, err
	}

	inboundByID := make(map[string]domain.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	nodeByID := make(map[string]domain.Node, len(nodes))
	for _, node := range nodes {
		nodeByID[node.ID] = node
	}

	inboundSet := map[string]domain.Inbound{}
	for _, credential := range credentials {
		if credential.UserID != userID {
			continue
		}
		inbound, ok := inboundByID[credential.InboundID]
		if !ok {
			continue
		}
		inboundSet[credential.InboundID] = inbound
	}
	if len(inboundSet) == 0 {
		return nil, nil
	}

	choices := make([]wizardInboundChoice, 0, len(inboundSet))
	for inboundID, inbound := range inboundSet {
		host := strings.TrimSpace(inbound.Domain)
		if host == "" {
			if node, ok := nodeByID[inbound.NodeID]; ok && strings.TrimSpace(node.Host) != "" {
				host = node.Host
			}
		}
		if host == "" {
			host = "<no-domain>"
		}
		nodeLabel := ""
		if node, ok := nodeByID[inbound.NodeID]; ok {
			nodeLabel = strings.TrimSpace(node.Name)
			if nodeLabel == "" {
				nodeLabel = strings.TrimSpace(node.Host)
			}
		}
		if nodeLabel == "" {
			nodeLabel = inbound.NodeID
		}
		shortID := inboundID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		choices = append(choices, wizardInboundChoice{
			InboundID: inboundID,
			Label:     fmt.Sprintf("%s/%s %s:%d (node: %s, id: %s)", inbound.Type, inbound.Transport, host, inbound.Port, nodeLabel, shortID),
		})
	}
	sort.Slice(choices, func(i, j int) bool {
		return choices[i].Label < choices[j].Label
	})
	return choices, nil
}

func runWizardDeleteUserSubscription(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, user domain.User) error {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	sub, err := store.Subscriptions().GetByUserID(cmd.Context(), user.ID)
	if err != nil {
		if strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
			fmt.Fprintln(out, "subscription not found")
			return nil
		}
		return err
	}

	confirm, err := promptBool(in, out, "Delete subscription metadata and files for this user (y/n)", false)
	if err != nil {
		return err
	}
	if !confirm {
		fmt.Fprintln(out, "cancelled")
		return nil
	}

	deletedSub, err := store.Subscriptions().DeleteByUserID(cmd.Context(), user.ID)
	if err != nil {
		return err
	}
	subscriptionDir, err := resolveSubscriptionDir(configPath)
	if err != nil {
		return err
	}
	removedFiles, err := cleanupUserSubscriptionFiles(user.ID, subscriptionDir, sub.OutputPath, sub.AccessToken)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "subscription deleted: metadata=%t files_removed=%d\n", deletedSub, removedFiles)
	return nil
}

func runWizardInboundMenu(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, inbound domain.Inbound) error {
	for {
		action, err := promptChoice(in, out, fmt.Sprintf("Inbound %s (%s/%s %s:%d)", inbound.ID, inbound.Type, inbound.Transport, inbound.Domain, inbound.Port), []string{
			"show users",
			"attach user",
			"back",
		}, "show users")
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fmt.Fprintln(out, "inbound menu cancelled")
				return nil
			}
			return err
		}

		switch action {
		case "show users":
			if err := runWizardShowInboundUsers(cmd, out, dbPath, inbound); err != nil {
				return err
			}
		case "attach user":
			if err := runWizardAttachUserToSpecificInbound(cmd, in, out, configPath, dbPath, inbound); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func runWizardAttachUserToInbound(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, user domain.User) error {
	inbound, ok, err := promptWizardSelectInbound(cmd, in, out, dbPath)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return runWizardAttachExistingUserToInbound(cmd, in, out, configPath, dbPath, user, inbound)
}

func runWizardAttachUserToSpecificInbound(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, inbound domain.Inbound) error {
	user, ok, err := promptWizardSelectUser(cmd, in, out, dbPath)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return runWizardAttachExistingUserToInbound(cmd, in, out, configPath, dbPath, user, inbound)
}

func runWizardAttachExistingUserToInbound(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, user domain.User, inbound domain.Inbound) error {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	existingCredentials, err := store.Credentials().List(cmd.Context())
	if err != nil {
		return err
	}
	if existing, exists := findCredentialByUserAndInbound(existingCredentials, user.ID, inbound.ID); exists {
		node, err := findNodeByID(cmd.Context(), store, inbound.NodeID)
		if err != nil {
			return err
		}
		uri, uriErr := renderSingleClientURI(cmd.Context(), node, inbound, existing)
		fmt.Fprintf(out, "credential already exists: id=%s user=%s inbound=%s kind=%s\n", existing.ID, user.ID, inbound.ID, existing.Kind)
		if uriErr != nil {
			fmt.Fprintf(out, "uri unavailable: %s\n", uriErr)
		} else {
			fmt.Fprintf(out, "client uri: %s\n", uri)
		}
		return nil
	}

	label, err := promptLine(in, out, "Client label (URI name)", user.Name)
	if err != nil {
		return err
	}
	credential, err := createCredentialForInbound(inbound, user.ID, label)
	if err != nil {
		return err
	}
	createdCred, err := store.Credentials().Create(cmd.Context(), credential)
	if err != nil {
		return fmt.Errorf("create credential: %w", err)
	}

	node, err := findNodeByID(cmd.Context(), store, inbound.NodeID)
	if err != nil {
		return err
	}
	uri, uriErr := renderSingleClientURI(cmd.Context(), node, inbound, createdCred)
	fmt.Fprintf(out, "credential created: id=%s user=%s inbound=%s kind=%s\n", createdCred.ID, user.ID, inbound.ID, createdCred.Kind)
	if uriErr != nil {
		fmt.Fprintf(out, "uri unavailable: %s\n", uriErr)
	} else {
		fmt.Fprintf(out, "client uri: %s\n", uri)
	}

	applyNow, err := promptBool(in, out, "Apply runtime changes now (y/n)", true)
	if err != nil {
		return err
	}
	if applyNow {
		if err := runProxyctlSubcommand(cmd, "apply", "--config", configPath, "--db", dbPath); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(out, "credential saved; run `proxyctl apply --config /etc/proxy-orchestrator/proxyctl.yaml` to activate it")
	}
	return nil
}

func findCredentialByUserAndInbound(credentials []domain.Credential, userID, inboundID string) (domain.Credential, bool) {
	userID = strings.TrimSpace(userID)
	inboundID = strings.TrimSpace(inboundID)
	for _, credential := range credentials {
		if strings.TrimSpace(credential.UserID) == userID && strings.TrimSpace(credential.InboundID) == inboundID {
			return credential, true
		}
	}
	return domain.Credential{}, false
}

func credentialLabel(credential domain.Credential) string {
	var metadata struct {
		Label string `json:"label"`
	}
	if strings.TrimSpace(credential.Metadata) != "" {
		_ = json.Unmarshal([]byte(credential.Metadata), &metadata)
	}
	return strings.TrimSpace(metadata.Label)
}

func setCredentialLabelMetadata(existingMetadata, label string) string {
	trimmedLabel := strings.TrimSpace(label)
	if trimmedLabel == "" {
		return strings.TrimSpace(existingMetadata)
	}

	raw := strings.TrimSpace(existingMetadata)
	meta := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &meta)
	}
	meta["label"] = trimmedLabel
	encoded, err := json.Marshal(meta)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func runWizardShowInboundUsers(cmd *cobra.Command, out io.Writer, dbPath string, inbound domain.Inbound) error {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	users, err := store.Users().List(cmd.Context())
	if err != nil {
		return err
	}
	credentials, err := store.Credentials().List(cmd.Context())
	if err != nil {
		return err
	}
	userByID := make(map[string]domain.User, len(users))
	for _, user := range users {
		userByID[user.ID] = user
	}

	found := false
	fmt.Fprintf(out, "inbound: %s (%s/%s %s:%d)\n", inbound.ID, inbound.Type, inbound.Transport, inbound.Domain, inbound.Port)
	for _, cred := range credentials {
		if cred.InboundID != inbound.ID {
			continue
		}
		userName := "<unknown>"
		if user, ok := userByID[cred.UserID]; ok {
			userName = user.Name
		}
		fmt.Fprintf(out, "- user=%s (%s) credential=%s kind=%s\n", cred.UserID, userName, cred.ID, cred.Kind)
		found = true
	}
	if !found {
		fmt.Fprintln(out, "no users attached")
	}
	return nil
}

type wizardUserConfigKind string

const (
	wizardUserConfigCredential   wizardUserConfigKind = "credential"
	wizardUserConfigSubscription wizardUserConfigKind = "subscription"
)

type wizardUserConfigItem struct {
	Kind               wizardUserConfigKind
	CredentialID       string
	InboundID          string
	InboundSummary     string
	InboundType        domain.Protocol
	InboundTransport   string
	InboundDomain      string
	InboundPort        int
	NodeID             string
	NodeHost           string
	CredentialKind     domain.CredentialKind
	CredentialSecret   string
	CredentialMetadata string
	SecretPreview      string
	ClientURI          string
	ClientURIError     string
	SubscriptionOutput string
	SubscriptionToken  string
	SubscriptionExists bool
}

func runWizardShowUserConfigs(cmd *cobra.Command, out io.Writer, dbPath string, user domain.User) error {
	items, err := listWizardUserConfigs(cmd, dbPath, user.ID)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "user: %s (%s)\n", user.Name, user.ID)
	if len(items) == 0 {
		fmt.Fprintln(out, "configs: none")
		return nil
	}
	for _, item := range items {
		switch item.Kind {
		case wizardUserConfigCredential:
			fmt.Fprintf(out, "- credential: id=%s inbound=%s kind=%s secret=%s\n", item.CredentialID, item.InboundSummary, item.CredentialKind, item.SecretPreview)
			if strings.TrimSpace(item.ClientURI) != "" {
				fmt.Fprintf(out, "  uri: %s\n", item.ClientURI)
			} else if strings.TrimSpace(item.ClientURIError) != "" {
				fmt.Fprintf(out, "  uri: <unavailable> (%s)\n", item.ClientURIError)
			}
		case wizardUserConfigSubscription:
			fmt.Fprintf(out, "- subscription: output_path=%s file_exists=%t\n", item.SubscriptionOutput, item.SubscriptionExists)
		}
	}
	return nil
}

func runWizardOpenCredential(cmd *cobra.Command, in *bufio.Reader, out io.Writer, dbPath string, user domain.User) error {
	items, err := listWizardUserConfigs(cmd, dbPath, user.ID)
	if err != nil {
		return err
	}

	credentials := make([]wizardUserConfigItem, 0)
	for _, item := range items {
		if item.Kind == wizardUserConfigCredential {
			credentials = append(credentials, item)
		}
	}
	if len(credentials) == 0 {
		fmt.Fprintln(out, "no credentials found")
		return nil
	}

	options := make([]string, 0, len(credentials))
	byOption := make(map[string]wizardUserConfigItem, len(credentials))
	for _, item := range credentials {
		option := fmt.Sprintf("%s (%s on %s)", item.CredentialID, item.CredentialKind, item.InboundSummary)
		options = append(options, option)
		byOption[option] = item
	}

	choice, err := promptChoice(in, out, "Credential", options, options[0])
	if err != nil {
		return err
	}
	selected := byOption[choice]

	for {
		action, err := promptChoice(in, out, fmt.Sprintf("Credential %s", selected.CredentialID), []string{
			"show details",
			"print URI",
			"print URI with fingerprint",
			"set client label",
			"show full secret",
			"delete credential",
			"back",
		}, "show details")
		if err != nil {
			return err
		}

		switch action {
		case "show details":
			fmt.Fprintf(out, "id: %s\n", selected.CredentialID)
			fmt.Fprintf(out, "kind: %s\n", selected.CredentialKind)
			fmt.Fprintf(out, "inbound_id: %s\n", selected.InboundID)
			fmt.Fprintf(out, "protocol: %s\n", selected.InboundType)
			fmt.Fprintf(out, "transport: %s\n", selected.InboundTransport)
			fmt.Fprintf(out, "domain: %s\n", selected.InboundDomain)
			fmt.Fprintf(out, "port: %d\n", selected.InboundPort)
			fmt.Fprintf(out, "node_id: %s\n", selected.NodeID)
			fmt.Fprintf(out, "node_host: %s\n", selected.NodeHost)
			if label := credentialLabel(domain.Credential{Metadata: selected.CredentialMetadata}); label != "" {
				fmt.Fprintf(out, "label: %s\n", label)
			}
			fmt.Fprintf(out, "secret(masked): %s\n", selected.SecretPreview)
			if strings.TrimSpace(selected.ClientURI) != "" {
				fmt.Fprintf(out, "uri: %s\n", selected.ClientURI)
			} else {
				fmt.Fprintf(out, "uri: <unavailable> (%s)\n", selected.ClientURIError)
			}
		case "print URI":
			if strings.TrimSpace(selected.ClientURI) == "" {
				fmt.Fprintf(out, "uri unavailable: %s\n", selected.ClientURIError)
			} else {
				fmt.Fprintln(out, selected.ClientURI)
			}
		case "print URI with fingerprint":
			if strings.TrimSpace(selected.ClientURI) == "" {
				fmt.Fprintf(out, "uri unavailable: %s\n", selected.ClientURIError)
				continue
			}
			fingerprinted, err := buildURIWithFingerprint(in, out, selected.ClientURI)
			if err != nil {
				return err
			}
			if strings.TrimSpace(fingerprinted) != "" {
				fmt.Fprintln(out, fingerprinted)
			}
		case "set client label":
			newLabel, err := promptLine(in, out, "Client label", credentialLabel(domain.Credential{Metadata: selected.CredentialMetadata}))
			if err != nil {
				return err
			}
			store, err := openStoreWithInit(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			updatedCred, err := store.Credentials().Update(cmd.Context(), domain.Credential{
				ID:        selected.CredentialID,
				UserID:    user.ID,
				InboundID: selected.InboundID,
				Kind:      selected.CredentialKind,
				Secret:    selected.CredentialSecret,
				Metadata:  setCredentialLabelMetadata(selected.CredentialMetadata, newLabel),
			})
			closeErr := store.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			selected.CredentialMetadata = updatedCred.Metadata
			items, listErr := listWizardUserConfigs(cmd, dbPath, user.ID)
			if listErr == nil {
				for _, item := range items {
					if item.Kind == wizardUserConfigCredential && item.CredentialID == selected.CredentialID {
						selected = item
						break
					}
				}
			}
			fmt.Fprintf(out, "label updated: %s\n", strings.TrimSpace(newLabel))
		case "show full secret":
			confirm, err := promptBool(in, out, "Print full secret to terminal (y/n)", false)
			if err != nil {
				return err
			}
			if confirm {
				fmt.Fprintf(out, "secret: %s\n", selected.CredentialSecret)
			} else {
				fmt.Fprintln(out, "cancelled")
			}
		case "delete credential":
			confirm, err := promptBool(in, out, "Delete this credential (y/n)", false)
			if err != nil {
				return err
			}
			if !confirm {
				fmt.Fprintln(out, "cancelled")
				continue
			}

			store, err := openStoreWithInit(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			deleted, err := store.Credentials().Delete(cmd.Context(), selected.CredentialID)
			closeErr := store.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}

			fmt.Fprintf(out, "credential deleted: id=%s deleted=%t\n", selected.CredentialID, deleted)
			return nil
		default:
			return nil
		}
	}
}

func runWizardDeleteSpecificUserConfig(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, user domain.User) error {
	items, err := listWizardUserConfigs(cmd, dbPath, user.ID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(out, "no configs to delete")
		return nil
	}

	options := make([]string, 0, len(items))
	byOption := make(map[string]wizardUserConfigItem, len(items))
	for _, item := range items {
		var option string
		if item.Kind == wizardUserConfigCredential {
			option = fmt.Sprintf("credential %s (%s on %s)", item.CredentialID, item.CredentialKind, item.InboundSummary)
		} else {
			option = fmt.Sprintf("subscription (%s)", item.SubscriptionOutput)
		}
		options = append(options, option)
		byOption[option] = item
	}

	choice, err := promptChoice(in, out, "Config to delete", options, options[0])
	if err != nil {
		return err
	}
	selected := byOption[choice]
	confirm, err := promptBool(in, out, "Confirm delete selected config (y/n)", false)
	if err != nil {
		return err
	}
	if !confirm {
		fmt.Fprintln(out, "cancelled")
		return nil
	}

	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	switch selected.Kind {
	case wizardUserConfigCredential:
		deleted, err := store.Credentials().Delete(cmd.Context(), selected.CredentialID)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "credential deleted: id=%s deleted=%t\n", selected.CredentialID, deleted)
	case wizardUserConfigSubscription:
		deletedSub, err := store.Subscriptions().DeleteByUserID(cmd.Context(), user.ID)
		if err != nil {
			return err
		}
		subscriptionDir, err := resolveSubscriptionDir(configPath)
		if err != nil {
			return err
		}
		removedFiles, err := cleanupUserSubscriptionFiles(user.ID, subscriptionDir, selected.SubscriptionOutput, selected.SubscriptionToken)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "subscription deleted: metadata=%t files_removed=%d\n", deletedSub, removedFiles)
	default:
		return fmt.Errorf("unsupported config kind: %s", selected.Kind)
	}
	return nil
}

func runWizardDeleteUserCompletely(cmd *cobra.Command, in *bufio.Reader, out io.Writer, configPath, dbPath string, user domain.User) (bool, error) {
	confirm, err := promptBool(in, out, "Delete user completely (credentials, subscription metadata/files, user record) (y/n)", false)
	if err != nil {
		return false, err
	}
	if !confirm {
		fmt.Fprintln(out, "cancelled")
		return false, nil
	}

	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return false, err
	}
	defer store.Close()

	subPath := ""
	subToken := ""
	sub, subErr := store.Subscriptions().GetByUserID(cmd.Context(), user.ID)
	if subErr == nil {
		subPath = strings.TrimSpace(sub.OutputPath)
		subToken = strings.TrimSpace(sub.AccessToken)
	}

	deletedCreds, err := store.Credentials().DeleteByUserID(cmd.Context(), user.ID)
	if err != nil {
		return false, err
	}
	deletedSub, err := store.Subscriptions().DeleteByUserID(cmd.Context(), user.ID)
	if err != nil {
		return false, err
	}
	deletedUser, err := store.Users().Delete(cmd.Context(), user.ID)
	if err != nil {
		return false, err
	}
	if !deletedUser {
		fmt.Fprintf(out, "user not found: %s\n", user.ID)
		return false, nil
	}

	subscriptionDir, err := resolveSubscriptionDir(configPath)
	if err != nil {
		return false, err
	}
	removedFiles, err := cleanupUserSubscriptionFiles(user.ID, subscriptionDir, subPath, subToken)
	if err != nil {
		return false, err
	}

	fmt.Fprintf(out, "user fully deleted: id=%s name=%s\n", user.ID, user.Name)
	fmt.Fprintf(out, "deleted credentials: %d\n", deletedCreds)
	fmt.Fprintf(out, "deleted subscription metadata: %t\n", deletedSub)
	fmt.Fprintf(out, "removed subscription files: %d\n", removedFiles)
	return true, nil
}

func listWizardUserConfigs(cmd *cobra.Command, dbPath, userID string) ([]wizardUserConfigItem, error) {
	store, err := openStoreWithInit(cmd.Context(), dbPath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	credentials, err := store.Credentials().List(cmd.Context())
	if err != nil {
		return nil, err
	}
	inbounds, err := store.Inbounds().List(cmd.Context())
	if err != nil {
		return nil, err
	}
	nodes, err := store.Nodes().List(cmd.Context())
	if err != nil {
		return nil, err
	}
	nodeByID := make(map[string]domain.Node, len(nodes))
	for _, node := range nodes {
		nodeByID[node.ID] = node
	}
	inboundByID := make(map[string]domain.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}

	items := make([]wizardUserConfigItem, 0)
	for _, cred := range credentials {
		if cred.UserID != userID {
			continue
		}
		summary := cred.InboundID
		var (
			inboundType      domain.Protocol
			inboundTransport string
			inboundDomain    string
			inboundPort      int
			nodeID           string
			nodeHost         string
		)
		if inbound, ok := inboundByID[cred.InboundID]; ok {
			host := strings.TrimSpace(inbound.Domain)
			if host == "" {
				host = "<no-domain>"
			}
			summary = fmt.Sprintf("%s/%s %s:%d", inbound.Type, inbound.Transport, host, inbound.Port)
			inboundType = inbound.Type
			inboundTransport = inbound.Transport
			inboundDomain = inbound.Domain
			inboundPort = inbound.Port
			nodeID = inbound.NodeID
			if node, hasNode := nodeByID[inbound.NodeID]; hasNode {
				nodeHost = node.Host
			}
		}
		clientURI := ""
		clientURIError := ""
		if inbound, ok := inboundByID[cred.InboundID]; ok {
			node, hasNode := nodeByID[inbound.NodeID]
			if hasNode {
				uri, uriErr := renderSingleClientURI(cmd.Context(), node, inbound, cred)
				if uriErr != nil {
					clientURIError = uriErr.Error()
				} else {
					clientURI = uri
				}
			} else {
				clientURIError = fmt.Sprintf("node %q not found", inbound.NodeID)
			}
		} else {
			clientURIError = fmt.Sprintf("inbound %q not found", cred.InboundID)
		}
		items = append(items, wizardUserConfigItem{
			Kind:               wizardUserConfigCredential,
			CredentialID:       cred.ID,
			InboundID:          cred.InboundID,
			InboundSummary:     summary,
			InboundType:        inboundType,
			InboundTransport:   inboundTransport,
			InboundDomain:      inboundDomain,
			InboundPort:        inboundPort,
			NodeID:             nodeID,
			NodeHost:           nodeHost,
			CredentialKind:     cred.Kind,
			CredentialSecret:   cred.Secret,
			CredentialMetadata: cred.Metadata,
			SecretPreview:      redactSecretPreview(cred.Secret),
			ClientURI:          clientURI,
			ClientURIError:     clientURIError,
		})
	}

	sub, err := store.Subscriptions().GetByUserID(cmd.Context(), userID)
	if err == nil {
		outputPath := strings.TrimSpace(sub.OutputPath)
		_, statErr := os.Stat(outputPath)
		items = append(items, wizardUserConfigItem{
			Kind:               wizardUserConfigSubscription,
			SubscriptionOutput: outputPath,
			SubscriptionToken:  strings.TrimSpace(sub.AccessToken),
			SubscriptionExists: statErr == nil,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].CredentialID < items[j].CredentialID
	})
	return items, nil
}

func redactSecretPreview(secret string) string {
	value := strings.TrimSpace(secret)
	if value == "" {
		return "<empty>"
	}
	if len(value) <= 8 {
		return value
	}
	return value[:4] + "..." + value[len(value)-4:]
}

func buildURIWithFingerprint(in *bufio.Reader, out io.Writer, rawURI string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return "", fmt.Errorf("parse client uri: %w", err)
	}

	if strings.ToLower(u.Scheme) != "vless" {
		fmt.Fprintln(out, "fingerprint preset is supported only for vless:// URIs")
		return "", nil
	}

	query := u.Query()
	if strings.ToLower(strings.TrimSpace(query.Get("security"))) != "reality" {
		fmt.Fprintln(out, "fingerprint is usually relevant for Reality links (security=reality); applying anyway")
	}

	preset, err := promptChoice(in, out, "Fingerprint preset", []string{
		"chrome (google)",
		"safari",
		"firefox",
		"edge",
		"ios",
		"android",
		"custom",
	}, "chrome (google)")
	if err != nil {
		return "", err
	}

	fp := ""
	switch preset {
	case "chrome (google)":
		fp = "chrome"
	case "safari":
		fp = "safari"
	case "firefox":
		fp = "firefox"
	case "edge":
		fp = "edge"
	case "ios":
		fp = "ios"
	case "android":
		fp = "android"
	default:
		custom, err := promptLineRequired(in, out, "Custom fingerprint")
		if err != nil {
			return "", err
		}
		fp = strings.TrimSpace(custom)
	}

	query.Set("fp", fp)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func promptUserChoice(in *bufio.Reader, out io.Writer, users []domain.User, label string) (string, map[string]domain.User, error) {
	options := make([]string, 0, len(users))
	userByChoice := make(map[string]domain.User, len(users))
	for _, user := range users {
		state := "enabled"
		if !user.Enabled {
			state = "disabled"
		}
		item := fmt.Sprintf("%s (%s, %s)", user.ID, user.Name, state)
		options = append(options, item)
		userByChoice[item] = user
	}
	choice, err := promptChoice(in, out, label, options, options[0])
	if err != nil {
		return "", nil, err
	}
	return choice, userByChoice, nil
}

func resolveSubscriptionDir(configPath string) (string, error) {
	cfg, err := loadAppConfig(configPath)
	if err != nil {
		return config.DefaultAppConfig().Paths.Subscription, err
	}
	return cfg.Paths.Subscription, nil
}

func loadAppConfig(configPath string) (config.AppConfig, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.DefaultAppConfig(), nil
	}
	return cfg, nil
}

func subscriptionPublicDir(subscriptionDir string) string {
	return filepath.Join(strings.TrimSpace(subscriptionDir), "public")
}

func buildSubscriptionPublicURL(cfg config.AppConfig, accessToken string) string {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return ""
	}
	domain := strings.TrimSpace(cfg.Public.Domain)
	if domain == "" {
		return ""
	}
	scheme := "https"
	if !cfg.Public.HTTPS {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/sub/%s", scheme, domain, token)
}

func cleanupUserSubscriptionFiles(userID, subscriptionDir, storedOutputPath, accessToken string) (int, error) {
	paths := []string{
		filepath.Join(subscriptionDir, userID+".txt"),
		filepath.Join(subscriptionDir, userID+".base64"),
		filepath.Join(subscriptionDir, userID+".json"),
	}
	publicDir := subscriptionPublicDir(subscriptionDir)
	token := strings.TrimSpace(accessToken)
	if token != "" {
		paths = append(paths,
			filepath.Join(publicDir, token),
			filepath.Join(publicDir, token+".txt"),
		)
	}
	if strings.TrimSpace(storedOutputPath) != "" {
		paths = append(paths, strings.TrimSpace(storedOutputPath))
	}
	unique := compactUnique(paths)

	removed := 0
	for _, p := range unique {
		err := os.Remove(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return removed, fmt.Errorf("remove subscription file %q: %w", p, err)
		}
		removed++
	}
	return removed, nil
}

func boolToEnv(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func parseCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func parseIndexCSV(raw string, max int) ([]int, error) {
	values := parseCSV(raw)
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		idx, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("invalid index %q", value)
		}
		if idx < 1 || idx > max {
			return nil, fmt.Errorf("index %d out of range 1..%d", idx, max)
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}
	sort.Ints(out)
	return out, nil
}

func wizardNormalizeProfileName(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' {
			b.WriteRune(ch)
			lastDash = false
			continue
		}
		if ch == '-' || ch == ' ' {
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func shellQuote(s string) string {
	// Wraps arbitrary input into a single-quoted shell literal.
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

type latestReleaseResponse struct {
	TagName string `json:"tag_name"`
}

func fetchLatestReleaseTag(ctx context.Context, apiURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("build latest release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "proxyctl-updater")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("query latest release API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("latest release API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload latestReleaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode latest release API response: %w", err)
	}
	tag := strings.TrimSpace(payload.TagName)
	if tag == "" {
		return "", fmt.Errorf("latest release API response does not contain tag_name")
	}
	return tag, nil
}

type semVersion struct {
	major int
	minor int
	patch int
	pre   string
}

func parseSemVersion(raw string) (semVersion, error) {
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return semVersion{}, fmt.Errorf("version is empty")
	}

	mainPart := v
	pre := ""
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		mainPart = v[:idx]
		pre = v[idx+1:]
	}

	parts := strings.Split(mainPart, ".")
	if len(parts) != 3 {
		return semVersion{}, fmt.Errorf("expected MAJOR.MINOR.PATCH, got %q", raw)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return semVersion{}, fmt.Errorf("parse major: %w", err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return semVersion{}, fmt.Errorf("parse minor: %w", err)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return semVersion{}, fmt.Errorf("parse patch: %w", err)
	}

	return semVersion{
		major: major,
		minor: minor,
		patch: patch,
		pre:   pre,
	}, nil
}

func compareSemVersion(a, b semVersion) int {
	if a.major != b.major {
		if a.major > b.major {
			return 1
		}
		return -1
	}
	if a.minor != b.minor {
		if a.minor > b.minor {
			return 1
		}
		return -1
	}
	if a.patch != b.patch {
		if a.patch > b.patch {
			return 1
		}
		return -1
	}

	// Stable release (no prerelease suffix) is newer than prerelease.
	if a.pre == "" && b.pre != "" {
		return 1
	}
	if a.pre != "" && b.pre == "" {
		return -1
	}
	return strings.Compare(a.pre, b.pre)
}

func findNodeByID(ctx context.Context, store *sqlite.Store, nodeID string) (domain.Node, error) {
	nodes, err := store.Nodes().List(ctx)
	if err != nil {
		return domain.Node{}, fmt.Errorf("list nodes: %w", err)
	}
	for _, node := range nodes {
		if node.ID == nodeID {
			return node, nil
		}
	}
	return domain.Node{}, fmt.Errorf("node %q not found", nodeID)
}

func findUserByID(ctx context.Context, store *sqlite.Store, userID string) (domain.User, error) {
	users, err := store.Users().List(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("list users: %w", err)
	}
	for _, user := range users {
		if user.ID == userID {
			return user, nil
		}
	}
	return domain.User{}, fmt.Errorf("user %q not found", userID)
}

func createCredentialForInbound(inbound domain.Inbound, userID, label string) (domain.Credential, error) {
	kind := domain.CredentialKindUUID
	secret := ""
	switch inbound.Type {
	case domain.ProtocolVLESS, domain.ProtocolXHTTP:
		uuid, err := randomUUIDv4()
		if err != nil {
			return domain.Credential{}, fmt.Errorf("generate UUID credential: %w", err)
		}
		secret = uuid
	case domain.ProtocolHysteria2:
		kind = domain.CredentialKindPassword
		password, err := randomHex(16)
		if err != nil {
			return domain.Credential{}, fmt.Errorf("generate password credential: %w", err)
		}
		secret = password
	default:
		return domain.Credential{}, fmt.Errorf("unsupported inbound protocol for auto credential: %s", inbound.Type)
	}

	metadata := setCredentialLabelMetadata("", label)
	return domain.Credential{
		UserID:    strings.TrimSpace(userID),
		InboundID: inbound.ID,
		Kind:      kind,
		Secret:    secret,
		Metadata:  metadata,
	}, nil
}

func renderSingleClientURI(ctx context.Context, node domain.Node, inbound domain.Inbound, credential domain.Credential) (string, error) {
	req := renderer.BuildRequest{
		Node:        node,
		Inbounds:    []domain.Inbound{inbound},
		Credentials: []domain.Credential{credential},
	}

	var (
		result renderer.RenderResult
		err    error
	)
	if inbound.Engine == domain.EngineXray {
		result, err = xray.New(nil).Render(ctx, req)
		if err != nil {
			return "", fmt.Errorf("render xray client URI: %w", err)
		}
	} else {
		result, err = singbox.New(nil).Render(ctx, req)
		if err != nil {
			return "", fmt.Errorf("render sing-box client URI: %w", err)
		}
	}

	for _, item := range result.ClientArtifacts {
		if item.CredentialID == credential.ID && strings.TrimSpace(item.URI) != "" {
			return item.URI, nil
		}
	}
	for _, item := range result.ClientArtifacts {
		if strings.TrimSpace(item.URI) != "" {
			return item.URI, nil
		}
	}
	return "", fmt.Errorf("renderer did not produce client URI")
}

func randomUUIDv4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		b[0:4],
		b[4:6],
		b[6:8],
		b[8:10],
		b[10:16],
	), nil
}

func randomHex(bytes int) (string, error) {
	if bytes <= 0 {
		return "", fmt.Errorf("bytes must be positive")
	}
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func wizardPortNetwork(protocol, transport string) string {
	if strings.EqualFold(strings.TrimSpace(protocol), "hysteria2") || strings.EqualFold(strings.TrimSpace(transport), "udp") {
		return "udp"
	}
	return "tcp"
}

func wizardPortCandidates(protocol, transport string) []int {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "hysteria2":
		return []int{8444, 9444, 10444, 18444}
	case "xhttp":
		return []int{9443, 10443, 8443, 18443}
	case "vless":
		switch strings.ToLower(strings.TrimSpace(transport)) {
		case "grpc":
			return []int{9443, 10443, 8443, 18443}
		case "ws":
			return []int{8443, 9443, 10443, 18443}
		default:
			return []int{8443, 9443, 10443, 18443}
		}
	default:
		return []int{8443, 9443, 10443, 18443}
	}
}

func suggestWizardPort(protocol, transport string, usedPorts map[int]struct{}, busyFn func(network string, port int) bool) int {
	candidates := wizardPortCandidates(protocol, transport)
	network := wizardPortNetwork(protocol, transport)
	for _, candidate := range candidates {
		if !isWizardPortBusy(candidate, usedPorts, network, busyFn) {
			return candidate
		}
	}
	return candidates[0]
}

func isWizardPortBusy(port int, usedPorts map[int]struct{}, network string, busyFn func(network string, port int) bool) bool {
	if _, exists := usedPorts[port]; exists {
		return true
	}
	if busyFn != nil && busyFn(network, port) {
		return true
	}
	return false
}

func hostPortBusy(network string, port int) bool {
	addr := fmt.Sprintf(":%d", port)
	if strings.EqualFold(strings.TrimSpace(network), "udp") {
		conn, err := net.ListenPacket("udp", addr)
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func generateRealityKeyPair() (publicKey, privateKey string, err error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	publicKey = base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
	privateKey = base64.RawURLEncoding.EncodeToString(private.Bytes())
	return publicKey, privateKey, nil
}

func promptRealityFingerprint(in *bufio.Reader, out io.Writer, defaultValue string) (string, error) {
	preset, err := promptChoice(in, out, "Reality fingerprint", []string{
		"chrome (google)",
		"safari",
		"firefox",
		"edge",
		"ios",
		"android",
		"custom",
	}, "chrome (google)")
	if err != nil {
		return "", err
	}
	switch preset {
	case "chrome (google)":
		return "chrome", nil
	case "safari":
		return "safari", nil
	case "firefox":
		return "firefox", nil
	case "edge":
		return "edge", nil
	case "ios":
		return "ios", nil
	case "android":
		return "android", nil
	case "custom":
		value, err := promptLine(in, out, "Custom reality fingerprint", defaultValue)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(value), nil
	default:
		return defaultValue, nil
	}
}

func promptRealityServer(in *bufio.Reader, out io.Writer) (string, error) {
	preset, err := promptChoice(in, out, "Reality server (dest host)", []string{
		"www.cloudflare.com",
		"www.google.com",
		"www.apple.com",
		"www.microsoft.com",
		"custom",
	}, "www.cloudflare.com")
	if err != nil {
		return "", err
	}
	if preset != "custom" {
		return strings.TrimSpace(preset), nil
	}
	return promptLineRequired(in, out, "Custom reality server (dest host)")
}

func newInboundListCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List inbounds",
		Long:  "Lists configured inbound listener profiles.",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			inbounds, err := store.Inbounds().List(cmd.Context())
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tENGINE\tNODE_ID\tDOMAIN\tPORT\tTLS\tREALITY\tTRANSPORT\tPATH\tSNI\tFLOW\tENABLED\tCREATED_AT")
			for _, inbound := range inbounds {
				fmt.Fprintf(
					w,
					"%s\t%s\t%s\t%s\t%s\t%d\t%t\t%t\t%s\t%s\t%s\t%s\t%t\t%s\n",
					inbound.ID,
					inbound.Type,
					inbound.Engine,
					inbound.NodeID,
					inbound.Domain,
					inbound.Port,
					inbound.TLSEnabled,
					inbound.RealityEnabled,
					inbound.Transport,
					inbound.Path,
					inbound.SNI,
					inbound.VLESSFlow,
					inbound.Enabled,
					inbound.CreatedAt.Format(time.RFC3339),
				)
			}
			return w.Flush()
		},
	}
}

func newRenderCmd(configPath, dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "render",
		Short: "Render runtime configs",
		Long:  "Renders sing-box/Xray runtime files and subscription files into runtime layout directories without apply/restart.",
		RunE: func(cmd *cobra.Command, args []string) error {
			appCfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			store, err := openStoreWithInit(cmd.Context(), resolveDBPath(cmd, appCfg, *dbPath))
			if err != nil {
				return err
			}
			defer store.Close()

			layoutManager := layout.New(layout.Directories{
				ConfigDir:        appCfg.Paths.ConfigDir,
				RuntimeDir:       appCfg.Paths.RuntimeDir,
				CaddyDir:         appCfg.Paths.CaddyDir,
				NginxDir:         appCfg.Paths.NginxDir,
				DecoySiteDir:     appCfg.Paths.DecoySiteDir,
				StateDir:         appCfg.Paths.StateDir,
				SubscriptionsDir: appCfg.Paths.Subscription,
				BackupsDir:       appCfg.Paths.BackupsDir,
			})
			if err := layoutManager.EnsureDirectories(); err != nil {
				return err
			}

			req, err := buildRenderRequest(cmd.Context(), store)
			if err != nil {
				return err
			}

			singResult, err := singbox.New(nil).Render(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("render sing-box config: %w", err)
			}
			xrayResult, err := xray.New(nil).Render(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("render xray config: %w", err)
			}

			singWrite, err := layoutManager.WriteRenderedSingBoxConfig(selectPreviewContent(singResult))
			if err != nil {
				return err
			}
			xrayWrite, err := layoutManager.WriteRenderedXrayConfig(selectPreviewContent(xrayResult))
			if err != nil {
				return err
			}

			proxyEngine, proxyWrite, decoyWritten, err := renderReverseProxyAndDecoy(layoutManager, appCfg, req, false)
			if err != nil {
				return err
			}

			subscriptions, err := renderSubscriptions(cmd.Context(), store, appCfg.Paths.Subscription, "")
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "sing-box config: %s\n", singWrite.Path)
			if singWrite.BackupPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "sing-box backup: %s\n", singWrite.BackupPath)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "xray config: %s\n", xrayWrite.Path)
			if xrayWrite.BackupPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "xray backup: %s\n", xrayWrite.BackupPath)
			}
			if proxyWrite != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s config: %s\n", proxyEngine, proxyWrite)
			}
			if decoyWritten > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "decoy assets: %d files\n", decoyWritten)
			}
			for _, line := range subscriptions {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
}

func newPreviewCmd(configPath, dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "preview",
		Short: "Render preview files",
		Long:  "Renders preview config/subscription files into runtime layout directories without apply/restart and without mutating stored subscription metadata.",
		RunE: func(cmd *cobra.Command, args []string) error {
			appCfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			store, err := openStoreWithInit(cmd.Context(), resolveDBPath(cmd, appCfg, *dbPath))
			if err != nil {
				return err
			}
			defer store.Close()

			layoutManager := layout.New(layout.Directories{
				ConfigDir:        appCfg.Paths.ConfigDir,
				RuntimeDir:       appCfg.Paths.RuntimeDir,
				CaddyDir:         appCfg.Paths.CaddyDir,
				NginxDir:         appCfg.Paths.NginxDir,
				DecoySiteDir:     appCfg.Paths.DecoySiteDir,
				StateDir:         appCfg.Paths.StateDir,
				SubscriptionsDir: appCfg.Paths.Subscription,
				BackupsDir:       appCfg.Paths.BackupsDir,
			})
			if err := layoutManager.EnsureDirectories(); err != nil {
				return err
			}

			req, err := buildRenderRequest(cmd.Context(), store)
			if err != nil {
				return err
			}

			singResult, err := singbox.New(nil).Render(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("render sing-box preview: %w", err)
			}
			xrayResult, err := xray.New(nil).Render(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("render xray preview: %w", err)
			}

			singPreviewPath := filepath.Join(appCfg.Paths.RuntimeDir, "sing-box.preview.json")
			xrayPreviewPath := filepath.Join(appCfg.Paths.RuntimeDir, "xray.preview.json")
			if err := layout.WriteAtomicFile(singPreviewPath, selectPreviewContent(singResult), 0o640); err != nil {
				return fmt.Errorf("write sing-box preview: %w", err)
			}
			if err := layout.WriteAtomicFile(xrayPreviewPath, selectPreviewContent(xrayResult), 0o640); err != nil {
				return fmt.Errorf("write xray preview: %w", err)
			}

			proxyEngine, proxyPreviewPath, decoyWritten, err := renderReverseProxyAndDecoy(layoutManager, appCfg, req, true)
			if err != nil {
				return err
			}

			subscriptions, err := renderSubscriptions(cmd.Context(), store, appCfg.Paths.Subscription, "preview")
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "sing-box preview: %s\n", singPreviewPath)
			fmt.Fprintf(cmd.OutOrStdout(), "xray preview: %s\n", xrayPreviewPath)
			if proxyPreviewPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s preview: %s\n", proxyEngine, proxyPreviewPath)
			}
			if decoyWritten > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "decoy assets: %d files\n", decoyWritten)
			}
			for _, line := range subscriptions {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
}

func newValidateCmd(configPath, dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate candidate runtime configuration",
		Long:  "Builds runtime configuration artifacts and runs validation hooks without writing runtime files or restarting services.",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := runApplyPipeline(cmd, *configPath, *dbPath, true)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "validated artifacts: %s\n", strings.Join(result.Validated, ", "))
			if len(result.ServiceOps) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "planned service operations:")
				for _, op := range result.ServiceOps {
					fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s\n", op.Action, op.Unit)
				}
			}
			return nil
		},
	}
}

func newApplyCmd(configPath, dbPath *string) *cobra.Command {
	dryRun := false

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply runtime configuration safely",
		Long:  "Runs validate -> apply -> restart with automatic rollback on restart failures.",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := runApplyPipeline(cmd, *configPath, *dbPath, dryRun)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "built artifacts: %s\n", strings.Join(result.ArtifactsBuilt, ", "))
			fmt.Fprintf(cmd.OutOrStdout(), "validated artifacts: %s\n", strings.Join(result.Validated, ", "))
			if result.DryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "dry-run: runtime files and services were not changed")
				return nil
			}

			for _, write := range result.Writes {
				fmt.Fprintf(cmd.OutOrStdout(), "runtime file: %s\n", write.Path)
				if write.BackupPath != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "backup: %s\n", write.BackupPath)
				}
			}
			for _, op := range result.ServiceOps {
				fmt.Fprintf(cmd.OutOrStdout(), "service %s: %s\n", op.Action, op.Unit)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Run validate pipeline only, without writing runtime files or restarting services")
	return cmd
}

func newSubscriptionCmd(configPath, dbPath *string) *cobra.Command {
	cmd := newGroupCmd(
		"subscription",
		"Manage subscription outputs",
		"Builds and inspects subscription payloads for client applications.",
	)
	cmd.AddCommand(
		newSubscriptionGenerateCmd(configPath, dbPath),
		newSubscriptionShowCmd(configPath, dbPath),
		newSubscriptionExportCmd(configPath, dbPath),
	)
	return cmd
}

func newSubscriptionGenerateCmd(configPath, dbPath *string) *cobra.Command {
	profile := subscriptionservice.DefaultProfileName
	inboundsCSV := ""

	cmd := &cobra.Command{
		Use:   "generate <user>",
		Short: "Generate subscription payload files for a user",
		Long:  "Collects client artifacts from sing-box and Xray renderers and stores txt/base64/json subscription files.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appCfg, err := loadAppConfig(*configPath)
			if err != nil {
				return err
			}
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			svc := subscriptionservice.New(
				store,
				appCfg.Paths.Subscription,
				subscriptionPublicDir(appCfg.Paths.Subscription),
				singbox.New(nil),
				xray.New(nil),
			)

			generated, err := svc.GenerateProfile(cmd.Context(), args[0], profile, parseCSV(inboundsCSV))
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "generated subscription for user=%s (%s) profile=%s\n", generated.User.Name, generated.User.ID, generated.ProfileName)
			fmt.Fprintf(cmd.OutOrStdout(), "txt: %s\n", generated.TXTPath)
			fmt.Fprintf(cmd.OutOrStdout(), "base64: %s\n", generated.Base64Path)
			fmt.Fprintf(cmd.OutOrStdout(), "json: %s\n", generated.JSONPath)
			if link := buildSubscriptionPublicURL(appCfg, generated.AccessToken); link != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "url: %s\n", link)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", subscriptionservice.DefaultProfileName, "Subscription profile name")
	cmd.Flags().StringVar(&inboundsCSV, "inbounds", "", "Comma-separated inbound IDs for named profile creation/update")
	return cmd
}

func newSubscriptionShowCmd(configPath, dbPath *string) *cobra.Command {
	profile := subscriptionservice.DefaultProfileName

	cmd := &cobra.Command{
		Use:   "show <user>",
		Short: "Show last generated subscription output for a user",
		Long:  "Reads the last generated subscription metadata and prints the stored payload.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appCfg, err := loadAppConfig(*configPath)
			if err != nil {
				return err
			}
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			svc := subscriptionservice.New(
				store,
				appCfg.Paths.Subscription,
				subscriptionPublicDir(appCfg.Paths.Subscription),
				singbox.New(nil),
				xray.New(nil),
			)
			result, err := svc.ShowProfile(cmd.Context(), args[0], profile)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "user=%s (%s)\n", result.User.Name, result.User.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "profile=%s\n", result.ProfileName)
			fmt.Fprintf(cmd.OutOrStdout(), "format=%s\n", result.Format)
			fmt.Fprintf(cmd.OutOrStdout(), "path=%s\n", result.Path)
			if link := buildSubscriptionPublicURL(appCfg, result.AccessToken); link != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "url=%s\n", link)
			}
			if len(result.Content) > 0 {
				if _, err := cmd.OutOrStdout().Write(result.Content); err != nil {
					return err
				}
				if result.Content[len(result.Content)-1] != '\n' {
					fmt.Fprintln(cmd.OutOrStdout())
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", subscriptionservice.DefaultProfileName, "Subscription profile name")
	return cmd
}

func newSubscriptionExportCmd(configPath, dbPath *string) *cobra.Command {
	format := subscriptionservice.FormatJSON
	profile := subscriptionservice.DefaultProfileName
	inboundsCSV := ""

	cmd := &cobra.Command{
		Use:   "export <user>",
		Short: "Generate and print subscription in the selected format",
		Long:  "Regenerates subscription files and prints one selected output format for automation workflows.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appCfg, err := loadAppConfig(*configPath)
			if err != nil {
				return err
			}
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			svc := subscriptionservice.New(
				store,
				appCfg.Paths.Subscription,
				subscriptionPublicDir(appCfg.Paths.Subscription),
				singbox.New(nil),
				xray.New(nil),
			)
			result, err := svc.ExportProfile(cmd.Context(), args[0], profile, parseCSV(inboundsCSV), format)
			if err != nil {
				return err
			}

			if _, err := cmd.OutOrStdout().Write(result.Content); err != nil {
				return err
			}
			if len(result.Content) == 0 || result.Content[len(result.Content)-1] != '\n' {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", subscriptionservice.FormatJSON, "Export format: json|txt|base64")
	cmd.Flags().StringVar(&profile, "profile", subscriptionservice.DefaultProfileName, "Subscription profile name")
	cmd.Flags().StringVar(&inboundsCSV, "inbounds", "", "Comma-separated inbound IDs for named profile creation/update")
	return cmd
}

func buildRenderRequest(ctx context.Context, store *sqlite.Store) (renderer.BuildRequest, error) {
	nodes, err := store.Nodes().List(ctx)
	if err != nil {
		return renderer.BuildRequest{}, fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return renderer.BuildRequest{}, fmt.Errorf("no nodes found")
	}

	selectedNode, err := selectPrimaryNode(nodes)
	if err != nil {
		return renderer.BuildRequest{}, err
	}

	inbounds, err := store.Inbounds().List(ctx)
	if err != nil {
		return renderer.BuildRequest{}, fmt.Errorf("list inbounds: %w", err)
	}
	credentials, err := store.Credentials().List(ctx)
	if err != nil {
		return renderer.BuildRequest{}, fmt.Errorf("list credentials: %w", err)
	}

	inboundByID := make(map[string]struct{})
	filteredInbounds := make([]domain.Inbound, 0)
	for _, inbound := range inbounds {
		if !inbound.Enabled || inbound.NodeID != selectedNode.ID {
			continue
		}
		inboundByID[inbound.ID] = struct{}{}
		filteredInbounds = append(filteredInbounds, inbound)
	}
	if len(filteredInbounds) == 0 {
		return renderer.BuildRequest{}, fmt.Errorf("no enabled inbounds found for node %q", selectedNode.ID)
	}
	sort.Slice(filteredInbounds, func(i, j int) bool {
		return filteredInbounds[i].ID < filteredInbounds[j].ID
	})

	filteredCredentials := make([]domain.Credential, 0)
	for _, cred := range credentials {
		if _, ok := inboundByID[cred.InboundID]; ok {
			filteredCredentials = append(filteredCredentials, cred)
		}
	}

	return renderer.BuildRequest{
		Node:        selectedNode,
		Inbounds:    filteredInbounds,
		Credentials: filteredCredentials,
	}, nil
}

func selectPrimaryNode(nodes []domain.Node) (domain.Node, error) {
	if len(nodes) == 0 {
		return domain.Node{}, fmt.Errorf("no nodes found")
	}

	enabled := make([]domain.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Enabled {
			enabled = append(enabled, node)
		}
	}
	if len(enabled) == 0 {
		return domain.Node{}, fmt.Errorf("no enabled nodes found")
	}

	sort.Slice(enabled, func(i, j int) bool {
		if enabled[i].Role != enabled[j].Role {
			return enabled[i].Role < enabled[j].Role
		}
		if enabled[i].CreatedAt != enabled[j].CreatedAt {
			return enabled[i].CreatedAt.Before(enabled[j].CreatedAt)
		}
		return enabled[i].ID < enabled[j].ID
	})
	return enabled[0], nil
}

func selectPreviewContent(result renderer.RenderResult) []byte {
	if len(result.PreviewJSON) > 0 {
		return result.PreviewJSON
	}
	if len(result.Artifacts) > 0 {
		return result.Artifacts[0].Content
	}
	return []byte("{}\n")
}

func renderSubscriptions(ctx context.Context, store *sqlite.Store, dataDir, suffix string) ([]string, error) {
	users, err := store.Users().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	credentials, err := store.Credentials().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}

	userHasCredential := make(map[string]bool, len(credentials))
	for _, cred := range credentials {
		userHasCredential[cred.UserID] = true
	}

	svc := subscriptionservice.New(
		store,
		dataDir,
		subscriptionPublicDir(dataDir),
		singbox.New(nil),
		xray.New(nil),
	)

	lines := make([]string, 0)
	for _, user := range users {
		if !userHasCredential[user.ID] {
			continue
		}

		if strings.TrimSpace(suffix) == "" {
			generated, genErr := svc.Generate(ctx, user.ID)
			if genErr != nil {
				return nil, fmt.Errorf("generate subscription for user %q: %w", user.ID, genErr)
			}
			lines = append(lines,
				fmt.Sprintf("subscription txt [%s]: %s", user.ID, generated.TXTPath),
				fmt.Sprintf("subscription base64 [%s]: %s", user.ID, generated.Base64Path),
				fmt.Sprintf("subscription json [%s]: %s", user.ID, generated.JSONPath),
			)
			continue
		}

		generated, genErr := svc.Build(ctx, user.ID)
		if genErr != nil {
			return nil, fmt.Errorf("build preview subscription for user %q: %w", user.ID, genErr)
		}

		writer := layout.New(layout.Directories{SubscriptionsDir: dataDir})
		written, writeErr := writer.WriteSubscriptionFilesWithSuffix(user.ID, layout.SubscriptionFiles{
			TXT:    generated.TXT,
			Base64: generated.Base64,
			JSON:   generated.JSON,
		}, suffix)
		if writeErr != nil {
			return nil, fmt.Errorf("write preview subscription for user %q: %w", user.ID, writeErr)
		}
		lines = append(lines,
			fmt.Sprintf("subscription preview txt [%s]: %s", user.ID, written.TXTPath),
			fmt.Sprintf("subscription preview base64 [%s]: %s", user.ID, written.Base64Path),
			fmt.Sprintf("subscription preview json [%s]: %s", user.ID, written.JSONPath),
		)
	}
	return lines, nil
}

func openStoreWithInit(ctx context.Context, dbPath string) (*sqlite.Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if err := store.Init(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func runApplyPipeline(cmd *cobra.Command, configPath, dbPath string, dryRun bool) (applyruntime.Result, error) {
	appCfg, err := config.Load(configPath)
	if err != nil {
		return applyruntime.Result{}, err
	}

	store, err := openStoreWithInit(cmd.Context(), resolveDBPath(cmd, appCfg, dbPath))
	if err != nil {
		return applyruntime.Result{}, err
	}
	defer store.Close()

	layoutManager := layout.New(layout.Directories{
		ConfigDir:        appCfg.Paths.ConfigDir,
		RuntimeDir:       appCfg.Paths.RuntimeDir,
		CaddyDir:         appCfg.Paths.CaddyDir,
		NginxDir:         appCfg.Paths.NginxDir,
		DecoySiteDir:     appCfg.Paths.DecoySiteDir,
		StateDir:         appCfg.Paths.StateDir,
		SubscriptionsDir: appCfg.Paths.Subscription,
		BackupsDir:       appCfg.Paths.BackupsDir,
	})

	var svcMgr applyruntime.ServiceManager
	if !dryRun {
		svcMgr = systemd.NewManager()
	}

	orch, err := applyruntime.NewOrchestrator(
		store,
		layoutManager,
		singbox.New(nil),
		xray.New(nil),
		[]applyruntime.ProcessValidator{applyruntime.JSONValidator{}},
		svcMgr,
		applyruntime.RuntimeUnitSet{
			SingBox: appCfg.Runtime.SingBoxUnit,
			Xray:    appCfg.Runtime.XrayUnit,
		},
	)
	if err != nil {
		return applyruntime.Result{}, err
	}

	if dryRun {
		result, runErr := orch.Validate(cmd.Context())
		if runErr != nil {
			return applyruntime.Result{}, fmt.Errorf("validate pipeline failed: %w", runErr)
		}
		return result, nil
	}

	result, runErr := orch.Apply(cmd.Context(), applyruntime.Options{DryRun: false})
	if runErr != nil {
		return applyruntime.Result{}, fmt.Errorf("apply pipeline failed: %w", runErr)
	}
	enabledUnits, enableErr := enableRuntimeUnits(cmd.Context(), result.ServiceOps)
	if enableErr != nil {
		return applyruntime.Result{}, enableErr
	}
	for _, unit := range enabledUnits {
		fmt.Fprintf(cmd.OutOrStdout(), "service enabled: %s\n", unit)
	}
	return result, nil
}

func enableRuntimeUnits(ctx context.Context, ops []applyruntime.ServiceOperation) ([]string, error) {
	if _, err := lookPath("systemctl"); err != nil {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(ops))
	enabled := make([]string, 0, len(ops))
	for _, op := range ops {
		unit := strings.TrimSpace(op.Unit)
		if unit == "" {
			continue
		}
		if _, exists := seen[unit]; exists {
			continue
		}
		seen[unit] = struct{}{}
		if _, err := runCommandOutput(ctx, "systemctl", "enable", unit); err != nil {
			return enabled, fmt.Errorf("enable runtime unit %q: %w", unit, err)
		}
		enabled = append(enabled, unit)
	}
	return enabled, nil
}

func renderReverseProxyAndDecoy(layoutManager *layout.Manager, cfg config.AppConfig, req renderer.BuildRequest, preview bool) (string, string, int, error) {
	var (
		engine      string
		path        string
		decoyAssets []layout.StaticAsset
	)

	switch cfg.ReverseProxy {
	case config.ReverseProxyCaddy:
		engine = string(config.ReverseProxyCaddy)
		builder := caddyproxy.New(cfg)
		caddyResult, err := builder.Build(caddyproxy.BuildRequest{
			Node:     req.Node,
			Inbounds: req.Inbounds,
		})
		if err != nil {
			return "", "", 0, fmt.Errorf("render caddy config: %w", err)
		}
		if preview {
			previewPath, err := layoutManager.WriteRenderedCaddyPreview(caddyResult.Caddyfile)
			if err != nil {
				return "", "", 0, fmt.Errorf("write caddy preview: %w", err)
			}
			path = previewPath
		} else {
			written, err := layoutManager.WriteRenderedCaddyConfig(caddyResult.Caddyfile)
			if err != nil {
				return "", "", 0, fmt.Errorf("write caddy config: %w", err)
			}
			path = written.Path
		}

		assets, err := caddyproxy.LoadDecoyAssets(cfg)
		if err != nil {
			return "", "", 0, fmt.Errorf("load decoy site templates: %w", err)
		}
		decoyAssets = make([]layout.StaticAsset, 0, len(assets))
		for _, asset := range assets {
			decoyAssets = append(decoyAssets, layout.StaticAsset{
				RelativePath: asset.RelativePath,
				Content:      asset.Content,
			})
		}

	case config.ReverseProxyNginx:
		engine = string(config.ReverseProxyNginx)
		builder := nginxproxy.New(cfg)
		nginxResult, err := builder.Build(nginxproxy.BuildRequest{
			Node:     req.Node,
			Inbounds: req.Inbounds,
		})
		if err != nil {
			return "", "", 0, fmt.Errorf("render nginx config: %w", err)
		}
		if preview {
			previewPath, err := layoutManager.WriteRenderedNginxPreview(nginxResult.NginxConfig)
			if err != nil {
				return "", "", 0, fmt.Errorf("write nginx preview: %w", err)
			}
			path = previewPath
		} else {
			written, err := layoutManager.WriteRenderedNginxConfig(nginxResult.NginxConfig)
			if err != nil {
				return "", "", 0, fmt.Errorf("write nginx config: %w", err)
			}
			path = written.Path
		}

		assets, err := nginxproxy.LoadDecoyAssets(cfg)
		if err != nil {
			return "", "", 0, fmt.Errorf("load decoy site templates: %w", err)
		}
		decoyAssets = make([]layout.StaticAsset, 0, len(assets))
		for _, asset := range assets {
			decoyAssets = append(decoyAssets, layout.StaticAsset{
				RelativePath: asset.RelativePath,
				Content:      asset.Content,
			})
		}
	default:
		return "", "", 0, fmt.Errorf("unsupported reverse proxy engine %q", cfg.ReverseProxy)
	}

	writtenAssets, err := layoutManager.WriteDecoySiteAssets(decoyAssets)
	if err != nil {
		return "", "", 0, fmt.Errorf("write decoy site assets: %w", err)
	}
	return engine, path, len(writtenAssets), nil
}

func resolveDBPath(cmd *cobra.Command, cfg config.AppConfig, dbPathFlag string) string {
	flag := cmd.Flags().Lookup("db")
	if flag != nil && flag.Changed {
		return dbPathFlag
	}
	if strings.TrimSpace(cfg.Storage.SQLitePath) != "" {
		return cfg.Storage.SQLitePath
	}
	return dbPathFlag
}
