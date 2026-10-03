package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/BasteArima/listok/internal/store"
)

const (
	SessionTTL        = 30 * 24 * time.Hour
	sessionTouchEvery = 5 * time.Minute
	MinPasswordLen    = 10
	maxPasswordLen    = 256
)

var (
	ErrInvalidCredentials = errors.New("неверный логин или пароль")
	ErrNoSession          = errors.New("сессии нет или она истекла")
	ErrSetupDone          = errors.New("первичная настройка уже выполнена")
	ErrSetupToken         = errors.New("неверный setup-токен")
	ErrBadUsername        = errors.New("логин: 2–32 символа, латиница, цифры, точка, дефис, подчёркивание")
	ErrWeakPassword       = fmt.Errorf("пароль: от %d до %d символов", MinPasswordLen, maxPasswordLen)
)

// RateLimitError — слишком много неудачных попыток, ждать Wait.
type RateLimitError struct{ Wait time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("слишком много попыток, подождите %s", e.Wait.Round(time.Second))
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,32}$`)

type Service struct {
	store *store.Store
	log   *slog.Logger
	now   func() time.Time

	userLimiter *Limiter // по логину+IP: 5 неудач за 15 мин
	ipLimiter   *Limiter // по IP, все логины вместе: 20 неудач за 15 мин

	initialized atomic.Bool
	setupMu     sync.Mutex
	setupHash   string

	dummyOnce sync.Once
	dummyHash string
}

func NewService(st *store.Store, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:       st,
		log:         log,
		now:         now,
		userLimiter: NewLimiter(5, 15*time.Minute, time.Minute, time.Hour),
		ipLimiter:   NewLimiter(20, 15*time.Minute, time.Minute, time.Hour),
	}
}

// Bootstrap вызывается при старте. Если пользователей нет:
// при заданных adminUser/adminPassword создаёт админа, иначе возвращает одноразовый setup-токен для /setup.
func (s *Service) Bootstrap(ctx context.Context, adminUser, adminPassword string) (setupToken string, err error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		s.initialized.Store(true)
		return "", nil
	}

	if adminUser != "" {
		if _, err := s.createFirstAdmin(ctx, adminUser, adminPassword, "env", ""); err != nil {
			return "", fmt.Errorf("создание админа из LISTOK_ADMIN_*: %w", err)
		}
		return "", nil
	}

	token := NewToken(18)
	s.setupMu.Lock()
	s.setupHash = HashToken(token)
	s.setupMu.Unlock()
	return token, nil
}

// NeedsSetup — пользователей ещё нет, все страницы ведут на /setup.
func (s *Service) NeedsSetup() bool { return !s.initialized.Load() }

// Setup создаёт первого админа по setup-токену из лога.
func (s *Service) Setup(ctx context.Context, token, username, password, ip string) (store.User, error) {
	if !s.NeedsSetup() {
		return store.User{}, ErrSetupDone
	}
	key := "setup|" + ip
	now := s.now()
	if ok, wait := s.ipLimiter.Allowed(key, now); !ok {
		return store.User{}, &RateLimitError{Wait: wait}
	}

	s.setupMu.Lock()
	want := s.setupHash
	s.setupMu.Unlock()
	if want == "" || subtle.ConstantTimeCompare([]byte(HashToken(strings.TrimSpace(token))), []byte(want)) != 1 {
		s.ipLimiter.Fail(key, now)
		s.audit(ctx, 0, "setup.fail", nil, ip)
		return store.User{}, ErrSetupToken
	}

	u, err := s.createFirstAdmin(ctx, username, password, "web", ip)
	if err != nil {
		return store.User{}, err
	}
	s.ipLimiter.Reset(key)
	return u, nil
}

func (s *Service) createFirstAdmin(ctx context.Context, username, password, via, ip string) (store.User, error) {
	if err := ValidateCredentials(username, password); err != nil {
		return store.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	id, err := s.store.CreateFirstAdmin(ctx, username, hash, s.now())
	if errors.Is(err, store.ErrAlreadyInitialized) {
		s.initialized.Store(true)
		return store.User{}, ErrSetupDone
	}
	if err != nil {
		return store.User{}, err
	}
	s.initialized.Store(true)
	s.setupMu.Lock()
	s.setupHash = ""
	s.setupMu.Unlock()

	s.log.Info("создан первый администратор", "username", username, "via", via)
	s.audit(ctx, id, "setup.done", map[string]any{"username": username, "via": via}, ip)
	return s.store.UserByID(ctx, id)
}

func ValidateCredentials(username, password string) error {
	if !usernameRe.MatchString(username) {
		return ErrBadUsername
	}
	if n := utf8.RuneCountInString(password); n < MinPasswordLen || n > maxPasswordLen {
		return ErrWeakPassword
	}
	return nil
}

// Login проверяет пароль и открывает сессию. Возвращает токен для cookie.
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (string, store.User, error) {
	now := s.now()
	userKey := strings.ToLower(username) + "|" + ip
	ipKey := "ip|" + ip
	for _, check := range []struct {
		l   *Limiter
		key string
	}{{s.userLimiter, userKey}, {s.ipLimiter, ipKey}} {
		if ok, wait := check.l.Allowed(check.key, now); !ok {
			return "", store.User{}, &RateLimitError{Wait: wait}
		}
	}

	u, err := s.store.UserByUsername(ctx, username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Тратим столько же времени, сколько на настоящую проверку: не выдаём, есть ли такой логин.
		VerifyPassword(s.dummy(), password)
	case err != nil:
		return "", store.User{}, err
	}

	ok := false
	if err == nil && u.DisabledAt == nil {
		ok, err = VerifyPassword(u.PasswordHash, password)
		if err != nil {
			return "", store.User{}, err
		}
	}
	if !ok {
		s.userLimiter.Fail(userKey, now)
		s.ipLimiter.Fail(ipKey, now)
		s.audit(ctx, u.ID, "login.fail", map[string]any{"username": username}, ip)
		return "", store.User{}, ErrInvalidCredentials
	}

	s.userLimiter.Reset(userKey)
	token := NewToken(32)
	if err := s.store.CreateSession(ctx, HashToken(token), u.ID, now, now.Add(SessionTTL), ip, userAgent); err != nil {
		return "", store.User{}, err
	}
	s.audit(ctx, u.ID, "login", nil, ip)
	return token, u, nil
}

// Authenticate находит пользователя по токену из cookie и продлевает сессию.
func (s *Service) Authenticate(ctx context.Context, token string) (store.User, error) {
	if token == "" {
		return store.User{}, ErrNoSession
	}
	now := s.now()
	hash := HashToken(token)
	sess, u, err := s.store.SessionWithUser(ctx, hash, now)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		return store.User{}, ErrNoSession
	}
	if err != nil {
		return store.User{}, err
	}
	if now.Sub(sess.LastSeenAt) >= sessionTouchEvery {
		if err := s.store.TouchSession(ctx, hash, now, now.Add(SessionTTL)); err != nil {
			s.log.Warn("не удалось продлить сессию", "err", err)
		}
	}
	return u, nil
}

func (s *Service) Logout(ctx context.Context, token string, userID int64, ip string) error {
	if token == "" {
		return nil
	}
	if err := s.store.DeleteSession(ctx, HashToken(token)); err != nil {
		return err
	}
	s.audit(ctx, userID, "logout", nil, ip)
	return nil
}

func (s *Service) dummy() string {
	s.dummyOnce.Do(func() {
		s.dummyHash, _ = HashPassword("listok-dummy-password")
	})
	return s.dummyHash
}

func (s *Service) audit(ctx context.Context, userID int64, action string, details map[string]any, ip string) {
	err := s.store.AddAudit(ctx, store.AuditEntry{At: s.now(), UserID: userID, Action: action, Details: details, IP: ip})
	if err != nil {
		s.log.Warn("не удалось записать audit_log", "action", action, "err", err)
	}
}
