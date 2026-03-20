package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"proxyctl/internal/storage"
)

// Store is a SQLite-backed implementation of storage.Store.
type Store struct {
	db            *sql.DB
	users         *userRepository
	nodes         *nodeRepository
	inbounds      *inboundRepository
	credentials   *credentialRepository
	subscriptions *subscriptionRepository
	userTraffic   *userTrafficRepository
}

var _ storage.Store = (*Store)(nil)

// Open creates a SQLite store for the given db file path.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	store := &Store{db: db}
	store.users = &userRepository{db: db}
	store.nodes = &nodeRepository{db: db}
	store.inbounds = &inboundRepository{db: db}
	store.credentials = &credentialRepository{db: db}
	store.subscriptions = &subscriptionRepository{db: db}
	store.userTraffic = &userTrafficRepository{db: db}

	return store, nil
}

// Init bootstraps the SQLite schema for MVP.
func (s *Store) Init(ctx context.Context) error {
	for _, statement := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	for _, statement := range schemaMigrations {
		if _, err := s.db.ExecContext(ctx, statement); err != nil && !isDuplicateColumnError(err) {
			return fmt.Errorf("apply schema migration: %w", err)
		}
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

func (s *Store) Users() storage.UserRepository { return s.users }

func (s *Store) Nodes() storage.NodeRepository { return s.nodes }

func (s *Store) Inbounds() storage.InboundRepository { return s.inbounds }

func (s *Store) Credentials() storage.CredentialRepository { return s.credentials }

func (s *Store) Subscriptions() storage.SubscriptionRepository { return s.subscriptions }

func (s *Store) UserTraffic() storage.UserTrafficRepository { return s.userTraffic }

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
