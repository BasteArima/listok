package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BasteArima/listok/internal/entry"
	"github.com/BasteArima/listok/internal/lists"
	"github.com/BasteArima/listok/internal/store"
)

const (
	lastListCookie = "listok_last_list"
	rowsLimit      = 500
)

type homeData struct {
	Lists    []store.List
	Editable []store.List
	Recent   []store.RecentChange
	LastList int64
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	all, err := s.Lists.Visible(r.Context(), *u)
	if err != nil {
		s.serverError(w, err)
		return
	}
	recent, err := s.Lists.Recent(r.Context(), *u, 10)
	if err != nil {
		s.serverError(w, err)
		return
	}
	d := homeData{Lists: all, Recent: recent}
	for _, l := range all {
		if lists.CanEdit(l) {
			d.Editable = append(d.Editable, l)
		}
	}
	if c, err := r.Cookie(lastListCookie); err == nil {
		d.LastList, _ = strconv.ParseInt(c.Value, 10, 64)
	}
	s.render(w, r, http.StatusOK, "home", page{Title: "Главная", Data: d})
}

func options(r *http.Request) entry.Options {
	return entry.Options{ExactHost: r.FormValue("exact") == "1"}
}

// preview — живой разбор ввода под полем добавления.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	input := r.PostFormValue("input")
	if strings.TrimSpace(input) == "" {
		s.renderFragments(w, http.StatusOK)
		return
	}
	target, _ := strconv.ParseInt(r.PostFormValue("list"), 10, 64)
	items, err := s.Lists.Preview(r.Context(), *u, input, options(r), target)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragments(w, http.StatusOK, fragment{"preview", items})
}

type addResult struct {
	Summary string
	Items   []lists.Item
}

func summarize(items []lists.Item) string {
	var added, exists, covered, failed int
	for _, it := range items {
		switch it.Status {
		case "added":
			added++
		case "exists":
			exists++
		case "covered":
			covered++
		case "error":
			failed++
		}
	}
	parts := []string{fmt.Sprintf("Добавлено: %d", added)}
	if exists > 0 {
		parts = append(parts, fmt.Sprintf("уже было: %d", exists))
	}
	if covered > 0 {
		parts = append(parts, fmt.Sprintf("уже покрыто: %d", covered))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("ошибок: %d", failed))
	}
	return strings.Join(parts, ", ")
}

// listByForm — список из поля "list" (id) среди видимых пользователю.
func (s *Server) listByForm(r *http.Request) (store.List, error) {
	u := userFrom(r.Context())
	id, _ := strconv.ParseInt(r.PostFormValue("list"), 10, 64)
	all, err := s.Lists.Visible(r.Context(), *u)
	if err != nil {
		return store.List{}, err
	}
	for _, l := range all {
		if l.ID == id {
			return l, nil
		}
	}
	return store.List{}, lists.ErrNotFound
}

// quick — быстрое добавление с главной.
func (s *Server) quick(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	l, err := s.listByForm(r)
	if err != nil {
		s.listError(w, err)
		return
	}
	items, err := s.Lists.Add(r.Context(), *u, l, r.PostFormValue("input"), options(r), r.PostFormValue("comment"), "web")
	if err != nil {
		s.listError(w, err)
		return
	}
	recent, err := s.Lists.Recent(r.Context(), *u, 10)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: lastListCookie, Value: strconv.FormatInt(l.ID, 10), Path: "/",
		MaxAge: int((365 * 24 * time.Hour).Seconds()), HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteLaxMode})

	s.renderFragments(w, http.StatusOK,
		fragment{"add-result", addResult{summarize(items), items}},
		fragment{"quick-input", "oob"},
		fragment{"oob-empty", "preview"},
		fragment{"recent-oob", recent},
	)
}

type listsData struct{ Lists []store.List }

func (s *Server) listsPage(w http.ResponseWriter, r *http.Request) {
	s.renderLists(w, r, http.StatusOK, "", nil)
}

func (s *Server) renderLists(w http.ResponseWriter, r *http.Request, status int, msg string, form map[string]string) {
	all, err := s.Lists.Visible(r.Context(), *userFrom(r.Context()))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, status, "lists", page{Title: "Списки", Error: msg, Form: form, Data: listsData{all}})
}

