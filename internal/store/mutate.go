package store

import (
	"context"
	"database/sql"
	"time"
)

// Change — одна строка истории (entry_changes).
type Change struct {
	Op         string // add, remove, update
	Value      string
	Kind       string
	OldComment *string
	NewComment *string
	OldEnabled *bool
	NewEnabled *bool
}

// Mutation — набор изменений одного списка внутри транзакции.
// Каждое изменение содержимого записывается в историю: одна Mutate = одна версия списка.
type Mutation struct {
	ctx     context.Context
	tx      *sql.Tx
	listID  int64
	userID  int64
	now     time.Time
	changes []Change
}

type MutateOptions struct {
	ListID     int64
	UserID     int64 // 0 = система
	Source     string
	Message    string
	ProposalID int64
	Now        time.Time
}

// Mutate выполняет fn в транзакции. Если fn что-то изменила, поднимает версию списка
// и пишет list_versions + entry_changes. Возвращает новую версию (0, если изменений не было) и изменения.
func (s *Store) Mutate(ctx context.Context, o MutateOptions, fn func(*Mutation) error) (int64, []Change, error) {
	var version int64
	var changes []Change
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		m := &Mutation{ctx: ctx, tx: tx, listID: o.ListID, userID: o.UserID, now: o.Now}
		if err := fn(m); err != nil {
			return err
		}
		if len(m.changes) == 0 {
			return nil
		}
		if err := tx.QueryRowContext(ctx,
			`UPDATE lists SET version = version + 1, updated_at = ? WHERE id = ? RETURNING version`,
			unix(o.Now), o.ListID).Scan(&version); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO list_versions (list_id, version, user_id, source, message, proposal_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			o.ListID, version, nullID(o.UserID), o.Source, o.Message, nullID(o.ProposalID), unix(o.Now))
		if err != nil {
			return err
		}
		versionID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, c := range m.changes {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO entry_changes (version_id, op, value, kind, old_comment, new_comment, old_enabled, new_enabled)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				versionID, c.Op, c.Value, c.Kind, c.OldComment, c.NewComment, c.OldEnabled, c.NewEnabled); err != nil {
				return err
			}
		}
		changes = m.changes
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return version, changes, nil
}

// Has — есть ли значение в списке (в любом состоянии).
func (m *Mutation) Has(value string) (bool, error) {
	var n int
	err := m.tx.QueryRowContext(m.ctx, `SELECT count(*) FROM entries WHERE list_id = ? AND value = ?`, m.listID, value).Scan(&n)
	return n > 0, err
}

// Add добавляет запись. false — такое значение в списке уже есть.
func (m *Mutation) Add(value, kind, comment string) (bool, error) {
	res, err := m.tx.ExecContext(m.ctx,
		`INSERT INTO entries (list_id, value, kind, comment, added_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (list_id, value) DO NOTHING`,
		m.listID, value, kind, comment, nullID(m.userID), unix(m.now), unix(m.now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	m.changes = append(m.changes, Change{Op: "add", Value: value, Kind: kind, NewComment: ptr(comment), NewEnabled: ptr(true)})
	return true, nil
}

// Remove удаляет запись по id. Возвращает удалённую запись.
func (m *Mutation) Remove(id int64) (Entry, error) {
	e, err := scanEntry(m.tx.QueryRowContext(m.ctx, entrySelect+` WHERE e.list_id = ? AND e.id = ?`, m.listID, id))
	if err != nil {
		return Entry{}, err
	}
	if _, err := m.tx.ExecContext(m.ctx, `DELETE FROM entries WHERE id = ?`, id); err != nil {
		return Entry{}, err
	}
	m.changes = append(m.changes, Change{Op: "remove", Value: e.Value, Kind: e.Kind, OldComment: ptr(e.Comment), OldEnabled: ptr(e.Enabled)})
	return e, nil
}

// Update меняет комментарий и/или включённость. nil — поле не трогать. Возвращает запись до и после.
func (m *Mutation) Update(id int64, comment *string, enabled *bool) (old, updated Entry, err error) {
	old, err = scanEntry(m.tx.QueryRowContext(m.ctx, entrySelect+` WHERE e.list_id = ? AND e.id = ?`, m.listID, id))
	if err != nil {
		return Entry{}, Entry{}, err
	}
	c := Change{Op: "update", Value: old.Value, Kind: old.Kind}
	updated = old
	if comment != nil && *comment != old.Comment {
		c.OldComment, c.NewComment = ptr(old.Comment), comment
		updated.Comment = *comment
	}
	if enabled != nil && *enabled != old.Enabled {
		c.OldEnabled, c.NewEnabled = ptr(old.Enabled), enabled
		updated.Enabled = *enabled
	}
	if c.NewComment == nil && c.NewEnabled == nil {
		return old, old, nil
	}
	if _, err := m.tx.ExecContext(m.ctx,
		`UPDATE entries SET comment = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		updated.Comment, updated.Enabled, unix(m.now), id); err != nil {
		return Entry{}, Entry{}, err
	}
	updated.UpdatedAt = m.now
	m.changes = append(m.changes, c)
	return old, updated, nil
}

func ptr[T any](v T) *T { return &v }

// RecentChange — строка ленты «последние изменения».
type RecentChange struct {
	ListSlug  string
	ListTitle string
	Version   int64
	Username  string
	Source    string
	Added     int
	Removed   int
	Updated   int
	Sample    []string // до 3 значений для превью
	CreatedAt time.Time
}

// RecentChanges — последние версии по видимым пользователю спискам.
func (s *Store) RecentChanges(ctx context.Context, userID int64, isAdmin bool, limit int) ([]RecentChange, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.id, l.slug, l.title, v.version, coalesce(u.username, ''), v.source, v.created_at,
		       (SELECT count(*) FROM entry_changes c WHERE c.version_id = v.id AND c.op = 'add'),
		       (SELECT count(*) FROM entry_changes c WHERE c.version_id = v.id AND c.op = 'remove'),
		       (SELECT count(*) FROM entry_changes c WHERE c.version_id = v.id AND c.op = 'update')
		FROM list_versions v
		JOIN lists l ON l.id = v.list_id
		LEFT JOIN users u ON u.id = v.user_id
		WHERE l.archived_at IS NULL
		  AND (:admin OR l.owner_id = :uid OR EXISTS (SELECT 1 FROM list_members m WHERE m.list_id = l.id AND m.user_id = :uid))
		ORDER BY v.created_at DESC, v.id DESC LIMIT :limit`,
		sql.Named("uid", userID), sql.Named("admin", isAdmin), sql.Named("limit", limit))
	if err != nil {
		return nil, err
	}
	var out []RecentChange
	var ids []int64
	for rows.Next() {
		var rc RecentChange
		var id, created int64
		if err := rows.Scan(&id, &rc.ListSlug, &rc.ListTitle, &rc.Version, &rc.Username, &rc.Source, &created,
			&rc.Added, &rc.Removed, &rc.Updated); err != nil {
			rows.Close()
			return nil, err
		}
		rc.CreatedAt = fromUnix(created)
		out = append(out, rc)
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		sr, err := s.db.QueryContext(ctx, `SELECT value FROM entry_changes WHERE version_id = ? ORDER BY id LIMIT 3`, id)
		if err != nil {
			return nil, err
		}
		for sr.Next() {
			var v string
			if err := sr.Scan(&v); err != nil {
				sr.Close()
				return nil, err
			}
			out[i].Sample = append(out[i].Sample, v)
		}
		sr.Close()
	}
	return out, nil
}
