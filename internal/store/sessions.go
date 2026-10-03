package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Session struct {
	IDHash     string
	UserID     int64
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, idHash string, userID int64, now, expires time.Time, ip, userAgent string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		idHash, userID, unix(now), unix(expires), unix(now), ip, userAgent)
	return err
}

// SessionWithUser возвращает живую сессию и её пользователя. Просроченная сессия = ErrNotFound.
func (s *Store) SessionWithUser(ctx context.Context, idHash string, now time.Time) (Session, User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT s.id_hash, s.user_id, s.created_at, s.expires_at, s.last_seen_at,
		       u.id, u.username, u.password_hash, u.is_admin, u.created_at, u.disabled_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ? AND s.expires_at > ?`, idHash, unix(now))

	var sess Session
	var created, expires, seen int64
	var u User
	var uCreated int64
	var uDisabled sql.NullInt64
	err := row.Scan(&sess.IDHash, &sess.UserID, &created, &expires, &seen,
		&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &uCreated, &uDisabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, User{}, ErrNotFound
		}
		return Session{}, User{}, err
	}
	sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt = fromUnix(created), fromUnix(expires), fromUnix(seen)
	u.CreatedAt = fromUnix(uCreated)
	u.DisabledAt = fromNullUnix(uDisabled)
	return sess, u, nil
}

// TouchSession продлевает сессию (скользящий срок).
func (s *Store) TouchSession(ctx context.Context, idHash string, now, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id_hash = ?`,
		unix(now), unix(expires), idHash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, unix(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