func (s *Server) createList(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	form := map[string]string{
		"title": r.PostFormValue("title"), "slug": r.PostFormValue("slug"), "description": r.PostFormValue("description"),
	}
	l, err := s.Lists.Create(r.Context(), *u, form["slug"], form["title"], form["description"])
	switch {
	case errors.Is(err, lists.ErrBadSlug), errors.Is(err, lists.ErrBadTitle), errors.Is(err, store.ErrSlugTaken):
		s.renderLists(w, r, http.StatusBadRequest, err.Error(), form)
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/lists/"+l.Slug, http.StatusSeeOther)
}

type rowData struct {
	E       store.Entry
	Slug    string
	CanEdit bool
	Error   string
}

type rowsData struct {
	Rows      []rowData
	Filtered  bool
	Truncated bool
	OOB       bool
}

type listData struct {
	List    store.List
	CanEdit bool
	Query   string
	Kind    string
	Rows    rowsData
}

// listFromPath — список из {slug} с проверкой видимости.
func (s *Server) listFromPath(w http.ResponseWriter, r *http.Request) (store.List, bool) {
	l, err := s.Lists.Get(r.Context(), *userFrom(r.Context()), r.PathValue("slug"))
	if err != nil {
		s.listError(w, err)
		return store.List{}, false
	}
	return l, true
}

func filterFrom(r *http.Request) store.EntryFilter {
	return store.EntryFilter{Query: r.FormValue("q"), Kind: r.FormValue("kind")}
}

func (s *Server) loadRows(ctx context.Context, l store.List, f store.EntryFilter, oob bool) (rowsData, error) {
	f.Limit = rowsLimit + 1
	es, err := s.Lists.Entries(ctx, l, f)
	if err != nil {
		return rowsData{}, err
	}
	d := rowsData{Filtered: f.Query != "" || f.Kind != "", OOB: oob}
	if len(es) > rowsLimit {
		es, d.Truncated = es[:rowsLimit], true
	}
	canEdit := lists.CanEdit(l)
	for _, e := range es {
		d.Rows = append(d.Rows, rowData{E: e, Slug: l.Slug, CanEdit: canEdit})
	}
	return d, nil
}

func (s *Server) listPage(w http.ResponseWriter, r *http.Request) {
	l, ok := s.listFromPath(w, r)
	if !ok {
		return
	}
	rows, err := s.loadRows(r.Context(), l, filterFrom(r), false)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, http.StatusOK, "list", page{Title: l.Title, Data: listData{
		List: l, CanEdit: lists.CanEdit(l), Query: r.FormValue("q"), Kind: r.FormValue("kind"), Rows: rows,
	}})
}

func (s *Server) listRows(w http.ResponseWriter, r *http.Request) {
	l, ok := s.listFromPath(w, r)
	if !ok {
		return
	}
	rows, err := s.loadRows(r.Context(), l, filterFrom(r), false)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragments(w, http.StatusOK, fragment{"rows", rows})
}

func (s *Server) addEntries(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	l, ok := s.listFromPath(w, r)
	if !ok {
		return
	}
	items, err := s.Lists.Add(r.Context(), *u, l, r.PostFormValue("input"), options(r), r.PostFormValue("comment"), "web")
	if err != nil {
		s.listError(w, err)
		return
	}
	// Перечитываем список: изменились счётчики и версия. Таблицу показываем без фильтра.
	l, err = s.Lists.Get(r.Context(), *u, l.Slug)
	if err != nil {
		s.serverError(w, err)
		return
	}
	rows, err := s.loadRows(r.Context(), l, store.EntryFilter{}, true)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragments(w, http.StatusOK,
		fragment{"add-result", addResult{summarize(items), items}},
		fragment{"rows", rows},
		fragment{"list-stats", statsData{l, true}},
		fragment{"oob-empty", "add-preview"},
	)
}

// entryFromPath — {id} записи в списке {slug}.
func (s *Server) entryFromPath(w http.ResponseWriter, r *http.Request) (store.List, int64, bool) {
	l, ok := s.listFromPath(w, r)
	if !ok {
		return store.List{}, 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.List{}, 0, false
	}
	return l, id, true
}

func (s *Server) patchEntry(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	l, id, ok := s.entryFromPath(w, r)
	if !ok {
		return
	}
	var e store.Entry
	var err error
	switch {
	case r.PostFormValue("enabled") != "":
		e, err = s.Lists.SetEnabled(r.Context(), *u, l, id, r.PostFormValue("enabled") == "1")
	case r.PostForm.Has("comment"):
		e, err = s.Lists.SetComment(r.Context(), *u, l, id, r.PostFormValue("comment"))
		if errors.Is(err, lists.ErrLongComment) {
			old, _ := s.Store.EntryByID(r.Context(), l.ID, id)
			old.Comment = r.PostFormValue("comment")
			s.renderFragments(w, http.StatusUnprocessableEntity, fragment{"row-edit", rowData{E: old, Slug: l.Slug, CanEdit: true, Error: err.Error()}})
			return
		}
	default:
		http.Error(w, "nothing to change", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.listError(w, err)
		return
	}
	s.renderFragments(w, http.StatusOK, fragment{"row", rowData{E: e, Slug: l.Slug, CanEdit: true}})
}

func (s *Server) entryRow(edit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l, id, ok := s.entryFromPath(w, r)
		if !ok {
			return
		}
		if edit && !lists.CanEdit(l) {
			s.listError(w, lists.ErrForbidden)
			return
		}
		e, err := s.Store.EntryByID(r.Context(), l.ID, id)
		if err != nil {
			s.listError(w, err)
			return
		}
		name := "row"
		if edit {
			name = "row-edit"
		}
		s.renderFragments(w, http.StatusOK, fragment{name, rowData{E: e, Slug: l.Slug, CanEdit: lists.CanEdit(l)}})
	}
}

func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request) {
	l, id, ok := s.entryFromPath(w, r)
	if !ok {
		return
	}
	if err := s.Lists.Delete(r.Context(), *userFrom(r.Context()), l, id); err != nil {
		s.listError(w, err)
		return
	}
	// Пустой ответ 200: htmx заменит строку ничем.
	w.WriteHeader(http.StatusOK)
}

func (s *Server) listError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, lists.ErrNotFound), errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, lists.ErrForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, lists.ErrLongComment):
		s.renderFragments(w, http.StatusUnprocessableEntity, fragment{"form-error", err.Error()})
	default:
		s.serverError(w, err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.Log.Error("ошибка обработки запроса", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
