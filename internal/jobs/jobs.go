// Package jobs — фоновые задачи процесса: чистка сессий и журнала опросов, ночной бэкап БД.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BasteArima/listok/internal/store"
)

const fetchRetention = 30 * 24 * time.Hour

type Runner struct {
	Store      *store.Store
	Log        *slog.Logger
	BackupDir  string // пусто — бэкапы выключены
	BackupKeep int
	Now        func() time.Time
}

// Run выполняет задачи сразу при старте и затем раз в час, пока жив ctx.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Tick — один проход всех задач. Ошибки только логируются: следующая попытка через час.
func (r *Runner) Tick(ctx context.Context) {
	now := r.now()
	if n, err := r.Store.DeleteExpiredSessions(ctx, now); err != nil {
		r.Log.Warn("чистка сессий", "err", err)
	} else if n > 0 {
		r.Log.Info("удалены просроченные сессии", "count", n)
	}
	if n, err := r.Store.DeleteOldFetches(ctx, now.Add(-fetchRetention)); err != nil {
		r.Log.Warn("чистка журнала опросов", "err", err)
	} else if n > 0 {
		r.Log.Info("удалены старые записи журнала опросов", "count", n)
	}
	if r.BackupDir != "" {
		if path, err := r.Backup(ctx); err != nil {
			r.Log.Error("бэкап БД", "err", err)
		} else if path != "" {
			r.Log.Info("сделан бэкап БД", "path", path)
		}
	}
}

// Backup делает бэкап за сегодня, если его ещё нет, и удаляет лишние старые.
// Возвращает путь нового бэкапа или "" — сегодня уже делали.
func (r *Runner) Backup(ctx context.Context) (string, error) {
	if err := os.MkdirAll(r.BackupDir, 0o750); err != nil {
		return "", err
	}
	name := "listok-" + r.now().Format("20060102") + ".db"
	path := filepath.Join(r.BackupDir, name)
	if _, err := os.Stat(path); err == nil {
		return "", r.prune()
	}
	// Пишем во временный файл: обрыв посреди VACUUM не оставит «сегодняшний» битый бэкап.
	tmp := path + ".tmp"
	os.Remove(tmp)
	if err := r.Store.Backup(ctx, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, r.prune()
}

func (r *Runner) prune() error {
	if r.BackupKeep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(r.BackupDir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "listok-") && strings.HasSuffix(n, ".db") && !e.IsDir() {
			names = append(names, n)
		}
	}
	slices.Sort(names) // дата в имени: лексикографический порядок = хронологический
	for len(names) > r.BackupKeep {
		if err := os.Remove(filepath.Join(r.BackupDir, names[0])); err != nil {
			return fmt.Errorf("удаление старого бэкапа: %w", err)
		}
		names = names[1:]
	}
	return nil
}
