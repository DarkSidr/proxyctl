package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"proxyctl/internal/domain"
)

type userRepository struct{ db *sql.DB }

type nodeRepository struct{ db *sql.DB }

type inboundRepository struct{ db *sql.DB }

type credentialRepository struct{ db *sql.DB }

type subscriptionRepository struct{ db *sql.DB }

func (r *userRepository) Create(ctx context.Context, user domain.User) (domain.User, error) {
	if user.ID == "" {
		user.ID = newID()
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}

	_, err := r.db.ExecContext(
		ctx,
		`INSERT INTO users (id, name, enabled, created_at) VALUES (?, ?, ?, ?)`,
		user.ID,
		user.Name,
		boolToInt(user.Enabled),
		user.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}
	return user, nil
}

func (r *userRepository) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, enabled, created_at FROM users ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]domain.User, 0)
	for rows.Next() {
		var (
			user      domain.User
			enabled   int
			createdAt string
		)
		if err := rows.Scan(&user.ID, &user.Name, &enabled, &createdAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		user.Enabled = intToBool(enabled)
		user.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse user created_at: %w", err)
		}
		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}

func (r *nodeRepository) Create(ctx context.Context, node domain.Node) (domain.Node, error) {
	if node.ID == "" {
		node.ID = newID()
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = time.Now().UTC()
	}

	_, err := r.db.ExecContext(
		ctx,
		`INSERT INTO nodes (id, name, host, role, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		node.ID,
		node.Name,
		node.Host,
		node.Role,
		boolToInt(node.Enabled),
		node.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.Node{}, fmt.Errorf("insert node: %w", err)
	}
	return node, nil
}

func (r *nodeRepository) List(ctx context.Context) ([]domain.Node, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, host, role, enabled, created_at FROM nodes ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]domain.Node, 0)
	for rows.Next() {
		var (
			node      domain.Node
			enabled   int
			createdAt string
		)
		if err := rows.Scan(&node.ID, &node.Name, &node.Host, &node.Role, &enabled, &createdAt); err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		node.Enabled = intToBool(enabled)
		node.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse node created_at: %w", err)
		}
		nodes = append(nodes, node)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes: %w", err)
	}
	return nodes, nil
}

func (r *inboundRepository) Create(ctx context.Context, inbound domain.Inbound) (domain.Inbound, error) {
	if inbound.ID == "" {
		inbound.ID = newID()
	}
	if inbound.CreatedAt.IsZero() {
		inbound.CreatedAt = time.Now().UTC()
	}

	_, err := r.db.ExecContext(
		ctx,
		`INSERT INTO inbounds (
			id, type, engine, node_id, domain, port, tls_enabled, transport, path, sni,
			reality_enabled, reality_public_key, reality_private_key, reality_short_id,
			reality_fingerprint, reality_spider_x, reality_server, reality_server_port, vless_flow,
			enabled, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inbound.ID,
		inbound.Type,
		inbound.Engine,
		inbound.NodeID,
		inbound.Domain,
		inbound.Port,
		boolToInt(inbound.TLSEnabled),
		inbound.Transport,
		inbound.Path,
		inbound.SNI,
		boolToInt(inbound.RealityEnabled),
		inbound.RealityPublicKey,
		inbound.RealityPrivateKey,
		inbound.RealityShortID,
		inbound.RealityFingerprint,
		inbound.RealitySpiderX,
		inbound.RealityServer,
		inbound.RealityServerPort,
		inbound.VLESSFlow,
		boolToInt(inbound.Enabled),
		inbound.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.Inbound{}, fmt.Errorf("insert inbound: %w", err)
	}
	return inbound, nil
}

func (r *inboundRepository) List(ctx context.Context) ([]domain.Inbound, error) {
	rows, err := r.db.QueryContext(
		ctx,
		`SELECT
			id, type, engine, node_id, domain, port, tls_enabled, transport, path, sni,
			reality_enabled, reality_public_key, reality_private_key, reality_short_id,
			reality_fingerprint, reality_spider_x, reality_server, reality_server_port, vless_flow,
			enabled, created_at
		FROM inbounds ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list inbounds: %w", err)
	}
	defer rows.Close()

	inbounds := make([]domain.Inbound, 0)
	for rows.Next() {
		var (
			inbound        domain.Inbound
			tls            int
			realityEnabled int
			enabled        int
			createdAt      string
		)
		if err := rows.Scan(
			&inbound.ID,
			&inbound.Type,
			&inbound.Engine,
			&inbound.NodeID,
			&inbound.Domain,
			&inbound.Port,
			&tls,
			&inbound.Transport,
			&inbound.Path,
			&inbound.SNI,
			&realityEnabled,
			&inbound.RealityPublicKey,
			&inbound.RealityPrivateKey,
			&inbound.RealityShortID,
			&inbound.RealityFingerprint,
			&inbound.RealitySpiderX,
			&inbound.RealityServer,
			&inbound.RealityServerPort,
			&inbound.VLESSFlow,
			&enabled,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan inbound: %w", err)
		}
		inbound.TLSEnabled = intToBool(tls)
		inbound.RealityEnabled = intToBool(realityEnabled)
		inbound.Enabled = intToBool(enabled)
		inbound.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse inbound created_at: %w", err)
		}
		inbounds = append(inbounds, inbound)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbounds: %w", err)
	}
	return inbounds, nil
}

func (r *credentialRepository) Create(ctx context.Context, credential domain.Credential) (domain.Credential, error) {
	if credential.ID == "" {
		credential.ID = newID()
	}
	if credential.CreatedAt.IsZero() {
		credential.CreatedAt = time.Now().UTC()
	}

	_, err := r.db.ExecContext(
		ctx,
		`INSERT INTO credentials (id, user_id, inbound_id, kind, secret, metadata, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		credential.ID,
		credential.UserID,
		credential.InboundID,
		credential.Kind,
		credential.Secret,
		credential.Metadata,
		credential.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.Credential{}, fmt.Errorf("insert credential: %w", err)
	}
	return credential, nil
}

func (r *credentialRepository) List(ctx context.Context) ([]domain.Credential, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, user_id, inbound_id, kind, secret, metadata, created_at FROM credentials ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()

	credentials := make([]domain.Credential, 0)
	for rows.Next() {
		var (
			credential domain.Credential
			createdAt  string
		)
		if err := rows.Scan(
			&credential.ID,
			&credential.UserID,
			&credential.InboundID,
			&credential.Kind,
			&credential.Secret,
			&credential.Metadata,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}

		credential.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse credential created_at: %w", err)
		}
		credentials = append(credentials, credential)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credentials: %w", err)
	}
	return credentials, nil
}

func (r *subscriptionRepository) Upsert(ctx context.Context, subscription domain.Subscription) (domain.Subscription, error) {
	if subscription.ID == "" {
		subscription.ID = newID()
	}
	if subscription.UpdatedAt.IsZero() {
		subscription.UpdatedAt = time.Now().UTC()
	}

	_, err := r.db.ExecContext(
		ctx,
		`INSERT INTO subscriptions (id, user_id, format, output_path, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			format = excluded.format,
			output_path = excluded.output_path,
			updated_at = excluded.updated_at`,
		subscription.ID,
		subscription.UserID,
		subscription.Format,
		subscription.OutputPath,
		subscription.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.Subscription{}, fmt.Errorf("upsert subscription: %w", err)
	}
	return subscription, nil
}

func (r *subscriptionRepository) GetByUserID(ctx context.Context, userID string) (domain.Subscription, error) {
	var (
		subscription domain.Subscription
		updatedAt    string
	)

	err := r.db.QueryRowContext(
		ctx,
		`SELECT id, user_id, format, output_path, updated_at FROM subscriptions WHERE user_id = ?`,
		userID,
	).Scan(
		&subscription.ID,
		&subscription.UserID,
		&subscription.Format,
		&subscription.OutputPath,
		&updatedAt,
	)
	if err != nil {
		return domain.Subscription{}, fmt.Errorf("get subscription by user id: %w", err)
	}

	subscription.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return domain.Subscription{}, fmt.Errorf("parse subscription updated_at: %w", err)
	}

	return subscription, nil
}
