package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrSectionTaken = errors.New("у этого роутера уже есть фид для такой секции")

type Router struct {
	ID        int64
	Name      string
	OwnerID   int64
	OwnerName string
	Notes     string
	CreatedAt time.Time
	// LastFetchAt — последний опрос любого фида роутера.
	LastFetchAt *time.Time
	FeedCount   int
}

const routerSelect = `
	SELECT r.id, r.name, r.owner_id, u.username, r.notes, r.created_at,
	       (SELECT max(f.last_fetch_at) FROM feeds f WHERE f.router_id = r.id),
	       (SELECT count(*) FROM feeds f WHERE f.router_id = r.id)
	FROM routers r JOIN users u ON u.id = r.owner_id`

func scanRouter(row interface{ Scan(...any) error }) (Router, error) {
	var r Router
	var created int64
	var last sql.NullInt64
	err := row.Scan(&r.ID, &r.Name, &r.OwnerID, &r.OwnerName, &r.Notes, &created, &last, &r.FeedCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Router{}, ErrNotFound
	}
	r.CreatedAt = fromUnix(created)
	r.LastFetchAt = fromNullUnix(last)
	return r, err
}

// Routers — роутеры пользователя (админ видит все).
func (s *Store) Routers(ctx context.Context, userID int64, isAdmin bool) ([]Router, error) {
	rows, err := s.db.QueryContext(ctx, routerSelect+` WHERE :admin OR r.owner_id = :uid ORDER BY r.name COLLATE NOCASE`,
		sql.Named("uid", userID), sql.Named("admin", isAdmin))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Router
	for rows.Next() {
		r, err := scanRouter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RouterByID(ctx context.Context, id int64) (Router, error) {
	return scanRouter(s.db.QueryRowContext(ctx, routerSelect+` WHERE r.id = ?`, id))
}

func (s *Store) CreateRouter(ctx context.Context, name, notes string, ownerID int64, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO routers (name, notes, owner_id, created_at) VALUES (?, ?, ?, ?)`, name, notes, ownerID, unix(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateRouter(ctx context.Context, id int64, name, notes string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE routers SET name = ?, notes = ? WHERE id = ?`, name, notes, id)
	return err
}

func (s *Store) DeleteRouter(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM routers WHERE id = ?`, id)
	return err
}

// Feed — фид роутера для одной секции forkop.
type Feed struct {
	ID          int64
	RouterID    int64
	Section     string
	Token       string
	Enabled     bool
	LastFetchAt *time.Time
	LastETag    string
	CreatedAt   time.Time
	ListIDs     []int64
	// Владелец роутера: от него зависит, какие списки фид может включать.
	OwnerID      int64
	OwnerIsAdmin bool
}

const feedSelect = `
	SELECT f.id, f.router_id, f.section, f.token, f.enabled, f.last_fetch_at, coalesce(f.last_etag, ''), f.created_at,
	       r.owner_id, u.is_admin
	FROM feeds f JOIN routers r ON r.id = f.router_id JOIN users u ON u.id = r.owner_id`

func scanFeed(row interface{ Scan(...any) error }) (Feed, error) {
	var f Feed
	var last sql.NullInt64
	var created int64
	err := row.Scan(&f.ID, &f.RouterID, &f.Section, &f.Token, &f.Enabled, &last, &f.LastETag, &created,
		&f.OwnerID, &f.OwnerIsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	f.LastFetchAt = fromNullUnix(last)
	f.CreatedAt = fromUnix(created)
	return f, err
}

func (s *Store) feedListIDs(ctx context.Context, feedID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT list_id FROM feed_lists WHERE feed_id = ? ORDER BY list_id`, feedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) FeedsByRouter(ctx context.Context, routerID int64) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, feedSelect+` WHERE f.router_id = ? ORDER BY f.section`, routerID)
	if err != nil {
		return nil, err
	}
	var out []Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].ListIDs, err = s.feedListIDs(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) FeedByID(ctx context.Context, id int64) (Feed, error) {
	f, err := scanFeed(s.db.QueryRowContext(ctx, feedSelect+` WHERE f.id = ?`, id))
	if err != nil {
		return Feed{}, err
	}
	f.ListIDs, err = s.feedListIDs(ctx, f.ID)
	return f, err
}

func (s *Store) FeedByToken(ctx context.Context, token string) (Feed, error) {
	return scanFeed(s.db.QueryRowContext(ctx, feedSelect+` WHERE f.token = ?`, token))
}

