package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

func (r *userRepository) Delete(ctx context.Context, userID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return false, fmt.Errorf("delete user: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete user rows affected: %w", err)
	}
	return affected > 0, nil
}

func (r *nodeRepository) Create(ctx context.Context, node domain.Node) (domain.Node, error) {
	node.Name = strings.TrimSpace(node.Name)
	node.Host = strings.TrimSpace(node.Host)
	if node.Name == "" {
		return domain.Node{}, fmt.Errorf("node name is required")
	}
	if node.Host == "" {
		return domain.Node{}, fmt.Errorf("node host is required")
	}
	if strings.TrimSpace(string(node.Role)) == "" {
		node.Role = domain.NodeRolePrimary
	}
	switch node.Role {
	case domain.NodeRolePrimary, domain.NodeRoleNode:
	default:
		return domain.Node{}, fmt.Errorf("node role must be one of: %s, %s", domain.NodeRolePrimary, domain.NodeRoleNode)
	}
	if node.ID == "" {
		node.ID = newID()
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = time.Now().UTC()
	}

	_, err := r.db.ExecContext(
		ctx,
		`INSERT INTO nodes (id, name, host, role, ssh_user, ssh_port, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		node.ID,
		node.Name,
		node.Host,
		node.Role,
		node.SSHUser,
		node.SSHPort,
		boolToInt(node.Enabled),
		node.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isPrimaryNodeConstraintError(err) {
			return domain.Node{}, fmt.Errorf("insert node: only one primary node is allowed; update existing primary to role=node first")
		}
		return domain.Node{}, fmt.Errorf("insert node: %w", err)
	}
	return node, nil
}

func (r *nodeRepository) List(ctx context.Context) ([]domain.Node, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, host, role, ssh_user, ssh_port, enabled, created_at FROM nodes ORDER BY created_at ASC, id ASC`)
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
		if err := rows.Scan(&node.ID, &node.Name, &node.Host, &node.Role, &node.SSHUser, &node.SSHPort, &enabled, &createdAt); err != nil {
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

func (r *nodeRepository) Update(ctx context.Context, node domain.Node) (domain.Node, error) {
	node.ID = strings.TrimSpace(node.ID)
	node.Name = strings.TrimSpace(node.Name)
	node.Host = strings.TrimSpace(node.Host)
	if node.ID == "" {
		return domain.Node{}, fmt.Errorf("node id is required")
	}
	if node.Name == "" {
		return domain.Node{}, fmt.Errorf("node name is required")
	}
	if node.Host == "" {
		return domain.Node{}, fmt.Errorf("node host is required")
	}
	if strings.TrimSpace(string(node.Role)) == "" {
		node.Role = domain.NodeRolePrimary
	}

	result, err := r.db.ExecContext(
		ctx,
		`UPDATE nodes SET name = ?, host = ?, role = ?, ssh_user = ?, ssh_port = ?, enabled = ? WHERE id = ?`,
		node.Name,
		node.Host,
		node.Role,
		node.SSHUser,
		node.SSHPort,
		boolToInt(node.Enabled),
		node.ID,
	)
	if err != nil {
		if isPrimaryNodeConstraintError(err) {
			return domain.Node{}, fmt.Errorf("update node: only one primary node is allowed; update existing primary to role=node first")
		}
		return domain.Node{}, fmt.Errorf("update node: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Node{}, fmt.Errorf("update node rows affected: %w", err)
	}
	if affected == 0 {
		return domain.Node{}, sql.ErrNoRows
	}

	var (
		enabled   int
		createdAt string
	)
	if err := r.db.QueryRowContext(
		ctx,
		`SELECT id, name, host, role, ssh_user, ssh_port, enabled, created_at FROM nodes WHERE id = ?`,
		node.ID,
	).Scan(&node.ID, &node.Name, &node.Host, &node.Role, &node.SSHUser, &node.SSHPort, &enabled, &createdAt); err != nil {
		return domain.Node{}, fmt.Errorf("read updated node: %w", err)
	}
	node.Enabled = intToBool(enabled)
	node.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.Node{}, fmt.Errorf("parse node created_at: %w", err)
	}
	return node, nil
}

func isPrimaryNodeConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "only one primary node is allowed")
}

