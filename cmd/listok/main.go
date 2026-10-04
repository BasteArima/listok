// Команда listok — сервис списков доменов и подсетей для роутеров с forkop.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/config"
	"github.com/BasteArima/listok/internal/db"
	"github.com/BasteArima/listok/internal/feed"
	"github.com/BasteArima/listok/internal/jobs"
	"github.com/BasteArima/listok/internal/lists"
	"github.com/BasteArima/listok/internal/routers"
	"github.com/BasteArima/listok/internal/store"
	"github.com/BasteArima/listok/internal/web"
)

// version подставляется при сборке: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	// В образе distroless нет curl: healthcheck Docker вызывает сам бинарник.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
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

	st := store.New(conn)
	authSvc := auth.NewService(st, log, nil)
	setupToken, err := authSvc.Bootstrap(ctx, cfg.AdminUser, cfg.AdminPassword)
	if err != nil {
		return err
	}
	if setupToken != "" {
		// Единственное место, где токен печатается целиком: в этом его смысл. Живёт до рестарта.
		log.Warn("пользователей нет: откройте /setup и введите setup-токен", "setup_token", setupToken)
	}
	jobsRunner := &jobs.Runner{Store: st, Log: log, BackupDir: filepath.Join(cfg.DataDir, "backups"), BackupKeep: cfg.BackupKeep}
	if cfg.BackupKeep == 0 {
		jobsRunner.BackupDir = "" // LISTOK_BACKUP_KEEP=0 — бэкапы выключены
	}
	go jobsRunner.Run(ctx)

	listsSvc := lists.New(st, log, nil)
	n, err := listsSvc.LoadIndex(ctx)
	if err != nil {
		return err
	}
	log.Info("индекс покрытия загружен", "entries", n)

	builder := feed.NewBuilder(st)
	listsSvc.OnDelete(func(int64) { builder.Forget() })
	routerSvc := routers.New(st, builder, nil)

	handler, err := web.New(web.Deps{DB: conn, Store: st, Auth: authSvc, Lists: listsSvc, Routers: routerSvc, Config: cfg, Log: log})
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// WriteTimeout не ставим: long-poll фидов держит ответ до LISTOK_LONGPOLL_MAX.
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listok запущен", "version", version, "addr", cfg.Addr, "db", dbPath, "base_url", cfg.BaseURL)
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

// healthcheck — GET /healthz на локальный порт из LISTOK_ADDR. 0 — здоров.
func healthcheck() int {
	addr := os.Getenv("LISTOK_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
