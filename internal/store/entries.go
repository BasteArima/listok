package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Entry struct {
	ID          int64
	ListID      int64
	Value       string
	Kind        string
	Comment     string
	Enabled     bool
	ExpiresAt   *time.Time
	AddedBy     int64
	AddedByName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const entrySelect = `
	SELECT e.id, e.list_id, e.value, e.kind, e.comment, e.enabled, e.expires_at,
	       coalesce(e.added_by, 0), coalesce(u.username, ''), e.created_at, e.updated_at
	FROM entries e LEFT JOIN users u ON u.id = e.added_by`

func scanEntry(row interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var expires sql.NullInt64
	var created, updated int64
	err := row.Scan(&e.ID, &e.ListID, &e.Value, &e.Kind, &e.Comment, &e.Enabled, &expires,
		&e.AddedBy, &e.AddedByName, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	e.ExpiresAt = fromNullUnix(expires)
	e.CreatedAt, e.UpdatedAt = fromUnix(created), fromUnix(updated)
	return e, err
}

type EntryFilter struct {
	Query string // подстрока в значении или комментарии
	Kind  string // "", "domain", "cidr" (оба семейства), "cidr4", "cidr6"
	Limit int
}

// Entries — записи списка: сначала новые.
func (s *Store) Entries(ctx context.Context, listID int64, f EntryFilter) ([]Entry, error) {
	where := []string{"e.list_id = ?"}
	args := []any{listID}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(strings.ToLower(q)) + "%"
		where = append(where, `(e.value LIKE ? ESCAPE '\' OR lower(e.comment) LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	switch f.Kind {
	case "domain", "cidr4", "cidr6":
		where = append(where, "e.kind = ?")
		args = append(args, f.Kind)
	case "cidr":
		where = append(where, "e.kind IN ('cidr4','cidr6')")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		entrySelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY e.created_at DESC, e.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EntryByID(ctx context.Context, listID, id int64) (Entry, error) {
	return scanEntry(s.db.QueryRowContext(ctx, entrySelect+` WHERE e.list_id = ? AND e.id = ?`, listID, id))
}

// IndexEntry — минимум для индекса покрытия.
type IndexEntry struct {
	ListID int64
	Value  string
	Kind   string
}

// EnabledEntries — включённые записи всех неархивных списков. Загружаются в индекс при старте.
func (s *Store) EnabledEntries(ctx context.Context) ([]IndexEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.list_id, e.value, e.kind FROM entries e JOIN lists l ON l.id = e.list_id
		WHERE e.enabled = 1 AND l.archived_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexEntry
	for rows.Next() {
		var e IndexEntry
		if err := rows.Scan(&e.ListID, &e.Value, &e.Kind); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