func (r *nodeRepository) Delete(ctx context.Context, nodeID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, strings.TrimSpace(nodeID))
	if err != nil {
		return false, fmt.Errorf("delete node: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete node rows affected: %w", err)
	}
	return affected > 0, nil
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
			id, type, engine, node_id, domain, port, tls_enabled, tls_cert_path, tls_key_path, transport, path, sni,
			reality_enabled, reality_public_key, reality_private_key, reality_short_id,
			reality_fingerprint, reality_spider_x, reality_server, reality_server_port, vless_flow,
			sniffing_enabled, sniffing_http, sniffing_tls, sniffing_quic, sniffing_fakedns,
			enabled, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inbound.ID,
		inbound.Type,
		inbound.Engine,
		inbound.NodeID,
		inbound.Domain,
		inbound.Port,
		boolToInt(inbound.TLSEnabled),
		inbound.TLSCertPath,
		inbound.TLSKeyPath,
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
		boolToInt(inbound.SniffingEnabled),
		boolToInt(inbound.SniffingHTTP),
		boolToInt(inbound.SniffingTLS),
		boolToInt(inbound.SniffingQUIC),
		boolToInt(inbound.SniffingFakeDNS),
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
			id, type, engine, node_id, COALESCE(domain, ''), port, tls_enabled, COALESCE(tls_cert_path, ''), COALESCE(tls_key_path, ''), COALESCE(transport, ''), COALESCE(path, ''), COALESCE(sni, ''),
			reality_enabled, COALESCE(reality_public_key, ''), COALESCE(reality_private_key, ''), COALESCE(reality_short_id, ''),
			COALESCE(reality_fingerprint, ''), COALESCE(reality_spider_x, ''), COALESCE(reality_server, ''), reality_server_port, COALESCE(vless_flow, ''),
			COALESCE(sniffing_enabled, 0), COALESCE(sniffing_http, 0), COALESCE(sniffing_tls, 0), COALESCE(sniffing_quic, 0), COALESCE(sniffing_fakedns, 0),
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
			inbound         domain.Inbound
			tls             int
			realityEnabled  int
			sniffingEnabled int
			sniffingHTTP    int
			sniffingTLS     int
			sniffingQUIC    int
			sniffingFakeDNS int
			enabled         int
			createdAt       string
		)
		if err := rows.Scan(
			&inbound.ID,
			&inbound.Type,
			&inbound.Engine,
			&inbound.NodeID,
			&inbound.Domain,
			&inbound.Port,
			&tls,
			&inbound.TLSCertPath,
			&inbound.TLSKeyPath,
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
			&sniffingEnabled,
			&sniffingHTTP,
			&sniffingTLS,
			&sniffingQUIC,
			&sniffingFakeDNS,
			&enabled,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan inbound: %w", err)
		}
		inbound.TLSEnabled = intToBool(tls)
		inbound.RealityEnabled = intToBool(realityEnabled)
		inbound.SniffingEnabled = intToBool(sniffingEnabled)
		inbound.SniffingHTTP = intToBool(sniffingHTTP)
		inbound.SniffingTLS = intToBool(sniffingTLS)
		inbound.SniffingQUIC = intToBool(sniffingQUIC)
		inbound.SniffingFakeDNS = intToBool(sniffingFakeDNS)
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

