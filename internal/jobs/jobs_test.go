package jobs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/BasteArima/listok/internal/db"
	"github.com/BasteArima/listok/internal/store"
)

func TestBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	conn, err := db.Open(ctx, filepath.Join(dir, "listok.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := db.Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	st := store.New(conn)
	if _, err := st.CreateUser(ctx, "admin", "x", true, time.Now()); err != nil {
		t.Fatal(err)
	}

	day := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	r := &Runner{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BackupDir: filepath.Join(dir, "backups"), BackupKeep: 3,
		Now: func() time.Time { return day }}

	path, err := r.Backup(ctx)
	if err != nil || filepath.Base(path) != "listok-20261001.db" {
		t.Fatalf("бэкап: %q %v", path, err)
	}
	// Бэкап — рабочая БД с данными.
	b, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	b.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	b.Close()
	if n != 1 {
		t.Fatalf("в бэкапе %d пользователей", n)
	}

	// Повтор в тот же день — ничего не делает.
	if path, err := r.Backup(ctx); path != "" || err != nil {
		t.Fatalf("повтор в тот же день: %q %v", path, err)
	}

	// Пять дней подряд — остаются три последних, временных файлов нет.
	for i := 1; i <= 5; i++ {
		day = day.Add(24 * time.Hour)
		if _, err := r.Backup(ctx); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(r.BackupDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"listok-20261004.db", "listok-20261005.db", "listok-20261006.db"}
	if !slices.Equal(names, want) {
		t.Fatalf("бэкапы %v, хотели %v", names, want)
	}
}
