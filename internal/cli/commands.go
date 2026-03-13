package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

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
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add inbound",
		Long:  "Creates a new inbound profile for one protocol/port.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(protocol) == "" {
				if !stdinIsTerminal(cmd.InOrStdin()) {
					return fmt.Errorf("--type is required")
				}
				prompted, err := promptInboundAddWizard(cmd, *dbPath)
				if err != nil {
					return err
				}
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
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Whether inbound is enabled")

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
}

func promptInboundAddWizard(cmd *cobra.Command, dbPath string) (inboundAddPromptResult, error) {
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

	defaultPort := 443
	switch protocol {
	case "hysteria2":
		defaultPort = 8444
	case "xhttp":
		defaultPort = 9443
	case "vless":
		switch transport {
		case "ws":
			defaultPort = 8443
		case "grpc":
			defaultPort = 9443
		}
	}
	port, err := promptInt(in, out, "Port", defaultPort)
	if err != nil {
		return inboundAddPromptResult{}, err
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
			realityPublicKey, err = promptLineRequired(in, out, "Reality public key")
			if err != nil {
				return inboundAddPromptResult{}, err
			}
			realityPrivateKey, err = promptLineRequired(in, out, "Reality private key")
			if err != nil {
				return inboundAddPromptResult{}, err
			}
			realityServer, err = promptLineRequired(in, out, "Reality server (dest host)")
			if err != nil {
				return inboundAddPromptResult{}, err
			}
			realityServerPort, err = promptInt(in, out, "Reality server port", 443)
			if err != nil {
				return inboundAddPromptResult{}, err
			}
			realityShortID, err = promptLine(in, out, "Reality short id (optional)", "")
			if err != nil {
				return inboundAddPromptResult{}, err
			}
			realityFingerprint, err = promptLine(in, out, "Reality fingerprint", "chrome")
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

	enabled, err := promptBool(in, out, "Enable inbound (y/n)", true)
	if err != nil {
		return inboundAddPromptResult{}, err
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
	}, nil
}

func promptChoice(in *bufio.Reader, out io.Writer, label string, options []string, defaultValue string) (string, error) {
	fmt.Fprintf(out, "%s:\n", label)
	for i, opt := range options {
		fmt.Fprintf(out, "  %d) %s\n", i+1, opt)
	}

	optionMap := make(map[string]string, len(options))
	for _, opt := range options {
		optionMap[strings.ToLower(opt)] = opt
	}

	for {
		fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
		line, err := in.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultValue, nil
		}
		if idx, err := strconv.Atoi(line); err == nil {
			if idx >= 1 && idx <= len(options) {
				return options[idx-1], nil
			}
		}
		if resolved, ok := optionMap[strings.ToLower(line)]; ok {
			return resolved, nil
		}
		fmt.Fprintf(out, "invalid value, choose one of: %s\n", strings.Join(options, ", "))
	}
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

func newSubscriptionCmd(dbPath *string) *cobra.Command {
	cmd := newGroupCmd(
		"subscription",
		"Manage subscription outputs",
		"Builds and inspects subscription payloads for client applications.",
	)
	cmd.AddCommand(
		newSubscriptionGenerateCmd(dbPath),
		newSubscriptionShowCmd(dbPath),
		newSubscriptionExportCmd(dbPath),
	)
	return cmd
}

func newSubscriptionGenerateCmd(dbPath *string) *cobra.Command {
	defaults := config.DefaultAppConfig()

	return &cobra.Command{
		Use:   "generate <user>",
		Short: "Generate subscription payload files for a user",
		Long:  "Collects client artifacts from sing-box and Xray renderers and stores txt/base64/json subscription files.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			svc := subscriptionservice.New(
				store,
				defaults.Paths.Subscription,
				singbox.New(nil),
				xray.New(nil),
			)

			generated, err := svc.Generate(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "generated subscription for user=%s (%s)\n", generated.User.Name, generated.User.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "txt: %s\n", generated.TXTPath)
			fmt.Fprintf(cmd.OutOrStdout(), "base64: %s\n", generated.Base64Path)
			fmt.Fprintf(cmd.OutOrStdout(), "json: %s\n", generated.JSONPath)
			return nil
		},
	}
}

func newSubscriptionShowCmd(dbPath *string) *cobra.Command {
	defaults := config.DefaultAppConfig()

	return &cobra.Command{
		Use:   "show <user>",
		Short: "Show last generated subscription output for a user",
		Long:  "Reads the last generated subscription metadata and prints the stored payload.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			svc := subscriptionservice.New(
				store,
				defaults.Paths.Subscription,
				singbox.New(nil),
				xray.New(nil),
			)
			result, err := svc.Show(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "user=%s (%s)\n", result.User.Name, result.User.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "format=%s\n", result.Format)
			fmt.Fprintf(cmd.OutOrStdout(), "path=%s\n", result.Path)
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
}

func newSubscriptionExportCmd(dbPath *string) *cobra.Command {
	defaults := config.DefaultAppConfig()
	format := subscriptionservice.FormatJSON

	cmd := &cobra.Command{
		Use:   "export <user>",
		Short: "Generate and print subscription in the selected format",
		Long:  "Regenerates subscription files and prints one selected output format for automation workflows.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStoreWithInit(cmd.Context(), *dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			svc := subscriptionservice.New(
				store,
				defaults.Paths.Subscription,
				singbox.New(nil),
				xray.New(nil),
			)
			result, err := svc.Export(cmd.Context(), args[0], format)
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
	return result, nil
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
