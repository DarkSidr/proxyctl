package sqlite

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		host TEXT NOT NULL,
		role TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS inbounds (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		engine TEXT NOT NULL,
		node_id TEXT NOT NULL,
		domain TEXT,
		port INTEGER NOT NULL,
		tls_enabled INTEGER NOT NULL DEFAULT 0,
		transport TEXT,
		path TEXT,
		sni TEXT,
		reality_enabled INTEGER NOT NULL DEFAULT 0,
		reality_public_key TEXT,
		reality_private_key TEXT,
		reality_short_id TEXT,
		reality_fingerprint TEXT,
		reality_spider_x TEXT,
		reality_server TEXT,
		reality_server_port INTEGER NOT NULL DEFAULT 0,
		vless_flow TEXT,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS credentials (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		inbound_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		secret TEXT NOT NULL,
		metadata TEXT,
		created_at TEXT NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
		FOREIGN KEY (inbound_id) REFERENCES inbounds(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS subscriptions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL UNIQUE,
		format TEXT NOT NULL,
		output_path TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_inbounds_node_id ON inbounds(node_id)`,
	`CREATE INDEX IF NOT EXISTS idx_credentials_user_id ON credentials(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_credentials_inbound_id ON credentials(inbound_id)`,
}

var schemaMigrations = []string{
	`ALTER TABLE inbounds ADD COLUMN reality_enabled INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE inbounds ADD COLUMN reality_public_key TEXT`,
	`ALTER TABLE inbounds ADD COLUMN reality_private_key TEXT`,
	`ALTER TABLE inbounds ADD COLUMN reality_short_id TEXT`,
	`ALTER TABLE inbounds ADD COLUMN reality_fingerprint TEXT`,
	`ALTER TABLE inbounds ADD COLUMN reality_spider_x TEXT`,
	`ALTER TABLE inbounds ADD COLUMN reality_server TEXT`,
	`ALTER TABLE inbounds ADD COLUMN reality_server_port INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE inbounds ADD COLUMN vless_flow TEXT`,
}
