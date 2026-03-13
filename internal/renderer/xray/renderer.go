package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"proxyctl/internal/domain"
	"proxyctl/internal/renderer"
)

const artifactName = "xray.json"

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
		return renderer.RenderResult{}, fmt.Errorf("marshal xray config: %w", err)
	}
	if err := r.validator.Validate(ctx, preview); err != nil {
		return renderer.RenderResult{}, fmt.Errorf("validate xray config: %w", err)
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

type configDoc struct {
	Inbounds  []inboundConfig  `json:"inbounds"`
	Outbounds []outboundConfig `json:"outbounds"`
}

type outboundConfig struct {
	Tag      string `json:"tag"`
	Protocol string `json:"protocol"`
}

type inboundConfig struct {
	Tag            string           `json:"tag"`
	Listen         string           `json:"listen"`
	Port           int              `json:"port"`
	Protocol       string           `json:"protocol"`
	Settings       inboundSettings  `json:"settings"`
	StreamSettings streamSettings   `json:"streamSettings"`
	Sniffing       *sniffingConfig  `json:"sniffing,omitempty"`
	Allocate       *allocationHints `json:"allocate,omitempty"`
}

type inboundSettings struct {
	Clients    []clientConfig `json:"clients"`
	Decryption string         `json:"decryption"`
}

type clientConfig struct {
	ID string `json:"id"`
}

type streamSettings struct {
	Network       string         `json:"network"`
	Security      string         `json:"security"`
	XHTTPSettings xhttpSettings  `json:"xhttpSettings"`
	TLSSettings   *tlsSettings   `json:"tlsSettings,omitempty"`
	Sockopt       *sockoptConfig `json:"sockopt,omitempty"`
}

type xhttpSettings struct {
	Path string `json:"path"`
	Host string `json:"host,omitempty"`
	Mode string `json:"mode"`
}

type tlsSettings struct {
	ServerName string `json:"serverName,omitempty"`
}

type sniffingConfig struct {
	Enabled      bool     `json:"enabled"`
	DestOverride []string `json:"destOverride,omitempty"`
}

type allocationHints struct {
	Strategy string `json:"strategy"`
}

type sockoptConfig struct {
	AcceptProxyProtocol bool `json:"acceptProxyProtocol"`
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
		if !inbound.Enabled || inbound.Engine != domain.EngineXray {
			continue
		}
		if inbound.Port < 1 || inbound.Port > 65535 {
			return configDoc{}, nil, fmt.Errorf("inbound %q has invalid port %d", inbound.ID, inbound.Port)
		}

		switch inbound.Type {
		case domain.ProtocolXHTTP:
			cfg, items, err := buildXHTTPInbound(req.Node, inbound, byInbound[inbound.ID])
			if err != nil {
				return configDoc{}, nil, err
			}
			cfgInbounds = append(cfgInbounds, cfg)
			clients = append(clients, items...)
		default:
			return configDoc{}, nil, fmt.Errorf("xray renderer does not support protocol %q", inbound.Type)
		}
	}

	return configDoc{
		Inbounds: cfgInbounds,
		Outbounds: []outboundConfig{
			{Tag: "direct", Protocol: "freedom"},
		},
	}, clients, nil
}

func buildXHTTPInbound(node domain.Node, inbound domain.Inbound, credentials []domain.Credential) (inboundConfig, []renderer.ClientArtifact, error) {
	if inbound.Transport != "xhttp" {
		return inboundConfig{}, nil, fmt.Errorf("inbound %q has unsupported xhttp transport %q", inbound.ID, inbound.Transport)
	}

	var (
		cfgClients []clientConfig
		clients    []renderer.ClientArtifact
	)
	for _, cred := range credentials {
		secret := strings.TrimSpace(cred.Secret)
		if cred.Kind != domain.CredentialKindUUID || secret == "" {
			continue
		}
		cfgClients = append(cfgClients, clientConfig{ID: secret})
		clients = append(clients, renderer.ClientArtifact{
			Protocol:     domain.ProtocolXHTTP,
			InboundID:    inbound.ID,
			CredentialID: cred.ID,
			URI:          xhttpURI(node.Host, inbound, secret),
		})
	}
	if len(cfgClients) == 0 {
		return inboundConfig{}, nil, fmt.Errorf("inbound %q requires at least one uuid credential", inbound.ID)
	}

	host := hostHeader(inbound, node.Host)
	path := normalizePath(inbound.Path)
	security := "none"
	var tlsCfg *tlsSettings
	if inbound.TLSEnabled {
		security = "tls"
		tlsCfg = &tlsSettings{ServerName: serverName(inbound, node.Host)}
	}

	return inboundConfig{
		Tag:      "xhttp-" + inbound.ID,
		Listen:   "::",
		Port:     inbound.Port,
		Protocol: "vless",
		Settings: inboundSettings{
			Clients:    cfgClients,
			Decryption: "none",
		},
		StreamSettings: streamSettings{
			Network:  "xhttp",
			Security: security,
			XHTTPSettings: xhttpSettings{
				Path: path,
				Host: host,
				Mode: "auto",
			},
			TLSSettings: tlsCfg,
		},
	}, clients, nil
}

func xhttpURI(host string, inbound domain.Inbound, uuid string) string {
	u := url.URL{
		Scheme: "vless",
		User:   url.User(strings.TrimSpace(uuid)),
		Host:   fmt.Sprintf("%s:%d", strings.TrimSpace(host), inbound.Port),
	}
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("type", "xhttp")
	q.Set("security", boolSecurity(inbound.TLSEnabled))

	server := serverName(inbound, host)
	if server != "" {
		q.Set("sni", server)
	}
	if header := hostHeader(inbound, host); header != "" {
		q.Set("host", header)
	}
	q.Set("path", normalizePath(inbound.Path))

	u.RawQuery = q.Encode()
	u.Fragment = "proxyctl-" + inbound.ID
	return u.String()
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

func hostHeader(inbound domain.Inbound, fallbackHost string) string {
	if strings.TrimSpace(inbound.Domain) != "" {
		return strings.TrimSpace(inbound.Domain)
	}
	return strings.TrimSpace(fallbackHost)
}

func normalizePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return "/"
	}
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func boolSecurity(tls bool) string {
	if tls {
		return "tls"
	}
	return "none"
}
