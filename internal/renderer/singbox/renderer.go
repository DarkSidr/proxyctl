package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"proxyctl/internal/domain"
	"proxyctl/internal/renderer"
)

const artifactName = "sing-box.json"

type Renderer struct {
	validator Validator
}

func New(v Validator) *Renderer {
	if v == nil {
		v = NoopValidator{}
	}
	return &Renderer{validator: v}
}

func (r *Renderer) Render(ctx context.Context, req renderer.BuildRequest) (renderer.RenderResult, error) {
	cfg, artifacts, err := buildConfig(req)
	if err != nil {
		return renderer.RenderResult{}, err
	}

	preview, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return renderer.RenderResult{}, fmt.Errorf("marshal sing-box config: %w", err)
	}
	if err := r.validator.Validate(ctx, preview); err != nil {
		return renderer.RenderResult{}, fmt.Errorf("validate sing-box config: %w", err)
	}

	return renderer.RenderResult{
		Artifacts: []renderer.Artifact{
			{
				Name:    artifactName,
				Content: preview,
			},
		},
		PreviewJSON:     preview,
		ClientArtifacts: artifacts,
	}, nil
}

type sbExperimental struct {
	V2RayAPI *sbV2RayAPI `json:"v2ray_api,omitempty"`
}

type sbV2RayAPI struct {
	Listen string       `json:"listen"`
	Stats  sbV2RayStats `json:"stats"`
}

type sbV2RayStats struct {
	Enabled bool     `json:"enabled"`
	Users   []string `json:"users"`
}

type configDoc struct {
	Inbounds     []inboundConfig  `json:"inbounds"`
	Outbounds    []outboundConfig `json:"outbounds"`
	Route        routeConfig      `json:"route"`
	Experimental *sbExperimental  `json:"experimental,omitempty"`
}

type outboundConfig struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type routeConfig struct {
	Final string `json:"final"`
}

type inboundConfig struct {
	Type       string          `json:"type"`
	Tag        string          `json:"tag"`
	Listen     string          `json:"listen"`
	ListenPort int             `json:"listen_port"`
	TLS        *tlsConfig      `json:"tls,omitempty"`
	Transport  *transport      `json:"transport,omitempty"`
	Users      json.RawMessage `json:"users"`
}

type tlsConfig struct {
	Enabled         bool   `json:"enabled"`
	ServerName      string `json:"server_name,omitempty"`
	CertificatePath string `json:"certificate_path,omitempty"`
	KeyPath         string `json:"key_path,omitempty"`
}

