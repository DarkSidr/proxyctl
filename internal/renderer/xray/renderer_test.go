package xray

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"proxyctl/internal/domain"
	"proxyctl/internal/renderer"
)

func TestRenderBuildsXrayConfigAndClientArtifacts(t *testing.T) {
	t.Parallel()

	r := New(nil)
	req := renderer.BuildRequest{
		Node: domain.Node{
			ID:   "node-1",
			Name: "eu-1",
			Host: "vpn.example.com",
		},
		Inbounds: []domain.Inbound{
			{
				ID:         "in-xhttp",
				Type:       domain.ProtocolXHTTP,
				Engine:     domain.EngineXray,
				Port:       443,
				TLSEnabled: true,
				Transport:  "xhttp",
				Path:       "/xhttp",
				Domain:     "edge.example.com",
				Enabled:    true,
			},
		},
		Credentials: []domain.Credential{
			{
				ID:        "cred-xhttp",
				InboundID: "in-xhttp",
				Kind:      domain.CredentialKindUUID,
				Secret:    "22222222-2222-2222-2222-222222222222",
			},
		},
	}

	got, err := r.Render(context.Background(), req)
	if err != nil {
		t.Fatalf("Render() unexpected error: %v", err)
	}

	if len(got.Artifacts) != 1 {
		t.Fatalf("Render() artifacts count = %d, want 1", len(got.Artifacts))
	}
	if got.Artifacts[0].Name != artifactName {
		t.Fatalf("artifact name = %q, want %q", got.Artifacts[0].Name, artifactName)
	}
	if len(got.PreviewJSON) == 0 {
		t.Fatalf("preview json is empty")
	}
	if !json.Valid(got.PreviewJSON) {
		t.Fatalf("preview json is invalid: %s", string(got.PreviewJSON))
	}

	var cfg struct {
		Inbounds []struct {
			Protocol       string `json:"protocol"`
			StreamSettings struct {
				Network  string `json:"network"`
				Security string `json:"security"`
				TLS      struct {
					Certificates []struct {
						CertificateFile string `json:"certificateFile"`
						KeyFile         string `json:"keyFile"`
					} `json:"certificates"`
				} `json:"tlsSettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(got.PreviewJSON, &cfg); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if len(cfg.Inbounds) != 1 {
		t.Fatalf("inbounds count = %d, want 1", len(cfg.Inbounds))
	}
	if cfg.Inbounds[0].Protocol != "vless" {
		t.Fatalf("inbound protocol = %q, want vless", cfg.Inbounds[0].Protocol)
	}
	if cfg.Inbounds[0].StreamSettings.Network != "xhttp" {
		t.Fatalf("stream network = %q, want xhttp", cfg.Inbounds[0].StreamSettings.Network)
	}
	if cfg.Inbounds[0].StreamSettings.Security != "tls" {
		t.Fatalf("stream security = %q, want tls", cfg.Inbounds[0].StreamSettings.Security)
	}
	if len(cfg.Inbounds[0].StreamSettings.TLS.Certificates) != 1 {
		t.Fatalf("tls certificates count = %d, want 1", len(cfg.Inbounds[0].StreamSettings.TLS.Certificates))
	}
	if cfg.Inbounds[0].StreamSettings.TLS.Certificates[0].CertificateFile != "/caddy/certificates/acme-v02.api.letsencrypt.org-directory/edge.example.com/edge.example.com.crt" {
		t.Fatalf("certificateFile = %q, want default caddy path", cfg.Inbounds[0].StreamSettings.TLS.Certificates[0].CertificateFile)
	}
	if cfg.Inbounds[0].StreamSettings.TLS.Certificates[0].KeyFile != "/caddy/certificates/acme-v02.api.letsencrypt.org-directory/edge.example.com/edge.example.com.key" {
		t.Fatalf("keyFile = %q, want default caddy path", cfg.Inbounds[0].StreamSettings.TLS.Certificates[0].KeyFile)
	}

	if len(got.ClientArtifacts) != 1 {
		t.Fatalf("client artifacts count = %d, want 1", len(got.ClientArtifacts))
	}
	item := got.ClientArtifacts[0]
	if item.Protocol != domain.ProtocolXHTTP {
		t.Fatalf("client protocol = %q, want %q", item.Protocol, domain.ProtocolXHTTP)
	}
	if !strings.HasPrefix(item.URI, "vless://") {
		t.Fatalf("xhttp uri = %q, want vless:// prefix", item.URI)
	}
	if !strings.Contains(item.URI, "type=xhttp") {
		t.Fatalf("xhttp uri = %q, expected type=xhttp", item.URI)
	}
}

func TestRenderFailsWithoutRequiredCredentials(t *testing.T) {
	t.Parallel()

	r := New(nil)
	_, err := r.Render(context.Background(), renderer.BuildRequest{
		Node: domain.Node{Host: "vpn.example.com"},
		Inbounds: []domain.Inbound{
			{
				ID:         "in-xhttp",
				Type:       domain.ProtocolXHTTP,
				Engine:     domain.EngineXray,
				Port:       443,
				Transport:  "xhttp",
				TLSEnabled: true,
				Enabled:    true,
			},
		},
	})
	if err == nil {
		t.Fatalf("Render() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "uuid credential") {
		t.Fatalf("Render() error = %q, want uuid credential error", err)
	}
}

func TestRenderBuildsVLESSRealityConfigAndClientArtifacts(t *testing.T) {
	t.Parallel()

	r := New(nil)
	req := renderer.BuildRequest{
		Node: domain.Node{
			ID:   "node-1",
			Name: "eu-1",
			Host: "64.188.78.69",
		},
		Inbounds: []domain.Inbound{
			{
				ID:                 "in-vless-reality",
				Type:               domain.ProtocolVLESS,
				Engine:             domain.EngineXray,
				Port:               443,
				Transport:          "tcp",
				SNI:                "www.intel.com",
				RealityEnabled:     true,
				RealityPublicKey:   "QBGmdUJyCULtBWHdp3FiKTSDcRkumkPHEuMw8THJGmY",
				RealityPrivateKey:  "sQCU8cQ1GW7xF9W5xMkDEnJcPXL0Rs9yoXG4_G8Bf3o",
				RealityShortID:     "797e",
				RealityFingerprint: "chrome",
				RealitySpiderX:     "/Jx6iYQje4UnbubT",
				RealityServer:      "www.intel.com",
				RealityServerPort:  443,
				VLESSFlow:          "xtls-rprx-vision",
				Enabled:            true,
			},
		},
		Credentials: []domain.Credential{
			{
				ID:        "cred-vless",
				InboundID: "in-vless-reality",
				Kind:      domain.CredentialKindUUID,
				Secret:    "e1cb2423-ffc5-4c1e-a092-a04827525568",
				Metadata:  `{"label":"kamil-reality"}`,
			},
		},
	}

	got, err := r.Render(context.Background(), req)
	if err != nil {
		t.Fatalf("Render() unexpected error: %v", err)
	}

	if len(got.ClientArtifacts) != 1 {
		t.Fatalf("client artifacts count = %d, want 1", len(got.ClientArtifacts))
	}
	uri := got.ClientArtifacts[0].URI
	if !strings.Contains(uri, "security=reality") {
		t.Fatalf("reality uri = %q, expected security=reality", uri)
	}
	if !strings.Contains(uri, "flow=xtls-rprx-vision") {
		t.Fatalf("reality uri = %q, expected flow=xtls-rprx-vision", uri)
	}
	if !strings.Contains(uri, "pbk=QBGmdUJyCULtBWHdp3FiKTSDcRkumkPHEuMw8THJGmY") {
		t.Fatalf("reality uri = %q, expected pbk query", uri)
	}
	if !strings.Contains(uri, "sid=797e") {
		t.Fatalf("reality uri = %q, expected sid query", uri)
	}
	if !strings.Contains(uri, "type=tcp") {
		t.Fatalf("reality uri = %q, expected type=tcp", uri)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse reality uri: %v", err)
	}
	if parsed.Fragment != "kamil-reality" {
		t.Fatalf("reality uri fragment = %q, want kamil-reality", parsed.Fragment)
	}

	var cfg struct {
		Inbounds []struct {
			Protocol       string `json:"protocol"`
			StreamSettings struct {
				Network  string `json:"network"`
				Security string `json:"security"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(got.PreviewJSON, &cfg); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if len(cfg.Inbounds) != 1 {
		t.Fatalf("inbounds count = %d, want 1", len(cfg.Inbounds))
	}
	if cfg.Inbounds[0].Protocol != "vless" {
		t.Fatalf("inbound protocol = %q, want vless", cfg.Inbounds[0].Protocol)
	}
	if cfg.Inbounds[0].StreamSettings.Network != "tcp" {
		t.Fatalf("stream network = %q, want tcp", cfg.Inbounds[0].StreamSettings.Network)
	}
	if cfg.Inbounds[0].StreamSettings.Security != "reality" {
		t.Fatalf("stream security = %q, want reality", cfg.Inbounds[0].StreamSettings.Security)
	}
}

func TestRenderXHTTPTLSUsesExplicitCertificatePaths(t *testing.T) {
	t.Parallel()

	r := New(nil)
	got, err := r.Render(context.Background(), renderer.BuildRequest{
		Node: domain.Node{Host: "node.example.com"},
		Inbounds: []domain.Inbound{
			{
				ID:          "in-xhttp",
				Type:        domain.ProtocolXHTTP,
				Engine:      domain.EngineXray,
				Port:        9443,
				Transport:   "xhttp",
				TLSEnabled:  true,
				Domain:      "darksidr.icu",
				TLSCertPath: "/custom/fullchain.pem",
				TLSKeyPath:  "/custom/privkey.pem",
				Enabled:     true,
			},
		},
		Credentials: []domain.Credential{
			{
				ID:        "cred-xhttp",
				InboundID: "in-xhttp",
				Kind:      domain.CredentialKindUUID,
				Secret:    "22222222-2222-2222-2222-222222222222",
			},
		},
	})
	if err != nil {
		t.Fatalf("Render() unexpected error: %v", err)
	}

	body := string(got.PreviewJSON)
	if !strings.Contains(body, `"certificateFile": "/custom/fullchain.pem"`) {
		t.Fatalf("expected explicit certificateFile, got: %s", body)
	}
	if !strings.Contains(body, `"keyFile": "/custom/privkey.pem"`) {
		t.Fatalf("expected explicit keyFile, got: %s", body)
	}
}
