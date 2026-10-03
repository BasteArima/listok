package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/BasteArima/listok/internal/lists"
	"github.com/BasteArima/listok/internal/store"
)

const versionsPage = 50

type versionsData struct {
	Slug     string
	CanEdit  bool
	Latest   int64 // текущая версия списка: к ней откатываться бессмысленно
	Versions []store.Version
	More     bool
	Before   int64
}

type historyData struct {
	List     store.List
	Versions versionsData
}

func (s *Server) loadVersions(r *http.Request, l store.List, before int64) (versionsData, error) {
	vs, err := s.Lists.Versions(r.Context(), l, before, versionsPage+1)
	if err != nil {
		return versionsData{}, err
	}
	d := versionsData{Slug: l.Slug, CanEdit: lists.CanEdit(l), Latest: l.Version}
	if len(vs) > versionsPage {
		vs, d.More = vs[:versionsPage], true
	}
	d.Versions = vs
	if len(vs) > 0 {
		d.Before = vs[len(vs)-1].Version
	}
	return d, nil
}

func (s *Server) historyPage(w http.ResponseWriter, r *http.Request) {
	l, ok := s.listFromPath(w, r)
	if !ok {
		return
	}
	vd, err := s.loadVersions(r, l, 0)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, http.StatusOK, "history", page{Title: "История · " + l.Title, Data: historyData{List: l, Versions: vd}})
}

func (s *Server) historyMore(w http.ResponseWriter, r *http.Request) {
	l, ok := s.listFromPath(w, r)
	if !ok {
		return
	}
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil || before < 1 {
		http.Error(w, "bad before", http.StatusBadRequest)
		return
	}
	vd, err := s.loadVersions(r, l, before)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragments(w, http.StatusOK, fragment{"versions", vd})
}

func versionFromPath(r *http.Request) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	return v, err == nil && v >= 0
}

func (s *Server) historyVersion(w http.ResponseWriter, r *http.Request) {
	l, ok := s.listFromPath(w, r)
	if !ok {
		return
	}
	v, ok := versionFromPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, changes, err := s.Lists.Version(r.Context(), l, v)
	if errors.Is(err, lists.ErrBadVersion) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragments(w, http.StatusOK, fragment{"changes", changes})
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	l, ok := s.listFromPath(w, r)
	if !ok {
		return
	}
	v, ok := versionFromPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, err := s.Lists.Rollback(r.Context(), *userFrom(r.Context()), l, v)
	switch {
	case errors.Is(err, lists.ErrBadVersion):
		http.NotFound(w, r)
		return
	case err != nil:
		s.listError(w, err)
		return
	}
	target := "/lists/" + l.Slug + "/history"
	if r.Header.Get("HX-Request") == "true" {
		// Полная перезагрузка страницы истории: изменились и лента, и счётчики.
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