type transport struct {
	Type        string `json:"type"`
	Path        string `json:"path,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
}

type vlessUser struct {
	UUID string `json:"uuid"`
}

type hysteria2User struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func buildConfig(req renderer.BuildRequest) (configDoc, []renderer.ClientArtifact, error) {
	byInbound := map[string][]domain.Credential{}
	for _, cred := range req.Credentials {
		byInbound[cred.InboundID] = append(byInbound[cred.InboundID], cred)
	}
	for id := range byInbound {
		sort.Slice(byInbound[id], func(i, j int) bool {
			return byInbound[id][i].ID < byInbound[id][j].ID
		})
	}

	inbounds := append([]domain.Inbound(nil), req.Inbounds...)
	sort.Slice(inbounds, func(i, j int) bool {
		return inbounds[i].ID < inbounds[j].ID
	})

	var (
		cfgInbounds []inboundConfig
		clients     []renderer.ClientArtifact
	)

	for _, inbound := range inbounds {
		if !inbound.Enabled || inbound.Engine != domain.EngineSingBox {
			continue
		}
		if inbound.Port < 1 || inbound.Port > 65535 {
			return configDoc{}, nil, fmt.Errorf("inbound %q has invalid port %d", inbound.ID, inbound.Port)
		}

		switch inbound.Type {
		case domain.ProtocolVLESS:
			cfg, items, err := buildVLESSInbound(req.Node, inbound, byInbound[inbound.ID])
			if err != nil {
				return configDoc{}, nil, err
			}
			cfgInbounds = append(cfgInbounds, cfg)
			clients = append(clients, items...)
		case domain.ProtocolHysteria2:
			cfg, items, err := buildHysteria2Inbound(req.Node, inbound, byInbound[inbound.ID])
			if err != nil {
				return configDoc{}, nil, err
			}
			cfgInbounds = append(cfgInbounds, cfg)
			clients = append(clients, items...)
		default:
			return configDoc{}, nil, fmt.Errorf("sing-box renderer does not support protocol %q", inbound.Type)
		}
	}

	return configDoc{
		Inbounds: cfgInbounds,
		Outbounds: []outboundConfig{
			{Type: "direct", Tag: "direct"},
		},
		Route: routeConfig{Final: "direct"},
	}, clients, nil
}

func buildVLESSInbound(node domain.Node, inbound domain.Inbound, credentials []domain.Credential) (inboundConfig, []renderer.ClientArtifact, error) {
	if inbound.Transport != "tcp" && inbound.Transport != "ws" && inbound.Transport != "grpc" {
		return inboundConfig{}, nil, fmt.Errorf("inbound %q has unsupported vless transport %q", inbound.ID, inbound.Transport)
	}

	var users []vlessUser
	var clients []renderer.ClientArtifact
	for _, cred := range credentials {
		if cred.Kind != domain.CredentialKindUUID || strings.TrimSpace(cred.Secret) == "" {
			continue
		}
		users = append(users, vlessUser{UUID: strings.TrimSpace(cred.Secret)})
		clients = append(clients, renderer.ClientArtifact{
			Protocol:     domain.ProtocolVLESS,
			InboundID:    inbound.ID,
			CredentialID: cred.ID,
			URI:          vlessURI(node.Host, inbound, cred),
		})
	}
	if len(users) == 0 {
		return inboundConfig{}, nil, fmt.Errorf("inbound %q requires at least one uuid credential", inbound.ID)
	}

	rawUsers, err := json.Marshal(users)
	if err != nil {
		return inboundConfig{}, nil, fmt.Errorf("marshal vless users: %w", err)
	}

	cfg := inboundConfig{
		Type:       "vless",
		Tag:        "vless-" + inbound.ID,
		Listen:     "::",
		ListenPort: inbound.Port,
		Users:      rawUsers,
	}
	if inbound.TLSEnabled {
		certPath, keyPath := resolveTLSPaths(node.Host, inbound)
		cfg.TLS = &tlsConfig{
			Enabled:         true,
			ServerName:      serverName(inbound, node.Host),
			CertificatePath: certPath,
			KeyPath:         keyPath,
		}
	}
	if inbound.Transport == "ws" {
		path := inbound.Path
		if strings.TrimSpace(path) == "" {
			path = "/"
		}
		cfg.Transport = &transport{Type: "ws", Path: path}
	}
	if inbound.Transport == "grpc" {
		name := strings.Trim(strings.TrimSpace(inbound.Path), "/")
		if name == "" {
			name = "grpc"
		}
		cfg.Transport = &transport{Type: "grpc", ServiceName: name}
	}

	return cfg, clients, nil
}

func buildHysteria2Inbound(node domain.Node, inbound domain.Inbound, credentials []domain.Credential) (inboundConfig, []renderer.ClientArtifact, error) {
	if inbound.Transport != "udp" {
		return inboundConfig{}, nil, fmt.Errorf("inbound %q has unsupported hysteria2 transport %q", inbound.ID, inbound.Transport)
	}

	var users []hysteria2User
	var clients []renderer.ClientArtifact
	for _, cred := range credentials {
		if cred.Kind != domain.CredentialKindPassword || strings.TrimSpace(cred.Secret) == "" {
			continue
		}
		users = append(users, hysteria2User{Name: cred.UserID, Password: strings.TrimSpace(cred.Secret)})
		clients = append(clients, renderer.ClientArtifact{
			Protocol:     domain.ProtocolHysteria2,
			InboundID:    inbound.ID,
			CredentialID: cred.ID,
			URI:          hysteria2URI(node.Host, inbound, cred),
		})
	}
	if len(users) == 0 {
		return inboundConfig{}, nil, fmt.Errorf("inbound %q requires at least one password credential", inbound.ID)
	}

	rawUsers, err := json.Marshal(users)
	if err != nil {
		return inboundConfig{}, nil, fmt.Errorf("marshal hysteria2 users: %w", err)
	}

	cfg := inboundConfig{
		Type:       "hysteria2",
		Tag:        "hysteria2-" + inbound.ID,
		Listen:     "::",
		ListenPort: inbound.Port,
		Users:      rawUsers,
	}
	if inbound.TLSEnabled {
		certPath, keyPath := resolveTLSPaths(node.Host, inbound)
		cfg.TLS = &tlsConfig{
			Enabled:         true,
			ServerName:      serverName(inbound, node.Host),
			CertificatePath: certPath,
			KeyPath:         keyPath,
		}
	}
	return cfg, clients, nil
}

func resolveTLSPaths(host string, inbound domain.Inbound) (string, string) {
	certPath := strings.TrimSpace(inbound.TLSCertPath)
	keyPath := strings.TrimSpace(inbound.TLSKeyPath)
	if certPath != "" && keyPath != "" {
		return certPath, keyPath
	}
	server := serverName(inbound, host)
	if server == "" {
		return certPath, keyPath
	}
	base := "/caddy/certificates/acme-v02.api.letsencrypt.org-directory/" + server + "/" + server
	if certPath == "" {
		certPath = base + ".crt"
	}
	if keyPath == "" {
		keyPath = base + ".key"
	}
	return certPath, keyPath
}

func serverName(inbound domain.Inbound, fallbackHost string) string {
	if strings.TrimSpace(inbound.SNI) != "" {
		return strings.TrimSpace(inbound.SNI)
	}
	if strings.TrimSpace(inbound.Domain) != "" {
		return strings.TrimSpace(inbound.Domain)
	}
	return strings.TrimSpace(fallbackHost)
}

func vlessURI(host string, inbound domain.Inbound, credential domain.Credential) string {
	connectHost := endpointHost(inbound, host)
	u := url.URL{
		Scheme: "vless",
		User:   url.User(strings.TrimSpace(credential.Secret)),
		Host:   fmt.Sprintf("%s:%d", strings.TrimSpace(connectHost), inbound.Port),
	}
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("security", boolSecurity(inbound.TLSEnabled))
	q.Set("type", inbound.Transport)

	sni := serverName(inbound, connectHost)
	if sni != "" {
		q.Set("sni", sni)
	}

	switch inbound.Transport {
	case "ws":
		path := inbound.Path
		if strings.TrimSpace(path) == "" {
			path = "/"
		}
		q.Set("path", path)
		if wsHost := wsHostHeader(inbound, connectHost); wsHost != "" {
			q.Set("host", wsHost)
		}
	case "grpc":
		name := strings.Trim(strings.TrimSpace(inbound.Path), "/")
		if name == "" {
			name = "grpc"
		}
		q.Set("serviceName", name)
		if authority := wsHostHeader(inbound, connectHost); authority != "" {
			q.Set("authority", authority)
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment = clientLabel(credential, inbound.ID)
	return u.String()
}

func hysteria2URI(host string, inbound domain.Inbound, credential domain.Credential) string {
	u := url.URL{
		Scheme: "hysteria2",
		User:   url.User(strings.TrimSpace(credential.Secret)),
		Host:   fmt.Sprintf("%s:%d", strings.TrimSpace(host), inbound.Port),
	}
	q := url.Values{}
	sni := serverName(inbound, host)
	if sni != "" {
		q.Set("sni", sni)
	}
	if inbound.TLSEnabled {
		q.Set("insecure", "0")
	}
	u.RawQuery = q.Encode()
	u.Fragment = clientLabel(credential, inbound.ID)
	return u.String()
}

func boolSecurity(tls bool) string {
	if tls {
		return "tls"
	}
	return "none"
}

func endpointHost(inbound domain.Inbound, fallbackHost string) string {
	if strings.TrimSpace(inbound.Domain) != "" {
		return strings.TrimSpace(inbound.Domain)
	}
	if strings.TrimSpace(inbound.SNI) != "" {
		return strings.TrimSpace(inbound.SNI)
	}
	return strings.TrimSpace(fallbackHost)
}

func wsHostHeader(inbound domain.Inbound, fallbackHost string) string {
	if strings.TrimSpace(inbound.Domain) != "" {
		return strings.TrimSpace(inbound.Domain)
	}
	if strings.TrimSpace(inbound.SNI) != "" {
		return strings.TrimSpace(inbound.SNI)
	}
	return strings.TrimSpace(fallbackHost)
}

func clientLabel(credential domain.Credential, inboundID string) string {
	var metadata struct {
		Label string `json:"label"`
	}
	if strings.TrimSpace(credential.Metadata) != "" {
		_ = json.Unmarshal([]byte(credential.Metadata), &metadata)
	}
	if label := strings.TrimSpace(metadata.Label); label != "" {
		if sanitized := sanitizeClientLabel(label); sanitized != "" {
			return sanitized
		}
	}
	return "proxyctl-" + strings.TrimSpace(inboundID)
}

func sanitizeClientLabel(label string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range strings.TrimSpace(label) {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}
