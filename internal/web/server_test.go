package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/config"
	"github.com/BasteArima/listok/internal/db"
	"github.com/BasteArima/listok/internal/store"
)

func TestMain(m *testing.M) {
	auth.UseFastHashingForTests()
	m.Run()
}

type env struct {
	srv        *Server
	ts         *httptest.Server
	client     *http.Client
	setupToken string
}

func newEnv(t *testing.T, adminUser, adminPass string) *env {
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(conn)
	a := auth.NewService(st, log, nil)
	tok, err := a.Bootstrap(ctx, adminUser, adminPass)
	if err != nil {
		t.Fatal(err)
	}

	e := &env{setupToken: tok}
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.srv.ServeHTTP(w, r) })
	e.ts = httptest.NewServer(handler)
	t.Cleanup(e.ts.Close)

	cfg := config.Config{BaseURL: e.ts.URL, TrustedProxy: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	e.srv, err = New(Deps{DB: conn, Store: st, Auth: a, Config: cfg, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	e.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

func (e *env) get(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := e.client.Get(e.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (e *env) post(t *testing.T, path string, form url.Values, hdr ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func body(t *testing.T, r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func expectRedirect(t *testing.T, r *http.Response, to string) {
	t.Helper()
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != to {
		t.Fatalf("ожидался 303 → %s, получено %d → %q", to, r.StatusCode, r.Header.Get("Location"))
	}
}

func TestHealthzAndStatic(t *testing.T) {
	e := newEnv(t, "", "")
	if r := e.get(t, "/healthz"); r.StatusCode != 200 || body(t, r) != "ok\n" {
		t.Fatalf("healthz: %d", r.StatusCode)
	}
	if r := e.get(t, "/static/app.css"); r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("static: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	r := e.get(t, "/healthz")
	if r.Header.Get("Content-Security-Policy") == "" || r.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("нет заголовков безопасности")
	}
}

func TestSetupThroughWeb(t *testing.T) {
	e := newEnv(t, "", "")

	expectRedirect(t, e.get(t, "/"), "/setup")
	expectRedirect(t, e.get(t, "/login"), "/setup")
	if r := e.get(t, "/setup"); r.StatusCode != 200 || !strings.Contains(body(t, r), "Setup-токен") {
		t.Fatal("форма setup не отдалась")
	}

	bad := e.post(t, "/setup", url.Values{"token": {"wrong"}, "username": {"admin"}, "password": {"long-enough-pass"}, "password2": {"long-enough-pass"}})
	if bad.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, bad), "setup-токен") {
		t.Fatalf("неверный токен: %d", bad.StatusCode)
	}
	mismatch := e.post(t, "/setup", url.Values{"token": {e.setupToken}, "username": {"admin"}, "password": {"long-enough-pass"}, "password2": {"other-pass-123"}})
	if mismatch.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, mismatch), "не совпадают") {
		t.Fatalf("несовпадение паролей: %d", mismatch.StatusCode)
	}

	ok := e.post(t, "/setup", url.Values{"token": {e.setupToken}, "username": {"admin"}, "password": {"long-enough-pass"}, "password2": {"long-enough-pass"}})
	expectRedirect(t, ok, "/")

	home := e.get(t, "/")
	if home.StatusCode != 200 || !strings.Contains(body(t, home), "admin") {
		t.Fatalf("после setup должен быть вход: %d", home.StatusCode)
	}
	expectRedirect(t, e.get(t, "/setup"), "/login")
}

func TestLoginLogout(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")

	expectRedirect(t, e.get(t, "/"), "/login?next=%2F")

	r := e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"wrong"}})
	if r.StatusCode != http.StatusUnauthorized || !strings.Contains(body(t, r), "неверный логин или пароль") {
		t.Fatalf("неверный пароль: %d", r.StatusCode)
	}

	r = e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"long-enough-pass"}, "next": {"//evil.example/x"}})
	expectRedirect(t, r, "/") // открытого редиректа нет
	var sess *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie сессии: %+v", sess)
	}

	if home := e.get(t, "/"); home.StatusCode != 200 {
		t.Fatalf("главная после входа: %d", home.StatusCode)
	}
	expectRedirect(t, e.get(t, "/login?next=/lists"), "/lists")

	expectRedirect(t, e.post(t, "/logout", nil), "/login")
	expectRedirect(t, e.get(t, "/"), "/login?next=%2F")
}

func TestLoginNextPreserved(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	r := e.get(t, "/login?next=/lists/common")
	if !regexp.MustCompile(`name="next" value="/lists/common"`).MatchString(body(t, r)) {
		t.Fatal("next не попал в форму")
	}
	expectRedirect(t, e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"long-enough-pass"}, "next": {"/lists/common"}}), "/lists/common")
}

func TestLoginRateLimitedThroughWeb(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	for range 5 {
		e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"wrong"}})
	}
	r := e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"long-enough-pass"}})
	if r.StatusCode != http.StatusTooManyRequests || !strings.Contains(body(t, r), "подождите") {
		t.Fatalf("ожидался 429, получено %d", r.StatusCode)
	}
	// Другой клиент за тем же NPM (другой X-Forwarded-For) не заблокирован.
	r = e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"long-enough-pass"}}, "X-Forwarded-For", "203.0.113.7")
	expectRedirect(t, r, "/")
}

func TestCrossOriginPostBlocked(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	r := e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"long-enough-pass"}},
		"Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example")
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("межсайтовый POST должен блокироваться, получено %d", r.StatusCode)
	}
}

func TestClientIP(t *testing.T) {
	s := &Server{Deps: Deps{Config: config.Config{TrustedProxy: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}}}}
	cases := []struct {
		remote, xff, want string
	}{
		{"203.0.113.1:5000", "", "203.0.113.1"},
		{"203.0.113.1:5000", "1.2.3.4", "203.0.113.1"},               // не прокси — XFF игнорируется
		{"172.18.0.5:5000", "198.51.100.9", "198.51.100.9"},          // NPM
		{"172.18.0.5:5000", "6.6.6.6, 198.51.100.9", "198.51.100.9"}, // подделанный левый адрес не берётся
		{"172.18.0.5:5000", "198.51.100.9, 172.18.0.2", "198.51.100.9"},
		{"172.18.0.5:5000", "", "172.18.0.5"},
		{"[::ffff:203.0.113.1]:5000", "", "203.0.113.1"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := s.clientIP(r); got != tc.want {
			t.Errorf("%s xff=%q: %s, хотели %s", tc.remote, tc.xff, got, tc.want)
		}
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"": "/", "/lists": "/lists", "//evil.example": "/", "/\\evil.example": "/", "https://evil.example": "/", "lists": "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, хотели %q", in, got, want)
		}
	}
}
