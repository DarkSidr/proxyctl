package renderer

import (
	"context"

	"proxyctl/internal/domain"
)

// Artifact is one rendered runtime config output.
type Artifact struct {
	Name    string
	Content []byte
}

// ClientArtifact is one generated client-facing output (for subscriptions).
type ClientArtifact struct {
	Protocol     domain.Protocol `json:"protocol"`
	InboundID    string          `json:"inbound_id"`
	CredentialID string          `json:"credential_id"`
	URI          string          `json:"uri"`
}

// BuildRequest contains data required to render one candidate revision.
type BuildRequest struct {
	Node        domain.Node
	Inbounds    []domain.Inbound
	Credentials []domain.Credential
}

// RenderResult contains server and client-facing renderer outputs.
type RenderResult struct {
	Artifacts       []Artifact
	PreviewJSON     []byte
	ClientArtifacts []ClientArtifact
}

// Service defines renderer behavior for runtime artifacts.
type Service interface {
	Render(ctx context.Context, req BuildRequest) (RenderResult, error)
}
