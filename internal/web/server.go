// Package web — HTTP-обработчики интерфейса (docs/api.md, docs/ui.md).
package web

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/config"
	"github.com/BasteArima/listok/internal/store"
)

//go:embed templates static
var assets embed.FS

type Deps struct {
	DB     *sql.DB
	Store  *store.Store
	Auth   *auth.Service
	Config config.Config
	Log    *slog.Logger
}

type Server struct {
	Deps
	pages        map[string]*pageTemplate
	secureCookie bool
	handler      http.Handler
}

func New(d Deps) (*Server, error) {
	pages, err := loadPages()
	if err != nil {
		return nil, err
	}
	s := &Server{
		Deps:         d,
		pages:        pages,
		secureCookie: strings.HasPrefix(d.Config.BaseURL, "https://"),
	}

	mux := http.NewServeMux()
	staticFS, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(staticFS))))
	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /setup", s.setupForm)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /logout", s.logout)

	mux.Handle("GET /{$}", s.requireUser(http.HandlerFunc(s.home)))

	// Защита от CSRF: браузерные запросы с изменением состояния только с того же origin.
	cop := http.NewCrossOriginProtection()
	if d.Config.BaseURL != "" {
		if err := cop.AddTrustedOrigin(d.Config.BaseURL); err != nil {
			return nil, err
		}
	}

	s.handler = securityHeaders(cop.Handler(s.setupGate(s.loadUser(mux))))
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// healthz проверяет, что БД отвечает. Используется healthcheck'ом Docker.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	if err := s.DB.PingContext(ctx); err != nil {
		s.Log.Error("healthz: БД недоступна", "err", err)
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "home", page{Title: "Списки"})
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Без хешей в именах файлов: кешируем ненадолго, чтобы обновления доезжали за час.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}
