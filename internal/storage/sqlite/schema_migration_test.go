package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestInitMigratesSubscriptionsAccessTokenFromOldSchema(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "proxyctl.db")
	rawDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer rawDB.Close()

	if _, err := rawDB.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := rawDB.Exec(`CREATE TABLE subscriptions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL UNIQUE,
		format TEXT NOT NULL,
		output_path TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy subscriptions table: %v", err)
	}

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	var hasAccessToken bool
	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(subscriptions)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info() error: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid      int
			name     string
			colType  string
			notNull  int
			defaultV sql.NullString
			primary  int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultV, &primary); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		if name == "access_token" {
			hasAccessToken = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma rows: %v", err)
	}
	if !hasAccessToken {
		t.Fatalf("subscriptions table does not contain access_token after Init()")
	}
}
