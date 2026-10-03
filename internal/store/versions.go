package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Version — строка истории списка со сводкой изменений.
type Version struct {
	ID         int64
	Version    int64
	Username   string
	Source     string
	Message    string
	ProposalID int64
	CreatedAt  time.Time
	Added      int
	Removed    int
	Updated    int
}

const versionSelect = `
	SELECT v.id, v.version, coalesce(u.username, ''), v.source, v.message, coalesce(v.proposal_id, 0), v.created_at,
	       (SELECT count(*) FROM entry_changes c WHERE c.version_id = v.id AND c.op = 'add'),
	       (SELECT count(*) FROM entry_changes c WHERE c.version_id = v.id AND c.op = 'remove'),
	       (SELECT count(*) FROM entry_changes c WHERE c.version_id = v.id AND c.op = 'update')
	FROM list_versions v LEFT JOIN users u ON u.id = v.user_id`

func scanVersion(row interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var created int64
	err := row.Scan(&v.ID, &v.Version, &v.Username, &v.Source, &v.Message, &v.ProposalID, &created,
		&v.Added, &v.Removed, &v.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	v.CreatedAt = fromUnix(created)
	return v, err
}

// Versions — версии списка от новых к старым. before > 0 — только версии меньше before (подгрузка «ещё»).
func (s *Store) Versions(ctx context.Context, listID, before int64, limit int) ([]Version, error) {
	q := versionSelect + ` WHERE v.list_id = ?`
	args := []any{listID}
	if before > 0 {
		q += ` AND v.version < ?`
		args = append(args, before)
	}
	q += ` ORDER BY v.version DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VersionWithChanges — одна версия и её изменения в порядке применения.
func (s *Store) VersionWithChanges(ctx context.Context, listID, version int64) (Version, []Change, error) {
	v, err := scanVersion(s.db.QueryRowContext(ctx, versionSelect+` WHERE v.list_id = ? AND v.version = ?`, listID, version))
	if err != nil {
		return Version{}, nil, err
	}
	changes, err := queryChanges(ctx, s.db, `WHERE version_id = ?`, v.ID)
	return v, changes, err
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryChanges(ctx context.Context, q querier, where string, args ...any) ([]Change, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT op, value, kind, old_comment, new_comment, old_enabled, new_enabled
		FROM entry_changes `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var oc, nc sql.NullString
		var oe, ne sql.NullBool
		if err := rows.Scan(&c.Op, &c.Value, &c.Kind, &oc, &nc, &oe, &ne); err != nil {
			return nil, err
		}
		if oc.Valid {
			c.OldComment = ptr(oc.String)
		}
		if nc.Valid {
			c.NewComment = ptr(nc.String)
		}
		if oe.Valid {
			c.OldEnabled = ptr(oe.Bool)
		}
		if ne.Valid {
			c.NewEnabled = ptr(ne.Bool)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CurrentVersion — номер текущей версии списка внутри транзакции.
func (m *Mutation) CurrentVersion() (int64, error) {
	var v int64
	err := m.tx.QueryRowContext(m.ctx, `SELECT version FROM lists WHERE id = ?`, m.listID).Scan(&v)
	return v, err
}

// Entries — все записи списка внутри транзакции.
func (m *Mutation) Entries() ([]Entry, error) {
	rows, err := m.tx.QueryContext(m.ctx, entrySelect+` WHERE e.list_id = ?`, m.listID)
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

// ChangesAfter — изменения всех версий новее version, сгруппированные по версиям: от новых к старым,
// внутри версии — в порядке применения.
func (m *Mutation) ChangesAfter(version int64) ([][]Change, error) {
	rows, err := m.tx.QueryContext(m.ctx,
		`SELECT id FROM list_versions WHERE list_id = ? AND version > ? ORDER BY version DESC`, m.listID, version)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([][]Change, 0, len(ids))
	for _, id := range ids {
		cs, err := queryChanges(m.ctx, m.tx, `WHERE version_id = ?`, id)
		if err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	return out, nil
}

// EntryByValue — запись списка по значению внутри транзакции.
func (m *Mutation) EntryByValue(value string) (Entry, error) {
	return scanEntry(m.tx.QueryRowContext(m.ctx, entrySelect+` WHERE e.list_id = ? AND e.value = ?`, m.listID, value))
}

// AddWith — как Add, но с заданной включённостью (для отката).
func (m *Mutation) AddWith(value, kind, comment string, enabled bool) (bool, error) {
	res, err := m.tx.ExecContext(m.ctx,
		`INSERT INTO entries (list_id, value, kind, comment, enabled, added_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (list_id, value) DO NOTHING`,
		m.listID, value, kind, comment, enabled, nullID(m.userID), unix(m.now), unix(m.now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	m.changes = append(m.changes, Change{Op: "add", Value: value, Kind: kind, NewComment: ptr(comment), NewEnabled: ptr(enabled)})
	return true, nil
}
