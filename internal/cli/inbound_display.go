package cli

import (
	"fmt"
	"strings"

	"proxyctl/internal/domain"
)

func inboundDisplayHost(node domain.Node, inbound domain.Inbound) string {
	endpointHost := strings.TrimSpace(node.Host)
	publicHost := strings.TrimSpace(inbound.Domain)

	if endpointHost == "" {
		endpointHost = publicHost
	}
	if endpointHost == "" {
		return "<no-domain>"
	}
	if publicHost == "" || strings.EqualFold(publicHost, endpointHost) {
		return endpointHost
	}

	label := "domain"
	if inbound.RealityEnabled && inbound.SelfSteal {
		label = "sni"
	}
	return fmt.Sprintf("%s [%s: %s]", endpointHost, label, publicHost)
}
