package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrAlreadyInitialized = errors.New("пользователи уже есть")

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
	DisabledAt   *time.Time
}

const userCols = `id, username, password_hash, is_admin, created_at, disabled_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created int64
	var disabled sql.NullInt64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &created, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = fromUnix(created)
	u.DisabledAt = fromNullUnix(disabled)
	return u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, isAdmin bool, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, isAdmin, unix(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateFirstAdmin создаёт админа, только если пользователей ещё нет.
// Иначе ErrAlreadyInitialized: два параллельных /setup не создадут двух админов.
func (s *Store) CreateFirstAdmin(ctx context.Context, username, passwordHash string, now time.Time) (int64, error) {
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadyInitialized
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, 1, ?)`,
			username, passwordHash, unix(now))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}