func (r *inboundRepository) Delete(ctx context.Context, inboundID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM inbounds WHERE id = ?`, strings.TrimSpace(inboundID))
	if err != nil {
		return false, fmt.Errorf("delete inbound: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete inbound rows affected: %w", err)
	}
	return affected > 0, nil
}

func (r *inboundRepository) Update(ctx context.Context, inbound domain.Inbound) (domain.Inbound, error) {
	inbound.ID = strings.TrimSpace(inbound.ID)
	if inbound.ID == "" {
		return domain.Inbound{}, fmt.Errorf("inbound id is required")
	}

	result, err := r.db.ExecContext(
		ctx,
		`UPDATE inbounds SET
			type = ?, engine = ?, node_id = ?, domain = ?, port = ?, tls_enabled = ?, tls_cert_path = ?, tls_key_path = ?,
			transport = ?, path = ?, sni = ?, reality_enabled = ?, reality_public_key = ?, reality_private_key = ?,
			reality_short_id = ?, reality_fingerprint = ?, reality_spider_x = ?, reality_server = ?, reality_server_port = ?,
			vless_flow = ?, sniffing_enabled = ?, sniffing_http = ?, sniffing_tls = ?, sniffing_quic = ?, sniffing_fakedns = ?,
			enabled = ?
		WHERE id = ?`,
		inbound.Type,
		inbound.Engine,
		inbound.NodeID,
		inbound.Domain,
		inbound.Port,
		boolToInt(inbound.TLSEnabled),
		inbound.TLSCertPath,
		inbound.TLSKeyPath,
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
		boolToInt(inbound.SniffingEnabled),
		boolToInt(inbound.SniffingHTTP),
		boolToInt(inbound.SniffingTLS),
		boolToInt(inbound.SniffingQUIC),
		boolToInt(inbound.SniffingFakeDNS),
		boolToInt(inbound.Enabled),
		inbound.ID,
	)
	if err != nil {
		return domain.Inbound{}, fmt.Errorf("update inbound: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Inbound{}, fmt.Errorf("update inbound rows affected: %w", err)
	}
	if affected == 0 {
		return domain.Inbound{}, sql.ErrNoRows
	}

	inbounds, err := r.List(ctx)
	if err != nil {
		return domain.Inbound{}, err
	}
	for _, item := range inbounds {
		if item.ID == inbound.ID {
			return item, nil
		}
	}
	return domain.Inbound{}, sql.ErrNoRows
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

func (r *credentialRepository) Update(ctx context.Context, credential domain.Credential) (domain.Credential, error) {
	credential.ID = strings.TrimSpace(credential.ID)
	if credential.ID == "" {
		return domain.Credential{}, fmt.Errorf("credential id is required")
	}

	result, err := r.db.ExecContext(
		ctx,
		`UPDATE credentials SET user_id = ?, inbound_id = ?, kind = ?, secret = ?, metadata = ? WHERE id = ?`,
		credential.UserID,
		credential.InboundID,
		credential.Kind,
		credential.Secret,
		credential.Metadata,
		credential.ID,
	)
	if err != nil {
		return domain.Credential{}, fmt.Errorf("update credential: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Credential{}, fmt.Errorf("update credential rows affected: %w", err)
	}
	if affected == 0 {
		return domain.Credential{}, sql.ErrNoRows
	}

	var createdAt string
	if err := r.db.QueryRowContext(
		ctx,
		`SELECT id, user_id, inbound_id, kind, secret, metadata, created_at FROM credentials WHERE id = ?`,
		credential.ID,
	).Scan(
		&credential.ID,
		&credential.UserID,
		&credential.InboundID,
		&credential.Kind,
		&credential.Secret,
		&credential.Metadata,
		&createdAt,
	); err != nil {
		return domain.Credential{}, fmt.Errorf("read updated credential: %w", err)
	}
	credential.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.Credential{}, fmt.Errorf("parse credential created_at: %w", err)
	}
	return credential, nil
}

func (r *credentialRepository) Delete(ctx context.Context, credentialID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, credentialID)
	if err != nil {
		return false, fmt.Errorf("delete credential: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete credential rows affected: %w", err)
	}
	return affected > 0, nil
}

func (r *credentialRepository) DeleteByUserID(ctx context.Context, userID string) (int, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM credentials WHERE user_id = ?`, userID)
	if err != nil {
		return 0, fmt.Errorf("delete credentials by user id: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete credentials rows affected: %w", err)
	}
	return int(affected), nil
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
		`INSERT INTO subscriptions (id, user_id, format, output_path, access_token, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			format = excluded.format,
			output_path = excluded.output_path,
			access_token = excluded.access_token,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at`,
		subscription.ID,
		subscription.UserID,
		subscription.Format,
		subscription.OutputPath,
		subscription.AccessToken,
		boolToInt(subscription.Enabled),
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
		enabled      int
		updatedAt    string
	)

	err := r.db.QueryRowContext(
		ctx,
		`SELECT id, user_id, format, output_path, COALESCE(access_token, ''), enabled, updated_at FROM subscriptions WHERE user_id = ?`,
		userID,
	).Scan(
		&subscription.ID,
		&subscription.UserID,
		&subscription.Format,
		&subscription.OutputPath,
		&subscription.AccessToken,
		&enabled,
		&updatedAt,
	)
	if err != nil {
		return domain.Subscription{}, fmt.Errorf("get subscription by user id: %w", err)
	}

	subscription.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return domain.Subscription{}, fmt.Errorf("parse subscription updated_at: %w", err)
	}
	subscription.Enabled = intToBool(enabled)

	return subscription, nil
}

func (r *subscriptionRepository) DeleteByUserID(ctx context.Context, userID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE user_id = ?`, userID)
	if err != nil {
		return false, fmt.Errorf("delete subscription by user id: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete subscription rows affected: %w", err)
	}
	return affected > 0, nil
}
