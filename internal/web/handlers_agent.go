package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BasteArima/listok/agent"
	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/routers"
	"github.com/BasteArima/listok/internal/store"
)

// Агент на роутере: установщик /install/<token>, API /agent/v1. Описание: docs/router-agent.md, docs/api.md.

// installView — команда установки, показывается один раз сразу после выдачи ссылки.
type installView struct {
	Command  string
	ServerIP string
	Expires  time.Time
}

// installCommand — что выполнить на роутере. С serverIP curl идёт на адрес сервера в LAN (D-028):
// роутер, обращаясь к собственному внешнему IP, попал бы в свой LuCI.
func (s *Server) installCommand(token, serverIP string) string {
	u := s.Config.BaseURL + "/install/" + token
	resolve := ""
	if serverIP != "" {
		u += "?ip=" + url.QueryEscape(serverIP)
		if pu, err := url.Parse(s.Config.BaseURL); err == nil {
			port := pu.Port()
			if port == "" {
				port = map[string]string{"https": "443", "http": "80"}[pu.Scheme]
			}
			ip := serverIP
			if strings.Contains(ip, ":") {
				ip = "[" + ip + "]"
			}
			resolve = " --resolve " + pu.Hostname() + ":" + port + ":" + ip
		}
	}
	return "curl -fsSL" + resolve + " '" + u + "' | sh"
}

// createInstall — POST /routers/{id}/install: выдать одноразовую ссылку установки агента.
func (s *Server) createInstall(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routerFromPath(w, r)
	if !ok {
		return
	}
	ip, err := routers.ParseServerIP(r.PostFormValue("server_ip"))
	if err != nil {
		s.renderRouter(w, r, rt, http.StatusBadRequest, err.Error(), map[string]string{"server_ip": r.PostFormValue("server_ip")}, nil)
		return
	}
	token, expires, err := s.Routers.CreateInstall(r.Context(), *userFrom(r.Context()), rt.ID)
	if err != nil {
		s.routerError(w, r, err)
		return
	}
	s.Log.Info("выдана ссылка установки агента", "router", rt.ID, "token", auth.TokenPrefix(token))
	rt, err = s.Routers.Get(r.Context(), *userFrom(r.Context()), rt.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderRouter(w, r, rt, http.StatusOK, "", nil, &installView{Command: s.installCommand(token, ip), ServerIP: ip, Expires: expires})
}

// revokeAgent — POST /routers/{id}/agent/revoke: забыть токен агента.
func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routerFromPath(w, r)
	if !ok {
		return
	}
	if err := s.Routers.RevokeAgent(r.Context(), *userFrom(r.Context()), rt.ID); err != nil {
		s.routerError(w, r, err)
		return
	}
	s.redirect(w, r, "/routers/"+r.PathValue("id"))
}

// fromShell — запрос пришёл от curl или wget (то есть, скорее всего, его выполнят в sh на роутере).
func fromShell(r *http.Request) bool {
	ua := r.UserAgent()
	return strings.HasPrefix(ua, "curl/") || strings.HasPrefix(ua, "Wget")
}

func shellError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte("echo 'listok: ОШИБКА: " + strings.ReplaceAll(msg, "'", "") + "' >&2\nexit 1\n"))
}

// installScript — GET /install/{token}: sh-установщик агента. Ссылка тратится только запросом
// от curl/wget: браузер или предпросмотр ссылки в мессенджере получают подсказку и ничего не ломают.
func (s *Server) installScript(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ip, err := routers.ParseServerIP(r.URL.Query().Get("ip"))
	if err != nil {
		shellError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !fromShell(r) {
		rt, err := s.Routers.PeekInstall(r.Context(), token)
		status := http.StatusOK
		if err != nil {
			status = http.StatusNotFound
		}
		s.render(w, r, status, "install", page{Title: "Установка агента", Data: map[string]any{
			"Valid": err == nil, "Router": rt.Name, "Command": s.installCommand(token, ip),
		}})
		return
	}
	b, err := s.Routers.Install(r.Context(), token)
	switch {
	case errors.Is(err, routers.ErrNotFound):
		shellError(w, http.StatusNotFound, "ссылка установки недействительна, просрочена или уже использована — выдайте новую на странице роутера")
		return
	case errors.Is(err, routers.ErrNoFeeds):
		shellError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	p := agent.InstallParams{Server: s.Config.BaseURL, ServerIP: ip, AgentToken: b.AgentToken}
	for _, f := range b.Feeds {
		p.Feeds = append(p.Feeds, agent.Feed{Section: f.Section, Token: f.Token})
	}
	script, err := agent.Install(p)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Log.Info("агент установлен по ссылке", "router", b.RouterName, "token", auth.TokenPrefix(token), "ip", s.clientIP(r))
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(script)
}

// agentAuth — роутер по заголовку Authorization: Bearer <токен агента>.
func (s *Server) agentAuth(w http.ResponseWriter, r *http.Request) (store.Router, bool) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	rt, err := s.Routers.Agent(r.Context(), strings.TrimSpace(token))
	if errors.Is(err, routers.ErrUnauthorized) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return store.Router{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return store.Router{}, false
	}
	return rt, true
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return false
	}
	return true
}

type helloRequest struct {
	AgentVersion   string   `json:"agent_version"`
	ForkopVersion  string   `json:"forkop_version"`
	SingboxVersion string   `json:"singbox_version"`
	Sections       []string `json:"sections"`
}

type helloFeed struct {
	Section string `json:"section"`
	URL     string `json:"url"`
}

type helloResponse struct {
	Feeds           []helloFeed `json:"feeds"`
	AgentVersion    string      `json:"agent_version"` // актуальная версия агента на сервере
	ReportEnabled   bool        `json:"report_enabled"`
	ReportIntervalS int         `json:"report_interval_s"`
}

// agentHello — POST /agent/v1/hello.
func (s *Server) agentHello(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.agentAuth(w, r)
	if !ok {
		return
	}
	var req helloRequest
	if !readJSON(w, r, &req) {
		return
	}
	feeds, err := s.Routers.Hello(r.Context(), rt, s.clientIP(r), store.AgentInfo{
		AgentVersion: req.AgentVersion, ForkopVersion: req.ForkopVersion, SingboxVersion: req.SingboxVersion,
	})
	if err != nil {
		s.serverError(w, err)
		return
	}
	resp := helloResponse{Feeds: []helloFeed{}, AgentVersion: agent.Version, ReportIntervalS: 300}
	for _, f := range feeds {
		resp.Feeds = append(resp.Feeds, helloFeed{Section: f.Section, URL: s.feedURL(f.Token)})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type appliedRequest struct {
	Section string `json:"section"`
	ETag    string `json:"etag"`
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
}

// agentApplied — POST /agent/v1/applied: результат forkop list_update на роутере.
func (s *Server) agentApplied(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.agentAuth(w, r)
	if !ok {
		return
	}
	var req appliedRequest
	if !readJSON(w, r, &req) {
		return
	}
	err := s.Routers.Applied(r.Context(), rt, s.clientIP(r), req.Section, req.ETag, req.OK, req.Error)
	if errors.Is(err, routers.ErrBadReport) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !req.OK {
		s.Log.Warn("агент не смог применить фид", "router", rt.Name, "section", req.Section, "error", req.Error)
	}
	w.WriteHeader(http.StatusNoContent)
}
