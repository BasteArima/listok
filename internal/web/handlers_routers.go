package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/routers"
	"github.com/BasteArima/listok/internal/store"
)

// serveFeed — GET /f/{token}.lst, публичный: доступ по секретному токену.
func (s *Server) serveFeed(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutSuffix(r.PathValue("file"), ".lst")
	if !ok {
		http.NotFound(w, r)
		return
	}
	c, notModified, err := s.Routers.Serve(r.Context(), token, s.clientIP(r), r.Header.Get("If-None-Match"))
	if errors.Is(err, routers.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.Log.Error("отдача фида", "token", auth.TokenPrefix(token), "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("ETag", c.ETag)
	h.Set("Cache-Control", "no-cache")
	if notModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(c.Body)))
	w.Write(c.Body)
}

func (s *Server) feedURL(token string) string {
	return s.Config.BaseURL + "/f/" + token + ".lst"
}

type routersData struct {
	Routers []store.Router
	IsAdmin bool
}

func (s *Server) routersPage(w http.ResponseWriter, r *http.Request) {
	s.renderRouters(w, r, http.StatusOK, "", nil)
}

func (s *Server) renderRouters(w http.ResponseWriter, r *http.Request, status int, msg string, form map[string]string) {
	u := userFrom(r.Context())
	rs, err := s.Routers.List(r.Context(), *u)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, status, "routers", page{Title: "Роутеры", Error: msg, Form: form, Data: routersData{rs, u.IsAdmin}})
}

func (s *Server) createRouter(w http.ResponseWriter, r *http.Request) {
	form := map[string]string{"name": r.PostFormValue("name"), "notes": r.PostFormValue("notes")}
	id, err := s.Routers.Create(r.Context(), *userFrom(r.Context()), form["name"], form["notes"])
	if errors.Is(err, routers.ErrBadName) || errors.Is(err, routers.ErrLongNotes) {
		s.renderRouters(w, r, http.StatusBadRequest, err.Error(), form)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.redirect(w, r, "/routers/"+strconv.FormatInt(id, 10))
}

type feedView struct {
	routers.FeedState
	URL     string
	ListSet map[int64]bool
}

type routerData struct {
	Router store.Router
	Feeds  []feedView
	Lists  []store.List // видимые владельцу роутера: из них собираются фиды
}

func (s *Server) routerFromPath(w http.ResponseWriter, r *http.Request) (store.Router, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.Router{}, false
	}
	rt, err := s.Routers.Get(r.Context(), *userFrom(r.Context()), id)
	if err != nil {
		s.routerError(w, r, err)
		return store.Router{}, false
	}
	return rt, true
}

func (s *Server) routerPage(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routerFromPath(w, r)
	if !ok {
		return
	}
	s.renderRouter(w, r, rt, http.StatusOK, "", nil)
}

func (s *Server) renderRouter(w http.ResponseWriter, r *http.Request, rt store.Router, status int, msg string, form map[string]string) {
	ctx := r.Context()
	feeds, err := s.Routers.Feeds(ctx, rt, 20)
	if err != nil {
		s.serverError(w, err)
		return
	}
	owner, err := s.Store.UserByID(ctx, rt.OwnerID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	ls, err := s.Lists.Visible(ctx, owner)
	if err != nil {
		s.serverError(w, err)
		return
	}
	d := routerData{Router: rt, Lists: ls}
	for _, f := range feeds {
		set := map[int64]bool{}
		for _, id := range f.ListIDs {
			set[id] = true
		}
		d.Feeds = append(d.Feeds, feedView{FeedState: f, URL: s.feedURL(f.Token), ListSet: set})
	}
	s.render(w, r, status, "router", page{Title: rt.Name, Error: msg, Form: form, Data: d})
}

func (s *Server) updateRouter(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routerFromPath(w, r)
	if !ok {
		return
	}
	err := s.Routers.Update(r.Context(), *userFrom(r.Context()), rt.ID, r.PostFormValue("name"), r.PostFormValue("notes"))
	if errors.Is(err, routers.ErrBadName) || errors.Is(err, routers.ErrLongNotes) {
		s.renderRouter(w, r, rt, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if err != nil {
		s.routerError(w, r, err)
		return
	}
	s.redirect(w, r, "/routers/"+strconv.FormatInt(rt.ID, 10))
}

func (s *Server) deleteRouter(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routerFromPath(w, r)
	if !ok {
		return
	}
	if err := s.Routers.Delete(r.Context(), *userFrom(r.Context()), rt.ID); err != nil {
		s.routerError(w, r, err)
		return
	}
	s.redirect(w, r, "/routers")
}

func listIDsFrom(r *http.Request) []int64 {
	r.ParseForm()
	var ids []int64
	for _, v := range r.PostForm["list"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Server) createFeed(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routerFromPath(w, r)
	if !ok {
		return
	}
	section := r.PostFormValue("section")
	_, err := s.Routers.CreateFeed(r.Context(), *userFrom(r.Context()), rt.ID, section, listIDsFrom(r))
	if errors.Is(err, routers.ErrBadSection) || errors.Is(err, routers.ErrBadList) || errors.Is(err, store.ErrSectionTaken) {
		s.renderRouter(w, r, rt, http.StatusBadRequest, err.Error(), map[string]string{"section": section})
		return
	}
	if err != nil {
		s.routerError(w, r, err)
		return
	}
	s.redirect(w, r, "/routers/"+strconv.FormatInt(rt.ID, 10))
}

// feedAction — общая обёртка для действий над фидом: /feeds/{id}/...
func (s *Server) feedAction(fn func(r *http.Request, u store.User, feedID int64) (int64, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		routerID, err := fn(r, *userFrom(r.Context()), id)
		if errors.Is(err, routers.ErrBadList) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			s.routerError(w, r, err)
			return
		}
		s.redirect(w, r, "/routers/"+strconv.FormatInt(routerID, 10))
	}
}

func (s *Server) routerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, routers.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	s.serverError(w, err)
}

// redirect — 303 для обычной формы; для htmx (формы с hx-confirm) — HX-Redirect, чтобы страница перезагрузилась целиком.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
