package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	names, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var out []migration
	seen := map[int]string{}
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		num, _, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("миграция %s: имя должно быть вида 0001_описание.sql", base)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("миграции %s и %s с одинаковым номером", prev, base)
		}
		seen[v] = base
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: base, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate применяет ещё не применённые миграции, каждую в своей транзакции.
// Возвращает номера применённых.
func Migrate(ctx context.Context, db *sql.DB) ([]int, error) {
	return migrate(ctx, db, migrationsFS)
}

func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) ([]int, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return nil, err
	}

	all, err := loadMigrations(fsys)
	if err != nil {
		return nil, err
	}

	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var done []int
	for _, m := range all {
		if applied[m.version] {
			continue
		}
		if err := apply(ctx, db, m); err != nil {
			return done, fmt.Errorf("миграция %s: %w", m.name, err)
		}
		done = append(done, m.version)
	}
	return done, nil
}

func apply(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
