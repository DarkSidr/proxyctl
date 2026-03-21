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
	Update(ctx context.Context, user domain.User) (domain.User, error)
	Delete(ctx context.Context, userID string) (bool, error)
}

// UserTrafficRepository defines persistence operations for per-user traffic counters.
type UserTrafficRepository interface {
	Upsert(ctx context.Context, userID string, addRX, addTX int64) error
	List(ctx context.Context) ([]domain.UserTrafficRecord, error)
	ResetUser(ctx context.Context, userID string) error
	ResetAll(ctx context.Context) error
}

// NodeRepository defines persistence operations for nodes.
type NodeRepository interface {
	Create(ctx context.Context, node domain.Node) (domain.Node, error)
	List(ctx context.Context) ([]domain.Node, error)
	Update(ctx context.Context, node domain.Node) (domain.Node, error)
	Delete(ctx context.Context, nodeID string) (bool, error)
	UpdateSyncStatus(ctx context.Context, nodeID string, ok bool, msg string) error
}

// InboundRepository defines persistence operations for inbounds.
type InboundRepository interface {
	Create(ctx context.Context, inbound domain.Inbound) (domain.Inbound, error)
	List(ctx context.Context) ([]domain.Inbound, error)
	Update(ctx context.Context, inbound domain.Inbound) (domain.Inbound, error)
	Delete(ctx context.Context, inboundID string) (bool, error)
}

// CredentialRepository defines persistence operations for credentials.
type CredentialRepository interface {
	Create(ctx context.Context, credential domain.Credential) (domain.Credential, error)
	List(ctx context.Context) ([]domain.Credential, error)
	Update(ctx context.Context, credential domain.Credential) (domain.Credential, error)
	Delete(ctx context.Context, credentialID string) (bool, error)
	DeleteByUserID(ctx context.Context, userID string) (int, error)
}

// SubscriptionRepository defines persistence operations for subscriptions.
type SubscriptionRepository interface {
	Upsert(ctx context.Context, subscription domain.Subscription) (domain.Subscription, error)
	GetByUserID(ctx context.Context, userID string) (domain.Subscription, error)
	DeleteByUserID(ctx context.Context, userID string) (bool, error)
}

// Store groups persistence ports used by application layer.
type Store interface {
	Migrator
	Users() UserRepository
	Nodes() NodeRepository
	Inbounds() InboundRepository
	Credentials() CredentialRepository
	Subscriptions() SubscriptionRepository
	UserTraffic() UserTrafficRepository
	Close() error
}
