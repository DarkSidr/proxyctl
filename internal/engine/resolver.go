package engine

import (
	"fmt"
	"strings"

	"proxyctl/internal/domain"
)

type ResolutionRequest struct {
	Protocol        domain.Protocol
	Transport       string
	PreferredEngine domain.Engine
}

type ProtocolRule struct {
	DefaultEngine     domain.Engine
	SupportedEngines  map[domain.Engine]struct{}
	AllowedTransports map[string]struct{}
}

var protocolMatrix = map[domain.Protocol]ProtocolRule{
	domain.ProtocolVLESS: {
		DefaultEngine: domain.EngineSingBox,
		SupportedEngines: map[domain.Engine]struct{}{
			domain.EngineSingBox: {},
		},
		AllowedTransports: map[string]struct{}{
			"tcp":  {},
			"ws":   {},
			"grpc": {},
		},
	},
	domain.ProtocolHysteria2: {
		DefaultEngine: domain.EngineSingBox,
		SupportedEngines: map[domain.Engine]struct{}{
			domain.EngineSingBox: {},
		},
		AllowedTransports: map[string]struct{}{
			"udp": {},
		},
	},
	domain.ProtocolXHTTP: {
		DefaultEngine: domain.EngineXray,
		SupportedEngines: map[domain.Engine]struct{}{
			domain.EngineXray: {},
		},
		AllowedTransports: map[string]struct{}{
			"xhttp": {},
		},
	},
}

func Resolve(req ResolutionRequest) (domain.Engine, error) {
	rule, ok := protocolMatrix[req.Protocol]
	if !ok {
		return "", fmt.Errorf("unsupported protocol %q", req.Protocol)
	}

	transport := normalize(req.Transport)
	if transport == "" {
		return "", fmt.Errorf("transport is required for protocol %q", req.Protocol)
	}
	if _, ok := rule.AllowedTransports[transport]; !ok {
		return "", fmt.Errorf("transport %q is not supported for protocol %q", req.Transport, req.Protocol)
	}

	preferred := normalizeEngine(req.PreferredEngine)
	if preferred == "" {
		return rule.DefaultEngine, nil
	}

	if _, ok := rule.SupportedEngines[preferred]; !ok {
		return "", fmt.Errorf(
			"engine %q is incompatible with protocol %q (transport %q)",
			req.PreferredEngine,
			req.Protocol,
			transport,
		)
	}

	return preferred, nil
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func normalizeEngine(e domain.Engine) domain.Engine {
	return domain.Engine(normalize(string(e)))
}
