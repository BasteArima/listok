// Package web — HTTP-обработчики интерфейса (docs/api.md, docs/ui.md).
package web

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"
)

type Server struct {
	db  *sql.DB
	log *slog.Logger
	mux *http.ServeMux
}

func New(db *sql.DB, log *slog.Logger) *Server {
	s := &Server{db: db, log: log, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.healthz)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// healthz проверяет, что БД отвечает. Используется healthcheck'ом Docker.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	if err := s.db.PingContext(ctx); err != nil {
		s.log.Error("healthz: БД недоступна", "err", err)
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}
