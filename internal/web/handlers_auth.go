package web

import (
	"errors"
	"net/http"

	"github.com/BasteArima/listok/internal/auth"
)

type setupData struct{ MinPassword int }

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "setup", page{Title: "Первый запуск", Data: setupData{auth.MinPasswordLen}})
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	token, username := r.PostFormValue("token"), r.PostFormValue("username")
	password := r.PostFormValue("password")
	fail := func(status int, msg string) {
		s.render(w, r, status, "setup", page{
			Title: "Первый запуск", Error: msg, Data: setupData{auth.MinPasswordLen},
			Form: map[string]string{"token": token, "username": username},
		})
	}

	if password != r.PostFormValue("password2") {
		fail(http.StatusBadRequest, "Пароли не совпадают")
		return
	}
	ip := s.clientIP(r)
	_, err := s.Auth.Setup(r.Context(), token, username, password, ip)
	var rl *auth.RateLimitError
	switch {
	case errors.Is(err, auth.ErrSetupDone):
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	case errors.As(err, &rl):
		fail(http.StatusTooManyRequests, rl.Error())
		return
	case errors.Is(err, auth.ErrSetupToken), errors.Is(err, auth.ErrBadUsername), errors.Is(err, auth.ErrWeakPassword):
		fail(http.StatusBadRequest, err.Error())
		return
	case err != nil:
		s.Log.Error("setup", "err", err)
		fail(http.StatusInternalServerError, "Внутренняя ошибка, подробности в логе")
		return
	}

	// Админ создан — сразу входим.
	sessToken, _, err := s.Auth.Login(r.Context(), username, password, ip, r.UserAgent())
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w, sessToken, int(auth.SessionTTL.Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if userFrom(r.Context()) != nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", page{Title: "Вход", Form: map[string]string{"next": next}})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	username, password := r.PostFormValue("username"), r.PostFormValue("password")
	next := safeNext(r.PostFormValue("next"))
	fail := func(status int, msg string) {
		s.render(w, r, status, "login", page{
			Title: "Вход", Error: msg,
			Form: map[string]string{"username": username, "next": next},
		})
	}

	token, _, err := s.Auth.Login(r.Context(), username, password, s.clientIP(r), r.UserAgent())
	var rl *auth.RateLimitError
	switch {
	case errors.As(err, &rl):
		fail(http.StatusTooManyRequests, rl.Error())
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		fail(http.StatusUnauthorized, err.Error())
		return
	case err != nil:
		s.Log.Error("login", "err", err)
		fail(http.StatusInternalServerError, "Внутренняя ошибка, подробности в логе")
		return
	}
	s.setSessionCookie(w, token, int(auth.SessionTTL.Seconds()))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		var uid int64
		if u := userFrom(r.Context()); u != nil {
			uid = u.ID
		}
		if err := s.Auth.Logout(r.Context(), c.Value, uid, s.clientIP(r)); err != nil {
			s.Log.Warn("logout", "err", err)
		}
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
