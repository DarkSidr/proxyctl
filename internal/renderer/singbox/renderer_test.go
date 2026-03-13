package singbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"proxyctl/internal/domain"
	"proxyctl/internal/renderer"
)

func TestRenderBuildsSingBoxConfigAndClientArtifacts(t *testing.T) {
	t.Parallel()

	r := New(nil)

	req := renderer.BuildRequest{
		Node: domain.Node{
			ID:   "n1",
			Name: "eu-1",
			Host: "vpn.example.com",
		},
		Inbounds: []domain.Inbound{
			{
				ID:         "in-vless",
				Type:       domain.ProtocolVLESS,
				Engine:     domain.EngineSingBox,
				Port:       443,
				TLSEnabled: true,
				Transport:  "ws",
				Path:       "/ws",
				Domain:     "vpn.example.com",
				Enabled:    true,
			},
			{
				ID:         "in-hy2",
				Type:       domain.ProtocolHysteria2,
				Engine:     domain.EngineSingBox,
				Port:       8443,
				TLSEnabled: true,
				Transport:  "udp",
				SNI:        "hy2.example.com",
				Enabled:    true,
			},
		},
		Credentials: []domain.Credential{
			{
				ID:        "cred-vless",
				InboundID: "in-vless",
				Kind:      domain.CredentialKindUUID,
				Secret:    "11111111-1111-1111-1111-111111111111",
			},
			{
				ID:        "cred-hy2",
				InboundID: "in-hy2",
				Kind:      domain.CredentialKindPassword,
				Secret:    "hy2-secret",
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
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(got.PreviewJSON, &cfg); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if len(cfg.Inbounds) != 2 {
		t.Fatalf("inbounds count = %d, want 2", len(cfg.Inbounds))
	}

	if len(got.ClientArtifacts) != 2 {
		t.Fatalf("client artifacts count = %d, want 2", len(got.ClientArtifacts))
	}

	var hasVLESS, hasHY2 bool
	for _, item := range got.ClientArtifacts {
		switch item.Protocol {
		case domain.ProtocolVLESS:
			hasVLESS = true
			if !strings.HasPrefix(item.URI, "vless://") {
				t.Fatalf("vless uri = %q, want vless:// prefix", item.URI)
			}
		case domain.ProtocolHysteria2:
			hasHY2 = true
			if !strings.HasPrefix(item.URI, "hysteria2://") {
				t.Fatalf("hy2 uri = %q, want hysteria2:// prefix", item.URI)
			}
		}
	}
	if !hasVLESS || !hasHY2 {
		t.Fatalf("expected both vless and hysteria2 artifacts, got %+v", got.ClientArtifacts)
	}
}

func TestRenderFailsWithoutRequiredCredentials(t *testing.T) {
	t.Parallel()

	r := New(nil)
	_, err := r.Render(context.Background(), renderer.BuildRequest{
		Node: domain.Node{Host: "vpn.example.com"},
		Inbounds: []domain.Inbound{
			{
				ID:         "in-vless",
				Type:       domain.ProtocolVLESS,
				Engine:     domain.EngineSingBox,
				Port:       443,
				Transport:  "tcp",
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
