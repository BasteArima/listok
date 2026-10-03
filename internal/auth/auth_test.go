package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BasteArima/listok/internal/db"
	"github.com/BasteArima/listok/internal/store"
)

func TestMain(m *testing.M) {
	UseFastHashingForTests()
	m.Run()
}

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("неожиданный формат: %s", h)
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Fatalf("верный пароль не прошёл: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Fatal("неверный пароль прошёл")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("соль не случайная: хеши совпали")
	}
}

func TestVerifyBoevyeParametry(t *testing.T) {
	// Хеш с боевыми параметрами проверяется, даже когда текущие параметры другие.
	h, err := hashWith("пароль-пароль", argonParams{memory: 64 * 1024, time: 3, threads: 2, keyLen: 32, saltLen: 16})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h, "m=65536,t=3,p=2") {
		t.Fatalf("параметры не записаны в хеш: %s", h)
	}
	if ok, err := VerifyPassword(h, "пароль-пароль"); !ok || err != nil {
		t.Fatalf("не прошёл: %v %v", ok, err)
	}
}

func TestVerifyBadHash(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=18$m=1,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=1,t=1,p=1$!!$aGFzaA"} {
		if _, err := VerifyPassword(h, "x"); !errors.Is(err, ErrBadHash) {
			t.Errorf("%q: ожидалась ErrBadHash, получено %v", h, err)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, 10*time.Minute, time.Minute, 4*time.Minute)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	l.Fail("k", t0)
	l.Fail("k", t0.Add(time.Second))
	if ok, _ := l.Allowed("k", t0.Add(2*time.Second)); !ok {
		t.Fatal("после 2 неудач ещё можно")
	}
	if lock := l.Fail("k", t0.Add(2*time.Second)); lock != time.Minute {
		t.Fatalf("третья неудача: блокировка %v, хотели 1м", lock)
	}
	if ok, wait := l.Allowed("k", t0.Add(32*time.Second)); ok || wait != 30*time.Second {
		t.Fatalf("должно быть заблокировано ещё 30с, получено ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allowed("other", t0); !ok {
		t.Fatal("другой ключ не должен блокироваться")
	}

	// Вторая серия — пауза вдвое длиннее, третья упирается в максимум... и не больше.
	t1 := t0.Add(2 * time.Minute)
	for i := range 3 {
		if lock := l.Fail("k", t1.Add(time.Duration(i)*time.Second)); i == 2 && lock != 2*time.Minute {
			t.Fatalf("вторая блокировка %v, хотели 2м", lock)
		}
	}
	t2 := t1.Add(5 * time.Minute)
	for i := range 3 {
		l.Fail("k", t2.Add(time.Duration(i)*time.Second))
	}
	t3 := t2.Add(5 * time.Minute)
	var last time.Duration
	for i := range 3 {
		last = l.Fail("k", t3.Add(time.Duration(i)*time.Second))
	}
	if last != 4*time.Minute {
		t.Fatalf("блокировка не должна превышать максимум: %v", last)
	}

	// Долго без ошибок — рост паузы сбрасывается.
	t4 := t3.Add(time.Hour)
	var lock time.Duration
	for i := range 3 {
		lock = l.Fail("k", t4.Add(time.Duration(i)*time.Second))
	}
	if lock != time.Minute {
		t.Fatalf("после долгой паузы ожидалась базовая блокировка, получено %v", lock)
	}

	l.Reset("k")
	if ok, _ := l.Allowed("k", t4.Add(3*time.Second)); !ok {
		t.Fatal("после Reset должно быть можно")
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestService(t *testing.T) (*Service, *store.Store, *clock) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := db.Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	st := store.New(conn)
	c := &clock{time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	return NewService(st, slog.New(slog.NewTextHandler(io.Discard, nil)), c.now), st, c
}

func TestSetupFlow(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestService(t)

	token, err := s.Bootstrap(ctx, "", "")
	if err != nil || token == "" {
		t.Fatalf("ожидался setup-токен: %q %v", token, err)
	}
	if !s.NeedsSetup() {
		t.Fatal("без пользователей нужна настройка")
	}
	if _, err := s.Setup(ctx, "wrong", "admin", "long-enough-pass", "1.1.1.1"); !errors.Is(err, ErrSetupToken) {
		t.Fatalf("неверный токен: %v", err)
	}
	if _, err := s.Setup(ctx, token, "a", "long-enough-pass", "1.1.1.1"); !errors.Is(err, ErrBadUsername) {
		t.Fatalf("короткий логин: %v", err)
	}
	if _, err := s.Setup(ctx, token, "admin", "short", "1.1.1.1"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("короткий пароль: %v", err)
	}
	u, err := s.Setup(ctx, " "+token+"\n", "admin", "long-enough-pass", "1.1.1.1")
	if err != nil || !u.IsAdmin || u.Username != "admin" {
		t.Fatalf("setup: %+v %v", u, err)
	}
	if s.NeedsSetup() {
		t.Fatal("после setup настройка не нужна")
	}
	if _, err := s.Setup(ctx, token, "admin2", "long-enough-pass", "1.1.1.1"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("повторный setup: %v", err)
	}

	// После рестарта: пользователи есть, токена нет.
	s2 := NewService(s.store, s.log, s.now)
	if tok, err := s2.Bootstrap(ctx, "", ""); tok != "" || err != nil || s2.NeedsSetup() {
		t.Fatalf("рестарт: tok=%q err=%v needs=%v", tok, err, s2.NeedsSetup())
	}
}

func TestSetupTokenBruteforceLimited(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestService(t)
	if _, err := s.Bootstrap(ctx, "", ""); err != nil {
		t.Fatal(err)
	}
	var rl *RateLimitError
	for i := range 25 {
		_, err := s.Setup(ctx, "guess", "admin", "long-enough-pass", "6.6.6.6")
		if errors.As(err, &rl) {
			if i < 20 {
				t.Fatalf("блокировка слишком рано, на попытке %d", i)
			}
			return
		}
	}
	t.Fatal("перебор setup-токена не ограничен")
}

func TestBootstrapFromEnv(t *testing.T) {
	ctx := context.Background()
	s, st, _ := newTestService(t)
	tok, err := s.Bootstrap(ctx, "boss", "long-enough-pass")
	if tok != "" || err != nil {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	u, err := st.UserByUsername(ctx, "BOSS") // COLLATE NOCASE
	if err != nil || !u.IsAdmin {
		t.Fatalf("админ из env не создан: %+v %v", u, err)
	}
	if _, err := NewService(st, s.log, s.now).Bootstrap(ctx, "boss", "short"); err != nil {
		t.Fatalf("при существующих пользователях env игнорируется, получено %v", err)
	}
}

func TestLoginAndSessions(t *testing.T) {
	ctx := context.Background()
	s, st, c := newTestService(t)
	if _, err := s.Bootstrap(ctx, "admin", "long-enough-pass"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Login(ctx, "admin", "nope", "1.1.1.1", "ua"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("неверный пароль: %v", err)
	}
	if _, _, err := s.Login(ctx, "ghost", "long-enough-pass", "1.1.1.1", "ua"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("нет пользователя: %v", err)
	}

	token, u, err := s.Login(ctx, "Admin", "long-enough-pass", "1.1.1.1", "ua")
	if err != nil || u.Username != "admin" {
		t.Fatalf("вход: %v", err)
	}
	if got, err := s.Authenticate(ctx, token); err != nil || got.ID != u.ID {
		t.Fatalf("Authenticate: %+v %v", got, err)
	}
	if _, err := s.Authenticate(ctx, token+"x"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("чужой токен: %v", err)
	}

	// Скользящий срок: заход через 29 дней продлевает сессию ещё на 30.
	c.t = c.t.Add(29 * 24 * time.Hour)
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("на 29-й день: %v", err)
	}
	c.t = c.t.Add(29 * 24 * time.Hour)
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("после продления: %v", err)
	}
	c.t = c.t.Add(31 * 24 * time.Hour)
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("после 31 дня тишины сессия должна истечь: %v", err)
	}
	if n, err := st.DeleteExpiredSessions(ctx, c.t); err != nil || n != 1 {
		t.Fatalf("чистка: n=%d err=%v", n, err)
	}

	token, _, _ = s.Login(ctx, "admin", "long-enough-pass", "1.1.1.1", "ua")
	if err := s.Logout(ctx, token, u.ID, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("после выхода: %v", err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ctx := context.Background()
	s, _, c := newTestService(t)
	if _, err := s.Bootstrap(ctx, "admin", "long-enough-pass"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		s.Login(ctx, "admin", "nope", "1.1.1.1", "")
	}
	var rl *RateLimitError
	if _, _, err := s.Login(ctx, "admin", "long-enough-pass", "1.1.1.1", ""); !errors.As(err, &rl) {
		t.Fatalf("после 5 неудач даже верный пароль должен ждать: %v", err)
	}
	if _, _, err := s.Login(ctx, "admin", "long-enough-pass", "2.2.2.2", ""); err != nil {
		t.Fatalf("с другого IP вход разрешён: %v", err)
	}
	c.t = c.t.Add(61 * time.Second)
	if _, _, err := s.Login(ctx, "admin", "long-enough-pass", "1.1.1.1", ""); err != nil {
		t.Fatalf("после паузы вход разрешён: %v", err)
	}
}
