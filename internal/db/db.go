// Package db открывает SQLite и применяет миграции (docs/db-schema.md).
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

// Open открывает БД по пути к файлу. PRAGMA передаются через DSN,
// поэтому применяются к каждому соединению пула, а не только к первому.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"foreign_keys(1)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
	} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate") // запись в транзакции сразу берёт блокировку: без SQLITE_BUSY посреди tx

	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("открытие %s: %w", path, err)
	}
	return db, nil
}
