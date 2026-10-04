package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Агент на роутере: ссылка установки, токен агента, отчёты. Токены хранятся только как sha256 (D-006).

// SetInstallToken выдаёт роутеру одноразовую ссылку установки (хеш токена и срок).
func (s *Store) SetInstallToken(ctx context.Context, routerID int64, hash string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE routers SET install_token_hash = ?, install_expires_at = ? WHERE id = ?`, hash, unix(expires), routerID)
	return err
}

// RouterByInstallToken — роутер по действующей (не просроченной) ссылке установки.
func (s *Store) RouterByInstallToken(ctx context.Context, hash string, now time.Time) (Router, error) {
	return scanRouter(s.db.QueryRowContext(ctx,
		routerSelect+` WHERE r.install_token_hash = ? AND r.install_expires_at > ?`, hash, unix(now)))
}

// ConsumeInstallToken меняет действующую ссылку установки на токен агента — атомарно, один раз.
// Просроченная или уже использованная ссылка — ErrNotFound. Возвращает id роутера.
func (s *Store) ConsumeInstallToken(ctx context.Context, installHash, agentHash string, now time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE routers SET agent_token_hash = ?, install_token_hash = NULL, install_expires_at = NULL
		WHERE install_token_hash = ? AND install_expires_at > ?
		RETURNING id`, agentHash, installHash, unix(now)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// RouterByAgentToken — роутер по хешу токена агента.
func (s *Store) RouterByAgentToken(ctx context.Context, hash string) (Router, error) {
	return scanRouter(s.db.QueryRowContext(ctx, routerSelect+` WHERE r.agent_token_hash = ?`, hash))
}

// RevokeAgent забывает токен агента: агент роутера больше не сможет отчитываться.
func (s *Store) RevokeAgent(ctx context.Context, routerID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE routers SET agent_token_hash = NULL WHERE id = ?`, routerID)
	return err
}

// AgentInfo — то, что агент сообщает о роутере в hello. Пустые поля не перезаписывают известные.
type AgentInfo struct {
	AgentVersion   string
	ForkopVersion  string
	SingboxVersion string
}

// TouchAgent отмечает запрос агента: время, адрес и (если пришли) версии.
func (s *Store) TouchAgent(ctx context.Context, routerID int64, now time.Time, ip string, info AgentInfo) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE routers SET last_seen_at = ?, last_ip = ?,
		       agent_version   = coalesce(nullif(?, ''), agent_version),
		       forkop_version  = coalesce(nullif(?, ''), forkop_version),
		       singbox_version = coalesce(nullif(?, ''), singbox_version)
		WHERE id = ?`,
		unix(now), nullStr(ip), info.AgentVersion, info.ForkopVersion, info.SingboxVersion, routerID)
	return err
}

// FeedBySection — фид роутера для секции forkop.
func (s *Store) FeedBySection(ctx context.Context, routerID int64, section string) (Feed, error) {
	return scanFeed(s.db.QueryRowContext(ctx, feedSelect+` WHERE f.router_id = ? AND f.section = ?`, routerID, section))
}

// SetApplied записывает отчёт агента о применении версии фида.
func (s *Store) SetApplied(ctx context.Context, feedID int64, etag string, at time.Time, ok bool, errText string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE feeds SET applied_etag = ?, applied_at = ?, applied_ok = ?, applied_error = ? WHERE id = ?`,
		etag, unix(at), ok, nullStr(errText), feedID)
	return err
}