func (s *Store) CreateFeed(ctx context.Context, routerID int64, section, token string, listIDs []int64, now time.Time) (int64, error) {
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO feeds (router_id, section, token, created_at) VALUES (?, ?, ?, ?)`,
			routerID, section, token, unix(now))
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: feeds.router_id, feeds.section") {
				return ErrSectionTaken
			}
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return setFeedLists(ctx, tx, id, listIDs)
	})
	return id, err
}

func (s *Store) SetFeedLists(ctx context.Context, feedID int64, listIDs []int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error { return setFeedLists(ctx, tx, feedID, listIDs) })
}

func setFeedLists(ctx context.Context, tx *sql.Tx, feedID int64, listIDs []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM feed_lists WHERE feed_id = ?`, feedID); err != nil {
		return err
	}
	for _, id := range listIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO feed_lists (feed_id, list_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, feedID, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetFeedToken(ctx context.Context, feedID int64, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE feeds SET token = ?, last_etag = NULL WHERE id = ?`, token, feedID)
	return err
}

func (s *Store) SetFeedEnabled(ctx context.Context, feedID int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE feeds SET enabled = ? WHERE id = ?`, enabled, feedID)
	return err
}

func (s *Store) DeleteFeed(ctx context.Context, feedID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM feeds WHERE id = ?`, feedID)
	return err
}

// feedListsCTE — списки фида, которые владелец роутера видит, плюс рекурсивно вложенные в них.
// Вложенные наследуют доступ от родителя: их подключение одобрил владелец родителя (docs/permissions.md).
// UNION убирает повторы и защищает от циклов.
const feedListsCTE = `
	WITH RECURSIVE fl(id) AS (
		SELECT l.id FROM feed_lists x JOIN lists l ON l.id = x.list_id
		WHERE x.feed_id = :feed AND l.archived_at IS NULL
		  AND (:admin OR l.owner_id = :owner
		       OR EXISTS (SELECT 1 FROM list_members m WHERE m.list_id = l.id AND m.user_id = :owner))
		UNION
		SELECT li.child_id FROM list_includes li JOIN fl ON li.parent_id = fl.id
		JOIN lists c ON c.id = li.child_id WHERE c.archived_at IS NULL
	)`

// FeedSourceKey — «id:версия» всех списков фида. Меняется при любом изменении содержимого
// или состава фида: по нему кешируется собранный фид.
func (s *Store) FeedSourceKey(ctx context.Context, f Feed) (string, error) {
	var key sql.NullString
	err := s.db.QueryRowContext(ctx, feedListsCTE+`
		SELECT group_concat(id || ':' || version, ',') FROM (
			SELECT l.id, l.version FROM lists l WHERE l.id IN (SELECT id FROM fl) ORDER BY l.id)`,
		sql.Named("feed", f.ID), sql.Named("owner", f.OwnerID), sql.Named("admin", f.OwnerIsAdmin)).Scan(&key)
	return key.String, err
}

// FeedEntries — включённые записи всех списков фида (с повторами: их убирает entry.Compact).
func (s *Store) FeedEntries(ctx context.Context, f Feed) ([]IndexEntry, error) {
	rows, err := s.db.QueryContext(ctx, feedListsCTE+`
		SELECT e.list_id, e.value, e.kind FROM entries e WHERE e.enabled = 1 AND e.list_id IN (SELECT id FROM fl)`,
		sql.Named("feed", f.ID), sql.Named("owner", f.OwnerID), sql.Named("admin", f.OwnerIsAdmin))
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

type FeedFetch struct {
	At     time.Time
	IP     string
	Status int
	ETag   string
}

// RecordFetch пишет опрос в журнал и обновляет последний опрос фида.
func (s *Store) RecordFetch(ctx context.Context, feedID int64, at time.Time, ip string, status int, etag string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO feed_fetches (feed_id, at, ip, status, etag) VALUES (?, ?, ?, ?, ?)`,
			feedID, unix(at), ip, status, etag); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE feeds SET last_fetch_at = ?, last_etag = ? WHERE id = ?`, unix(at), etag, feedID)
		return err
	})
}

func (s *Store) FeedFetches(ctx context.Context, feedID int64, limit int) ([]FeedFetch, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, coalesce(ip, ''), status, coalesce(etag, '') FROM feed_fetches WHERE feed_id = ? ORDER BY at DESC, id DESC LIMIT ?`,
		feedID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedFetch
	for rows.Next() {
		var f FeedFetch
		var at int64
		if err := rows.Scan(&at, &f.IP, &f.Status, &f.ETag); err != nil {
			return nil, err
		}
		f.At = fromUnix(at)
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteOldFetches — чистка журнала опросов (docs/feeds.md: хранится 30 дней).
func (s *Store) DeleteOldFetches(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM feed_fetches WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
