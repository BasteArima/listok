package web

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/BasteArima/listok/internal/store"
)

const sessionCookie = "listok_session"

type ctxKey int

const userKey ctxKey = 1

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey).(*store.User)
	return u
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("X-Frame-Options", "DENY")
		// Скрипты только свои файлы, без inline. При подключении Alpine.js учесть его CSP-сборку.
		hd.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

// setupGate: пока пользователей нет, всё ведёт на /setup; после — /setup недоступен.
func (s *Server) setupGate(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || strings.HasPrefix(p, "/static/") {
			h.ServeHTTP(w, r)
			return
		}
		needs := s.Auth.NeedsSetup()
		switch {
		case needs && p != "/setup":
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
		case !needs && p == "/setup":
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		default:
			h.ServeHTTP(w, r)
		}
	})
}

// loadUser кладёт пользователя из cookie сессии в контекст, если сессия живая.
func (s *Server) loadUser(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err == nil && c.Value != "" {
			u, err := s.Auth.Authenticate(r.Context(), c.Value)
			if err == nil {
				r = r.WithContext(context.WithValue(r.Context(), userKey, &u))
			} else {
				s.clearSessionCookie(w)
			}
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) requireUser(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r.Context()) == nil {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secureCookie,
		// Lax, а не Strict: ссылки на listok из Telegram и «Поделиться» должны открываться залогиненными.
		// От CSRF защищает CrossOriginProtection.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	s.setSessionCookie(w, "", -1)
}

// clientIP — адрес клиента. X-Forwarded-For учитывается, только если запрос пришёл от доверенного прокси (NPM);
// берётся самый правый адрес, не принадлежащий доверенным прокси.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	remote = remote.Unmap()
	if !s.trusted(remote) {
		return remote.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !s.trusted(a) {
			return a.String()
		}
	}
	return remote.String()
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.Config.TrustedProxy {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// safeNext пропускает только локальные пути: защита от открытого редиректа после входа.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}
