package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// API-токены: расширение браузера, команды на iPad, скрипты. В БД только sha256 и первые символы (D-006).

type APIToken struct {
	ID         int64
	Name       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name, hash, prefix string, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (user_id, name, token_hash, prefix, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, name, hash, prefix, unix(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// APITokens — действующие токены пользователя, новые сверху.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, prefix, created_at, last_used_at FROM api_tokens
		WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &created, &used); err != nil {
			return nil, err
		}
		t.CreatedAt = fromUnix(created)
		t.LastUsedAt = fromNullUnix(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken отзывает токен этого пользователя. Чужой или уже отозванный — ErrNotFound.
func (s *Store) RevokeAPIToken(ctx context.Context, id, userID int64, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		unix(now), id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UseAPIToken отмечает запрос по токену и возвращает его владельца. Неизвестный или отозванный — ErrNotFound.
func (s *Store) UseAPIToken(ctx context.Context, hash string, now time.Time) (User, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx,
		`UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ? AND revoked_at IS NULL RETURNING user_id`,
		unix(now), hash).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, userID)
}
