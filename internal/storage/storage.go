package storage

import (
	"context"

	"proxyctl/internal/domain"
)

// SecretStore isolates secret handling strategy for future hardening.
type SecretStore interface {
	Put(ctx context.Context, key string, plaintext string) error
	Get(ctx context.Context, key string) (string, error)
}

// Migrator bootstraps or upgrades persistence schema.
type Migrator interface {
	Init(ctx context.Context) error
}

// UserRepository defines persistence operations for users.
type UserRepository interface {
	Create(ctx context.Context, user domain.User) (domain.User, error)
	List(ctx context.Context) ([]domain.User, error)
}

// NodeRepository defines persistence operations for nodes.
type NodeRepository interface {
	Create(ctx context.Context, node domain.Node) (domain.Node, error)
	List(ctx context.Context) ([]domain.Node, error)
}

// InboundRepository defines persistence operations for inbounds.
type InboundRepository interface {
	Create(ctx context.Context, inbound domain.Inbound) (domain.Inbound, error)
	List(ctx context.Context) ([]domain.Inbound, error)
}

// CredentialRepository defines persistence operations for credentials.
type CredentialRepository interface {
	Create(ctx context.Context, credential domain.Credential) (domain.Credential, error)
	List(ctx context.Context) ([]domain.Credential, error)
}

// SubscriptionRepository defines persistence operations for subscriptions.
type SubscriptionRepository interface {
	Upsert(ctx context.Context, subscription domain.Subscription) (domain.Subscription, error)
	GetByUserID(ctx context.Context, userID string) (domain.Subscription, error)
}

// Store groups persistence ports used by application layer.
type Store interface {
	Migrator
	Users() UserRepository
	Nodes() NodeRepository
	Inbounds() InboundRepository
	Credentials() CredentialRepository
	Subscriptions() SubscriptionRepository
	Close() error
}
