package subscription

import "proxyctl/internal/domain"

// Payload represents one generated subscription document.
type Payload struct {
	Format  string
	Content []byte
}

// Service defines the subscription output builder contract.
type Service interface {
	Build(node domain.Node, inbounds []domain.InboundProfile) (Payload, error)
}
