// Команда listok — сервис списков доменов и подсетей для роутеров с forkop.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BasteArima/listok/internal/config"
	"github.com/BasteArima/listok/internal/db"
	"github.com/BasteArima/listok/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("остановка с ошибкой", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	dbPath := filepath.Join(cfg.DataDir, "listok.db")
	conn, err := db.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	applied, err := db.Migrate(ctx, conn)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		log.Info("применены миграции", "versions", applied)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           web.New(conn, log),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// WriteTimeout не ставим: long-poll фидов держит ответ до LISTOK_LONGPOLL_MAX.
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listok запущен", "addr", cfg.Addr, "db", dbPath, "base_url", cfg.BaseURL)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("получен сигнал, останавливаюсь")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}
