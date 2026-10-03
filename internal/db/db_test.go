package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestMigrateIdempotent(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)

	first, err := Migrate(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || first[0] != 1 {
		t.Fatalf("первый прогон должен применить 0001, применено %v", first)
	}
	second, err := Migrate(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("второй прогон ничего не должен применять, применено %v", second)
	}

	want := []string{
		"users", "sessions", "api_tokens", "lists", "list_members", "list_includes", "entries",
		"proposals", "proposal_items", "list_versions", "entry_changes", "routers", "feeds",
		"feed_lists", "feed_fetches", "observations", "suggestion_ignores", "audit_log", "settings",
	}
	for _, name := range want {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil || n != 1 {
			t.Errorf("таблица %s не создана (err=%v)", name, err)
		}
	}
}

func TestPragmas(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	// Пул может открыть новое соединение: PRAGMA должны действовать на любом.
	d.SetMaxOpenConns(4)
	for range 4 {
		var fk int
		var mode string
		if err := d.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("foreign_keys=%d err=%v", fk, err)
		}
		if err := d.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode=%q err=%v", mode, err)
		}
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if _, err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	_, err := d.ExecContext(ctx, `INSERT INTO lists (slug, title, owner_id, kind, created_at, updated_at) VALUES ('x','x',999,'manual',0,0)`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("ожидалась ошибка внешнего ключа, получено %v", err)
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	fsys := fstest.MapFS{
		"migrations/0001_ok.sql":  {Data: []byte(`CREATE TABLE a (x INTEGER);`)},
		"migrations/0002_bad.sql": {Data: []byte(`CREATE TABLE b (x INTEGER); SELECT * FROM nope;`)},
	}
	done, err := migrate(ctx, d, fsys)
	if err == nil {
		t.Fatal("ожидалась ошибка во второй миграции")
	}
	if len(done) != 1 || done[0] != 1 {
		t.Fatalf("должна примениться только 0001, применено %v", done)
	}
	var n int
	d.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='b'`).Scan(&n)
	if n != 0 {
		t.Fatal("таблица b из упавшей миграции должна откатиться")
	}
}

func TestBadMigrationName(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{"migrations/init.sql": {Data: []byte(``)}})
	if err == nil {
		t.Fatal("ожидалась ошибка формата имени")
	}
}
