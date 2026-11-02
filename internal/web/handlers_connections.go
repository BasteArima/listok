package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/BasteArima/listok/extension"
	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/lists"
	"github.com/BasteArima/listok/internal/store"
)

// Вкладка «Подключения»: токены API для расширения браузера и команд на iPad (docs/extension.md).

type newConnection struct {
	Name       string
	Token      string
	Connection string // адрес и токен одной строкой: её вставляют в настройки расширения
}

type connectionsData struct {
	Tokens           []store.APIToken
	New              *newConnection
	Base             string
	Editable         []store.List
	ExtensionVersion string
}

func (s *Server) renderConnections(w http.ResponseWriter, r *http.Request, status int, errText string, form map[string]string, created *newConnection) {
	u := userFrom(r.Context())
	tokens, err := s.Store.APITokens(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	all, err := s.Lists.Visible(r.Context(), *u)
	if err != nil {
		s.serverError(w, err)
		return
	}
	d := connectionsData{Tokens: tokens, New: created, Base: s.Config.BaseURL, ExtensionVersion: extension.Version}
	for _, l := range all {
		if lists.CanEdit(l) {
			d.Editable = append(d.Editable, l)
		}
	}
	s.render(w, r, status, "connections", page{Title: "Подключения", Error: errText, Form: form, Data: d})
}

func (s *Server) connectionsPage(w http.ResponseWriter, r *http.Request) {
	s.renderConnections(w, r, http.StatusOK, "", nil, nil)
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	token, err := s.Auth.CreateAPIToken(r.Context(), *userFrom(r.Context()), name, s.clientIP(r))
	if errors.Is(err, auth.ErrBadTokenName) {
		s.renderConnections(w, r, http.StatusUnprocessableEntity, err.Error(), map[string]string{"name": name}, nil)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // Токен на странице не должен вернуться кнопкой «Назад».
	s.renderConnections(w, r, http.StatusOK, "", nil, &newConnection{Name: name, Token: token, Connection: s.Config.BaseURL + "#" + token})
}

func (s *Server) revokeConnection(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	err = s.Auth.RevokeAPIToken(r.Context(), *userFrom(r.Context()), id, s.clientIP(r))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.redirect(w, r, "/connections")
}

// extensionZip — расширение для установки через «Загрузить распакованное».
func (s *Server) extensionZip(w http.ResponseWriter, r *http.Request) {
	data, err := extension.Zip()
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="listok-extension-%s.zip"`, extension.Version))
	w.Write(data)
}
