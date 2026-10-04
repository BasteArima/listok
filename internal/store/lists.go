package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrSlugTaken = errors.New("такой адрес списка уже занят")

type List struct {
	ID           int64
	Slug         string
	Title        string
	Description  string
	OwnerID      int64
	OwnerName    string
	Kind         string
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	EntryCount   int
	EnabledCount int
	// Role — роль запросившего пользователя: owner, editor, viewer или admin (админ без явной роли).
	Role string
}

const listSelect = `
	SELECT l.id, l.slug, l.title, l.description, l.owner_id, u.username, l.kind, l.version,
	       l.created_at, l.updated_at,
	       (SELECT count(*) FROM entries e WHERE e.list_id = l.id),
	       (SELECT count(*) FROM entries e WHERE e.list_id = l.id AND e.enabled = 1),
	       CASE WHEN l.owner_id = :uid THEN 'owner'
	            ELSE coalesce((SELECT m.role FROM list_members m WHERE m.list_id = l.id AND m.user_id = :uid),
	                          CASE WHEN :admin THEN 'admin' ELSE '' END)
	       END AS role
	FROM lists l JOIN users u ON u.id = l.owner_id`

func scanList(row interface{ Scan(...any) error }) (List, error) {
	var l List
	var created, updated int64
	err := row.Scan(&l.ID, &l.Slug, &l.Title, &l.Description, &l.OwnerID, &l.OwnerName, &l.Kind, &l.Version,
		&created, &updated, &l.EntryCount, &l.EnabledCount, &l.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, ErrNotFound
	}
	l.CreatedAt, l.UpdatedAt = fromUnix(created), fromUnix(updated)
	return l, err
}

// VisibleLists — неархивные списки, которые пользователь видит: свои, где он участник, или все для админа.
func (s *Store) VisibleLists(ctx context.Context, userID int64, isAdmin bool) ([]List, error) {
	rows, err := s.db.QueryContext(ctx, listSelect+`
		WHERE l.archived_at IS NULL
		  AND (:admin OR l.owner_id = :uid OR EXISTS (SELECT 1 FROM list_members m WHERE m.list_id = l.id AND m.user_id = :uid))
		ORDER BY l.title COLLATE NOCASE`,
		sql.Named("uid", userID), sql.Named("admin", isAdmin))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []List
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListBySlug возвращает список с ролью пользователя. Role == "" — пользователь его не видит.
func (s *Store) ListBySlug(ctx context.Context, slug string, userID int64, isAdmin bool) (List, error) {
	return scanList(s.db.QueryRowContext(ctx, listSelect+` WHERE l.slug = :slug AND l.archived_at IS NULL`,
		sql.Named("slug", slug), sql.Named("uid", userID), sql.Named("admin", isAdmin)))
}

// ListFeedCount — в скольких фидах список: напрямую или как вложенный в подключённый список.
func (s *Store) ListFeedCount(ctx context.Context, listID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(DISTINCT feed_id) FROM feed_lists
		WHERE list_id = :id OR list_id IN (SELECT parent_id FROM list_includes WHERE child_id = :id)`,
		sql.Named("id", listID)).Scan(&n)
	return n, err
}

// DeleteList удаляет список насовсем. Записи, история, права, вложения и место в фидах
// уходят каскадом (foreign_keys включены в db.Open).
func (s *Store) DeleteList(ctx context.Context, listID int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		// Предложения «подключить этот список» ссылаются на него без каскада.
		if _, err := tx.ExecContext(ctx, `DELETE FROM proposals WHERE source_list_id = ?`, listID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM lists WHERE id = ?`, listID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err == nil {
				err = ErrNotFound
			}
			return err
		}
		return nil
	})
}

func (s *Store) CreateList(ctx context.Context, slug, title, description string, ownerID int64, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO lists (slug, title, description, owner_id, kind, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'manual', ?, ?)`,
		slug, title, description, ownerID, unix(now), unix(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: lists.slug") {
			return 0, ErrSlugTaken
		}
		return 0, err
	}
	return res.LastInsertId()
}
