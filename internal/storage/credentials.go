package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"vps-node-auditor/internal/domain"
)

// ListCredentials returns redacted credential metadata and never selects UUID.
// 中文：ListCredentials 返回脱敏的凭据元数据，并且查询本身不读取 UUID。
func (s *Store) ListCredentials(ctx context.Context, userName string) ([]domain.CredentialSummary, error) {
	query := `SELECT u.display_name, c.display_name, c.enabled, c.created_at, c.revoked_at
		FROM credentials c JOIN users u ON u.id=c.user_id`
	var args []any
	if userName != "" {
		query += " WHERE u.display_name=?"
		args = append(args, userName)
	}
	query += " ORDER BY u.display_name, c.created_at, c.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()
	var result []domain.CredentialSummary
	for rows.Next() {
		var item domain.CredentialSummary
		var enabled int
		var created int64
		var revoked sql.NullInt64
		if err := rows.Scan(&item.UserName, &item.CredentialName, &enabled, &created, &revoked); err != nil {
			return nil, fmt.Errorf("scan credential list: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = time.Unix(created, 0).UTC()
		if revoked.Valid {
			value := time.Unix(revoked.Int64, 0).UTC()
			item.RevokedAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ActiveCredential resolves one active credential for an owner operation. If
// more than one is active, credentialName is mandatory.
// 中文：ActiveCredential 为所有者操作解析一个活动凭据；存在多个活动凭据时，
// 必须明确提供 credentialName。
func (s *Store) ActiveCredential(ctx context.Context, userName, credentialName string) (domain.Credential, error) {
	query := `SELECT c.id, c.user_id, u.display_name, c.display_name, c.uuid,
		COALESCE(c.stats_user,''), c.enabled, c.created_at
		FROM credentials c JOIN users u ON u.id=c.user_id
		WHERE u.display_name=? AND c.enabled=1`
	args := []any{userName}
	if credentialName != "" {
		query += " AND c.display_name=?"
		args = append(args, credentialName)
	}
	query += " ORDER BY c.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.Credential{}, fmt.Errorf("resolve active credential: %w", err)
	}
	defer rows.Close()
	var found []domain.Credential
	for rows.Next() {
		var item domain.Credential
		var enabled int
		var created int64
		if err := rows.Scan(&item.ID, &item.UserID, &item.UserName, &item.DisplayName,
			&item.UUID, &item.StatsUser, &enabled, &created); err != nil {
			return domain.Credential{}, fmt.Errorf("scan active credential: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = time.Unix(created, 0).UTC()
		found = append(found, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Credential{}, err
	}
	if len(found) == 0 {
		return domain.Credential{}, errors.New("no matching active credential")
	}
	if len(found) > 1 {
		return domain.Credential{}, errors.New("multiple active credentials exist; specify --credential")
	}
	return found[0], nil
}

// ActiveCredentials returns every active credential for one user. UUIDs are
// internal-only and callers must not print or log the returned values.
func (s *Store) ActiveCredentials(ctx context.Context, userName string) ([]domain.Credential, error) {
	query := `SELECT c.id, c.user_id, u.display_name,
		c.display_name, c.uuid, COALESCE(c.stats_user,''), c.enabled, c.created_at
		FROM credentials c JOIN users u ON u.id=c.user_id
		WHERE c.enabled=1`
	var args []any
	if userName != "" {
		query += " AND u.display_name=?"
		args = append(args, userName)
	}
	query += " ORDER BY c.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list active credentials: %w", err)
	}
	defer rows.Close()
	var result []domain.Credential
	for rows.Next() {
		var item domain.Credential
		var enabled int
		var created int64
		if err := rows.Scan(&item.ID, &item.UserID, &item.UserName, &item.DisplayName,
			&item.UUID, &item.StatsUser, &enabled, &created); err != nil {
			return nil, fmt.Errorf("scan active credentials: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = time.Unix(created, 0).UTC()
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 && userName != "" {
		return nil, errors.New("user has no active credentials")
	}
	return result, nil
}

// AddCredential commits one new active credential after external core
// validation has succeeded.
// 中文：AddCredential 在外部核心验证成功后提交一个新的活动凭据。
func (s *Store) AddCredential(ctx context.Context, userName, credentialName, uuid, statsUser string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin add credential: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(display_name, created_at)
		VALUES(?, ?) ON CONFLICT(display_name) DO NOTHING`, userName, now.UTC().Unix()); err != nil {
		return fmt.Errorf("ensure provisioned user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credentials(
		user_id, display_name, uuid, stats_user, enabled, created_at)
		SELECT id, ?, ?, ?, 1, ? FROM users WHERE display_name=?`,
		credentialName, uuid, statsUser, now.UTC().Unix(), userName); err != nil {
		return fmt.Errorf("insert provisioned credential: %w", err)
	}
	// Root-run provision operations can cause SQLite to create root-owned WAL
	// files. Repair ownership before commit so a failure still rolls back the
	// logical credential change instead of leaving runtime and SQLite divergent.
	if err := s.secureFiles(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provisioned credential: %w", err)
	}
	return nil
}

// RotateCredential preserves the old row as revoked history and inserts the
// replacement atomically.
// 中文：RotateCredential 将旧行保留为已撤销历史，并原子插入替代凭据。
func (s *Store) RotateCredential(ctx context.Context, oldID int64, credentialName, uuid, statsUser string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rotate credential: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE credentials SET enabled=0, revoked_at=?
		WHERE id=? AND enabled=1`, now.UTC().Unix(), oldID)
	if err != nil {
		return fmt.Errorf("revoke old credential: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("old credential is no longer active")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credentials(
		user_id, display_name, uuid, stats_user, enabled, created_at)
		SELECT user_id, ?, ?, ?, 1, ? FROM credentials WHERE id=?`,
		credentialName, uuid, statsUser, now.UTC().Unix(), oldID); err != nil {
		return fmt.Errorf("insert rotated credential: %w", err)
	}
	if err := s.secureFiles(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rotated credential: %w", err)
	}
	return nil
}

// DisableCredential revokes one active credential while preserving history.
// 中文：DisableCredential 停用一个活动凭据，同时保留历史记录。
func (s *Store) DisableCredential(ctx context.Context, id int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin disable credential: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE credentials SET enabled=0, revoked_at=?
		WHERE id=? AND enabled=1`, now.UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("disable credential: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("credential is no longer active")
	}
	if err := s.secureFiles(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit disabled credential: %w", err)
	}
	return nil
}

// DisableCredentials revokes a known set of active credentials in one
// transaction while retaining all user, credential, and traffic history.
func (s *Store) DisableCredentials(ctx context.Context, ids []int64, now time.Time) error {
	if len(ids) == 0 {
		return errors.New("no active credentials to disable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin disable credentials: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE credentials SET enabled=0, revoked_at=?
			WHERE id=? AND enabled=1`, now.UTC().Unix(), id)
		if err != nil {
			return fmt.Errorf("disable credential set: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("credential set changed during removal")
		}
	}
	if err := s.secureFiles(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit disabled credentials: %w", err)
	}
	return nil
}
