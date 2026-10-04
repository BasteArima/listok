// Package web — HTTP-обработчики интерфейса (docs/api.md, docs/ui.md).
package web

import (
	"context"
	"database/sql"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/config"
	"github.com/BasteArima/listok/internal/lists"
	"github.com/BasteArima/listok/internal/routers"
	"github.com/BasteArima/listok/internal/store"
)

//go:embed templates static
var assets embed.FS

type Deps struct {
	DB      *sql.DB
	Store   *store.Store
	Auth    *auth.Service
	Lists   *lists.Service
	Routers *routers.Service
	Config  config.Config
	Log     *slog.Logger
}

type Server struct {
	Deps
	pages        map[string]*pageTemplate
	partials     *template.Template
	secureCookie bool
	handler      http.Handler
	publicLimit  *rateLimit // публичные пути по токену: /f/, /install/, /agent/v1
}

func New(d Deps) (*Server, error) {
	pages, partials, err := loadPages()
	if err != nil {
		return nil, err
	}
	s := &Server{
		Deps:         d,
		pages:        pages,
		partials:     partials,
		secureCookie: strings.HasPrefix(d.Config.BaseURL, "https://"),
		publicLimit:  newRateLimit(publicRatePerMinute),
	}

	mux := http.NewServeMux()
	staticFS, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(staticFS))))
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /f/{file}", s.limited(s.serveFeed))
	mux.HandleFunc("GET /install/{token}", s.limited(s.installScript))
	mux.HandleFunc("POST /agent/v1/hello", s.limited(s.agentHello))
	mux.HandleFunc("POST /agent/v1/applied", s.limited(s.agentApplied))

	mux.HandleFunc("GET /setup", s.setupForm)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /logout", s.logout)

	authed := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireUser(h)) }
	authed("GET /{$}", s.home)
	authed("POST /preview", s.preview)
	authed("POST /quick", s.quick)
	authed("GET /lists", s.listsPage)
	authed("POST /lists", s.createList)
	authed("GET /lists/{slug}", s.listPage)
	authed("GET /lists/{slug}/rows", s.listRows)
	authed("POST /lists/{slug}/clear", s.clearList)
	authed("POST /lists/{slug}/delete", s.deleteList)
	authed("POST /lists/{slug}/entries", s.addEntries)
	authed("POST /lists/{slug}/entries/bulk", s.bulkEntries)
	authed("PATCH /lists/{slug}/entries/{id}", s.patchEntry)
	authed("DELETE /lists/{slug}/entries/{id}", s.deleteEntry)
	authed("GET /lists/{slug}/entries/{id}/row", s.entryRow(false))
	authed("GET /lists/{slug}/entries/{id}/edit", s.entryRow(true))
	authed("GET /lists/{slug}/history", s.historyPage)
	authed("GET /lists/{slug}/history/more", s.historyMore)
	authed("GET /lists/{slug}/history/{version}", s.historyVersion)
	authed("POST /lists/{slug}/rollback/{version}", s.rollback)
	authed("GET /routers", s.routersPage)
	authed("POST /routers", s.createRouter)
	authed("GET /routers/{id}", s.routerPage)
	authed("POST /routers/{id}", s.updateRouter)
	authed("POST /routers/{id}/delete", s.deleteRouter)
	authed("POST /routers/{id}/install", s.createInstall)
	authed("POST /routers/{id}/agent/revoke", s.revokeAgent)
	authed("POST /routers/{id}/feeds", s.createFeed)
	authed("POST /feeds/{id}/lists", s.feedAction(func(r *http.Request, u store.User, id int64) (int64, error) {
		return s.Routers.SetFeedLists(r.Context(), u, id, listIDsFrom(r))
	}))
	authed("POST /feeds/{id}/toggle", s.feedAction(func(r *http.Request, u store.User, id int64) (int64, error) {
		return s.Routers.SetFeedEnabled(r.Context(), u, id, r.PostFormValue("enabled") == "1")
	}))
	authed("POST /feeds/{id}/regenerate", s.feedAction(func(r *http.Request, u store.User, id int64) (int64, error) {
		return s.Routers.RegenerateToken(r.Context(), u, id)
	}))
	authed("POST /feeds/{id}/delete", s.feedAction(func(r *http.Request, u store.User, id int64) (int64, error) {
		return s.Routers.DeleteFeed(r.Context(), u, id)
	}))

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

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ссылки из шаблонов содержат ?v=<хеш статики>, поэтому кешировать можно сколько угодно.
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}
