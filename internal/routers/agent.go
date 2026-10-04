package routers

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/feed"
	"github.com/BasteArima/listok/internal/store"
)

// Агент на роутере (docs/router-agent.md): ссылка установки, установка, hello и отчёт о применении.

const InstallTTL = 24 * time.Hour

var (
	ErrNoFeeds      = errors.New("у роутера нет включённых фидов — сначала создайте фид")
	ErrBadServerIP  = errors.New("IP сервера: адрес IPv4 или IPv6, например 192.168.1.10")
	ErrUnauthorized = errors.New("неверный токен агента")
	ErrBadReport    = errors.New("неверный отчёт агента")
)

// ParseServerIP — необязательный адрес сервера listok в сети роутера (агент ходит на него
// через curl --resolve, мимо собственного внешнего IP роутера, D-028). Пустая строка — не задан.
func ParseServerIP(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil || a.Zone() != "" {
		return "", ErrBadServerIP
	}
	return a.String(), nil
}

// CreateInstall выдаёт одноразовую ссылку установки агента на InstallTTL. Прежняя ссылка перестаёт работать.
func (s *Service) CreateInstall(ctx context.Context, u store.User, routerID int64) (token string, expires time.Time, err error) {
	r, err := s.Get(ctx, u, routerID)
	if err != nil {
		return "", time.Time{}, err
	}
	token, expires = feed.NewToken(), s.now().Add(InstallTTL)
	return token, expires, s.st.SetInstallToken(ctx, r.ID, auth.HashToken(token), expires)
}

// InstallFeed — секция и токен фида для конфига агента.
type InstallFeed struct {
	Section string
	Token   string
}

// InstallBundle — всё, что установщик подставляет в конфиг роутера.
type InstallBundle struct {
	RouterName string
	AgentToken string
	Feeds      []InstallFeed
}

// PeekInstall проверяет ссылку установки, не тратя её (для страницы-подсказки в браузере).
func (s *Service) PeekInstall(ctx context.Context, token string) (store.Router, error) {
	if token == "" {
		return store.Router{}, ErrNotFound
	}
	r, err := s.st.RouterByInstallToken(ctx, auth.HashToken(token), s.now())
	if errors.Is(err, store.ErrNotFound) {
		return store.Router{}, ErrNotFound
	}
	return r, err
}

// Install тратит ссылку установки и выпускает токен агента. Без включённых фидов ссылка не тратится.
func (s *Service) Install(ctx context.Context, token string) (InstallBundle, error) {
	r, err := s.PeekInstall(ctx, token)
	if err != nil {
		return InstallBundle{}, err
	}
	feeds, err := s.st.FeedsByRouter(ctx, r.ID)
	if err != nil {
		return InstallBundle{}, err
	}
	b := InstallBundle{RouterName: r.Name}
	for _, f := range feeds {
		if f.Enabled {
			b.Feeds = append(b.Feeds, InstallFeed{Section: f.Section, Token: f.Token})
		}
	}
	if len(b.Feeds) == 0 {
		return InstallBundle{}, ErrNoFeeds
	}
	b.AgentToken = feed.NewToken()
	if _, err := s.st.ConsumeInstallToken(ctx, auth.HashToken(token), auth.HashToken(b.AgentToken), s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return InstallBundle{}, ErrNotFound // гонка: ссылку только что использовали
		}
		return InstallBundle{}, err
	}
	return b, nil
}

// RevokeAgent забывает токен агента (кнопка «Отвязать агент»). Фиды продолжают работать.
func (s *Service) RevokeAgent(ctx context.Context, u store.User, routerID int64) error {
	if _, err := s.Get(ctx, u, routerID); err != nil {
		return err
	}
	return s.st.RevokeAgent(ctx, routerID)
}

// Agent — роутер по токену агента из заголовка Authorization.
func (s *Service) Agent(ctx context.Context, token string) (store.Router, error) {
	if token == "" {
		return store.Router{}, ErrUnauthorized
	}
	r, err := s.st.RouterByAgentToken(ctx, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.Router{}, ErrUnauthorized
	}
	return r, err
}

// HelloFeed — фид в ответе hello.
type HelloFeed struct {
	Section string `json:"section"`
	Token   string `json:"-"`
}

// clip — строка из отчёта агента: без управляющих символов, не длиннее n рун.
func clip(v string, n int) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(v))
	if utf8.RuneCountInString(v) > n {
		v = string([]rune(v)[:n])
	}
	return v
}

// Hello отмечает агента живым, запоминает версии и возвращает включённые фиды роутера.
func (s *Service) Hello(ctx context.Context, r store.Router, ip string, info store.AgentInfo) ([]HelloFeed, error) {
	info = store.AgentInfo{AgentVersion: clip(info.AgentVersion, 40), ForkopVersion: clip(info.ForkopVersion, 60),
		SingboxVersion: clip(info.SingboxVersion, 60)}
	if err := s.st.TouchAgent(ctx, r.ID, s.now(), ip, info); err != nil {
		return nil, err
	}
	feeds, err := s.st.FeedsByRouter(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	var out []HelloFeed
	for _, f := range feeds {
		if f.Enabled {
			out = append(out, HelloFeed{Section: f.Section, Token: f.Token})
		}
	}
	return out, nil
}

// Applied записывает отчёт агента о применении версии фида секции.
func (s *Service) Applied(ctx context.Context, r store.Router, ip, section, etag string, ok bool, errText string) error {
	section, etag = strings.TrimSpace(section), clip(etag, 40)
	if !sectionRe.MatchString(section) || etag == "" {
		return ErrBadReport
	}
	f, err := s.st.FeedBySection(ctx, r.ID, section)
	if errors.Is(err, store.ErrNotFound) {
		return ErrBadReport
	}
	if err != nil {
		return err
	}
	if ok {
		errText = ""
	}
	if err := s.st.SetApplied(ctx, f.ID, etag, s.now(), ok, clip(errText, 300)); err != nil {
		return err
	}
	return s.st.TouchAgent(ctx, r.ID, s.now(), ip, store.AgentInfo{})
}
