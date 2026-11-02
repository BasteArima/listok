package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/entry"
	"github.com/BasteArima/listok/internal/lists"
)

// JSON API /api/v1: расширение браузера, команды на iPad, скрипты. Описание: docs/api.md, docs/extension.md.

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

// apiCORS разрешает запросы с любого origin: API не смотрит cookie, нужен токен в заголовке,
// поэтому чужая страница ничего не сможет сделать от имени вошедшего пользователя (D-040).
func apiCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Access-Control-Allow-Origin", "*")
		hd.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		hd.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		hd.Set("Access-Control-Max-Age", "86400")
		h(w, r)
	}
}

// apiAuth — пользователь по заголовку Authorization: Bearer <токен API>. Сессия из cookie здесь не действует.
func (s *Server) apiAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		u, err := s.Auth.AuthenticateAPI(r.Context(), strings.TrimSpace(token))
		if errors.Is(err, auth.ErrNoSession) {
			apiError(w, http.StatusUnauthorized, "unauthorized", "Токен не подходит или отозван")
			return
		}
		if err != nil {
			s.apiServerError(w, err)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, &u)))
	}
}

func (s *Server) apiServerError(w http.ResponseWriter, err error) {
	s.Log.Error("ошибка обработки запроса API", "err", err)
	apiError(w, http.StatusInternalServerError, "internal", "Ошибка сервера")
}

func (s *Server) apiListError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, lists.ErrNotFound):
		apiError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, lists.ErrForbidden):
		apiError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, lists.ErrLongComment):
		apiError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
	default:
		s.apiServerError(w, err)
	}
}

type apiList struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Entries int    `json:"entries"`
	CanEdit bool   `json:"can_edit"`
}

// apiLists — видимые списки и имя владельца токена (настройки расширения показывают, кто подключён).
func (s *Server) apiLists(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	all, err := s.Lists.Visible(r.Context(), *u)
	if err != nil {
		s.apiServerError(w, err)
		return
	}
	out := make([]apiList, 0, len(all))
	menu := map[string]string{}
	for _, l := range all {
		out = append(out, apiList{Slug: l.Slug, Title: l.Title, Entries: l.EnabledCount, CanEdit: lists.CanEdit(l)})
		if !lists.CanEdit(l) {
			continue
		}
		title := l.Title
		if _, taken := menu[title]; taken {
			title = fmt.Sprintf("%s (%s)", l.Title, l.Slug)
		}
		menu[title] = l.Slug
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u.Username, "lists": out, "menu": menu})
}

type apiCover struct {
	List  string `json:"list"`
	Slug  string `json:"slug"`
	Value string `json:"value"`
}

type apiItem struct {
	Input     string     `json:"input"`
	Value     string     `json:"value,omitempty"`
	Kind      string     `json:"kind,omitempty"`
	Status    string     `json:"status,omitempty"` // только после добавления: added, exists, covered, error
	Error     string     `json:"error,omitempty"`
	Warnings  []string   `json:"warnings,omitempty"`
	CoveredBy []apiCover `json:"covered_by,omitempty"`
}

func apiItems(items []lists.Item) []apiItem {
	out := make([]apiItem, 0, len(items))
	for _, it := range items {
		a := apiItem{Input: it.Input, Status: it.Status}
		if it.Err != nil {
			a.Error = it.Err.Error()
		} else {
			a.Value, a.Kind = it.Entry.Value, string(it.Entry.Kind)
		}
		for _, w := range it.Warnings {
			a.Warnings = append(a.Warnings, warnLabel(w))
		}
		for _, c := range it.CoveredBy {
			a.CoveredBy = append(a.CoveredBy, apiCover{List: c.ListTitle, Slug: c.ListSlug, Value: c.Value})
		}
		out = append(out, a)
	}
	return out
}

// apiCheck разбирает ввод и показывает покрытие, ничего не меняя. Токены независимы: хосты одной страницы
// могут сводиться к одному домену, и это не повтор.
func (s *Server) apiCheck(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	opt := entry.Options{ExactHost: r.URL.Query().Get("exact") == "1"}
	items, err := s.Lists.PreviewEach(r.Context(), *u, entry.Tokens(r.URL.Query().Get("q")), opt)
	if err != nil {
		s.apiServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": apiItems(items)})
}

type apiAddRequest struct {
	Input     string `json:"input"`
	Comment   string `json:"comment"`
	ExactHost bool   `json:"exact_host"`
}

// apiAddEntries добавляет записи в список одной версией (source=api). Повтор безопасен: запись получит status=exists.
func (s *Server) apiAddEntries(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	var req apiAddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_json", "Тело запроса — JSON с полем input")
		return
	}
	l, err := s.Lists.Get(r.Context(), *u, r.PathValue("slug"))
	if err != nil {
		s.apiListError(w, err)
		return
	}
	items, err := s.Lists.Add(r.Context(), *u, l, req.Input, entry.Options{ExactHost: req.ExactHost}, req.Comment, "api")
	if err != nil {
		s.apiListError(w, err)
		return
	}
	if len(items) == 0 {
		apiError(w, http.StatusUnprocessableEntity, "empty", "Нечего добавлять")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": addMessage(items, l.Title), "items": apiItems(items)})
}

// addMessage — итог одной фразой: его показывают окошко расширения и уведомление команды на iPad.
func addMessage(items []lists.Item, title string) string {
	if len(items) > 1 {
		return fmt.Sprintf("«%s». %s", title, summarize(items))
	}
	it := items[0]
	switch it.Status {
	case "added":
		return fmt.Sprintf("Добавлено: %s в «%s»", it.Entry.Value, title)
	case "exists":
		return fmt.Sprintf("%s уже есть в «%s»", it.Entry.Value, title)
	case "covered":
		return fmt.Sprintf("%s уже покрыт записью в «%s»", it.Entry.Value, title)
	}
	if it.Err != nil {
		return fmt.Sprintf("%s: %s", it.Input, it.Err)
	}
	return summarize(items)
}
