package engine

import (
	"testing"

	"proxyctl/internal/domain"
)

func TestResolveCompatibleMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  ResolutionRequest
		want domain.Engine
	}{
		{
			name: "vless defaults to sing-box",
			req: ResolutionRequest{
				Protocol:  domain.ProtocolVLESS,
				Transport: "tcp",
			},
			want: domain.EngineSingBox,
		},
		{
			name: "hysteria2 accepts explicit sing-box",
			req: ResolutionRequest{
				Protocol:        domain.ProtocolHysteria2,
				Transport:       "udp",
				PreferredEngine: domain.EngineSingBox,
			},
			want: domain.EngineSingBox,
		},
		{
			name: "xhttp defaults to xray",
			req: ResolutionRequest{
				Protocol:  domain.ProtocolXHTTP,
				Transport: "xhttp",
			},
			want: domain.EngineXray,
		},
		{
			name: "transport and engine are normalized",
			req: ResolutionRequest{
				Protocol:        domain.ProtocolXHTTP,
				Transport:       "  XHTTP  ",
				PreferredEngine: domain.Engine(" XRAY "),
			},
			want: domain.EngineXray,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(tc.req)
			if err != nil {
				t.Fatalf("Resolve() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Resolve()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestResolveIncompatibleMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  ResolutionRequest
	}{
		{
			name: "unsupported protocol",
			req: ResolutionRequest{
				Protocol:  domain.Protocol("trojan"),
				Transport: "tcp",
			},
		},
		{
			name: "missing transport",
			req: ResolutionRequest{
				Protocol: domain.ProtocolVLESS,
			},
		},
		{
			name: "unsupported transport for hysteria2",
			req: ResolutionRequest{
				Protocol:  domain.ProtocolHysteria2,
				Transport: "ws",
			},
		},
		{
			name: "incompatible preferred engine",
			req: ResolutionRequest{
				Protocol:        domain.ProtocolXHTTP,
				Transport:       "xhttp",
				PreferredEngine: domain.EngineSingBox,
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Resolve(tc.req); err == nil {
				t.Fatalf("Resolve() expected error, got nil")
			}
		})
	}
}
