package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/BasteArima/listok/internal/store"
)

var newTokenRe = regexp.MustCompile(`id="new-token"|value="([A-Za-z0-9_-]{43})"`)

// apiDo — запрос как из расширения браузера: межсайтовый, с origin расширения и без cookie.
func apiDo(t *testing.T, e *env, method, path, token, payload string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(payload))
	req.Header.Set("Origin", "chrome-extension://abcdef")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, body(t, resp)
}

func createToken(t *testing.T, e *env, name string) string {
	t.Helper()
	r := e.post(t, "/connections", url.Values{"name": {name}})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("создание токена: %d", r.StatusCode)
	}
	page := body(t, r)
	m := newTokenRe.FindAllStringSubmatch(page, -1)
	for _, v := range m {
		if v[1] != "" {
			mustContain(t, page, "Токен «"+name+"» создан", "#"+v[1], "показывается один раз")
			return v[1]
		}
	}
	t.Fatal("на странице нет токена")
	return ""
}

func TestConnectionsPage(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")

	if r := e.post(t, "/connections", url.Values{"name": {"  "}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("пустое название: %d", r.StatusCode)
	}
	token := createToken(t, e, "ПК")

	// Токен показывается один раз, в БД — только хеш.
	page := body(t, e.get(t, "/connections"))
	mustContain(t, page, "ПК", token[:6]+"…", "ещё не использовался", "/connections/1/revoke", "/extension.zip")
	if strings.Contains(page, token) {
		t.Fatal("токен виден при повторном открытии")
	}
	var n int
	e.srv.DB.QueryRowContext(context.Background(), `SELECT count(*) FROM api_tokens WHERE token_hash = ?`, token).Scan(&n)
	if n != 0 {
		t.Fatal("токен лежит в БД открыто")
	}
	mustContain(t, body(t, e.get(t, "/")), `href="/connections"`)

	// Первый запрос виден в таблице, отзыв закрывает доступ.
	if r, _ := apiDo(t, e, "GET", "/api/v1/lists", token, ""); r.StatusCode != http.StatusOK {
		t.Fatalf("запрос с токеном: %d", r.StatusCode)
	}
	if strings.Contains(body(t, e.get(t, "/connections")), "ещё не использовался") {
		t.Fatal("последний запрос не отмечен")
	}
	expectHXRedirect(t, e.do(t, "POST", "/connections/1/revoke", nil), "/connections")
	if r, _ := apiDo(t, e, "GET", "/api/v1/lists", token, ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("отозванный токен: %d", r.StatusCode)
	}
	if r := e.do(t, "POST", "/connections/1/revoke", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("повторный отзыв: %d", r.StatusCode)
	}
}

func TestAPI(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	token := createToken(t, e, "ПК")
	ctx := context.Background()

	// Без токена 401. Сессия из cookie не действует: API авторизуется только заголовком.
	if r, b := apiDo(t, e, "GET", "/api/v1/lists", "", ""); r.StatusCode != http.StatusUnauthorized || !strings.Contains(b, `"error":"unauthorized"`) {
		t.Fatalf("без токена: %d %s", r.StatusCode, b)
	}
	if r := e.get(t, "/api/v1/lists"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("с cookie сессии: %d", r.StatusCode)
	}

	// Preflight проходит без токена.
	r, _ := apiDo(t, e, "OPTIONS", "/api/v1/lists/common/entries", "", "")
	if r.StatusCode != http.StatusNoContent || r.Header.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(r.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight: %d %v", r.StatusCode, r.Header)
	}

	r, b := apiDo(t, e, "GET", "/api/v1/lists", token, "")
	if r.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("нет заголовка CORS")
	}
	mustContain(t, b, `"user":"admin"`, `"slug":"common"`, `"title":"Общий"`, `"can_edit":true`)

	// Проверка ничего не меняет: хост сокращается до домена, «только этот хост» оставляет как есть.
	_, b = apiDo(t, e, "GET", "/api/v1/check?q="+url.QueryEscape("https://www.youtube.com/watch?v=1"), token, "")
	mustContain(t, b, `"value":"youtube.com"`, `"kind":"domain"`)
	_, b = apiDo(t, e, "GET", "/api/v1/check?exact=1&q=www.youtube.com", token, "")
	mustContain(t, b, `"value":"www.youtube.com"`)
	_, b = apiDo(t, e, "GET", "/api/v1/check?q=192.168.1.1", token, "")
	mustContain(t, b, "частный или служебный диапазон")

	// Добавление: межсайтовый запрос проходит, источник — api, адрес можно прислать целой ссылкой.
	add := func(payload string) (int, map[string]any) {
		t.Helper()
		r, b := apiDo(t, e, "POST", "/api/v1/lists/common/entries", token, payload)
		var v map[string]any
		json.Unmarshal([]byte(b), &v)
		return r.StatusCode, v
	}
	code, v := add(`{"input":"https://www.youtube.com/watch?v=1","comment":"видео"}`)
	if code != http.StatusOK || v["message"] != "Добавлено: youtube.com в «Общий»" {
		t.Fatalf("добавление: %d %v", code, v)
	}
	var source, comment string
	e.srv.DB.QueryRowContext(ctx, `SELECT source FROM list_versions ORDER BY id DESC LIMIT 1`).Scan(&source)
	e.srv.DB.QueryRowContext(ctx, `SELECT comment FROM entries WHERE value = 'youtube.com'`).Scan(&comment)
	if source != "api" || comment != "видео" {
		t.Fatalf("source=%q comment=%q", source, comment)
	}

	// Повтор безопасен: расширение повторяет запрос на запасном адресе.
	if _, v = add(`{"input":"youtube.com"}`); v["message"] != "youtube.com уже есть в «Общий»" {
		t.Fatalf("повтор: %v", v)
	}
	if _, v = add(`{"input":"m.youtube.com","exact_host":true}`); v["message"] != "m.youtube.com уже покрыт записью в «Общий»" {
		t.Fatalf("покрыто: %v", v)
	}
	if _, v = add(`{"input":"com"}`); !strings.HasPrefix(v["message"].(string), "com: ") {
		t.Fatalf("ошибка разбора: %v", v)
	}
	if _, b = apiDo(t, e, "GET", "/api/v1/check?q=youtube.com", token, ""); !strings.Contains(b, `"list":"Общий"`) {
		t.Fatalf("покрытие в проверке: %s", b)
	}

	for payload, want := range map[string]int{
		`{"input":" "}`: http.StatusUnprocessableEntity,
		`не json`:       http.StatusBadRequest,
		`{"input":"a.example","comment":"` + strings.Repeat("я", 201) + `"}`: http.StatusUnprocessableEntity,
	} {
		if code, _ := add(payload); code != want {
			t.Errorf("%.30s: %d, ждали %d", payload, code, want)
		}
	}
	if r, _ := apiDo(t, e, "POST", "/api/v1/lists/nope/entries", token, `{"input":"a.example"}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("нет списка: %d", r.StatusCode)
	}

	// Защита от CSRF на остальных маршрутах осталась.
	if r := e.post(t, "/lists", url.Values{"title": {"Чужой"}}, "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("межсайтовый POST в интерфейс: %d", r.StatusCode)
	}
}

// Права токена — права его владельца: просмотр не даёт добавлять, чужой токен не отозвать.
func TestAPIPermissions(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Смотреть"}, "slug": {"view"}}), "/lists/view")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Скрытый"}, "slug": {"hidden"}}), "/lists/hidden")
	adminToken := createToken(t, e, "админ")
	ctx := context.Background()

	id, err := e.srv.Store.CreateUser(ctx, "guest", "x", false, store.User{}.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.DB.ExecContext(ctx, `INSERT INTO list_members (list_id, user_id, role) SELECT id, ?, 'viewer' FROM lists WHERE slug = 'view'`, id); err != nil {
		t.Fatal(err)
	}
	guest, err := e.srv.Store.UserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	token, err := e.srv.Auth.CreateAPIToken(ctx, guest, "гость", "")
	if err != nil {
		t.Fatal(err)
	}

	_, b := apiDo(t, e, "GET", "/api/v1/lists", token, "")
	mustContain(t, b, `"user":"guest"`, `"slug":"view"`, `"can_edit":false`)
	if strings.Contains(b, "hidden") {
		t.Fatalf("виден чужой список: %s", b)
	}
	if r, _ := apiDo(t, e, "POST", "/api/v1/lists/view/entries", token, `{"input":"a.example"}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("просмотр: %d", r.StatusCode)
	}
	if r, _ := apiDo(t, e, "POST", "/api/v1/lists/hidden/entries", token, `{"input":"a.example"}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("скрытый список: %d", r.StatusCode)
	}

	if err := e.srv.Auth.RevokeAPIToken(ctx, guest, 1, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("отзыв чужого токена: %v", err)
	}
	if r, _ := apiDo(t, e, "GET", "/api/v1/lists", adminToken, ""); r.StatusCode != http.StatusOK {
		t.Fatalf("токен админа после чужой попытки отзыва: %d", r.StatusCode)
	}

	// Отключённый пользователь теряет и токены.
	if _, err := e.srv.DB.ExecContext(ctx, `UPDATE users SET disabled_at = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if r, _ := apiDo(t, e, "GET", "/api/v1/lists", token, ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("отключённый пользователь: %d", r.StatusCode)
	}
}

func TestExtensionZip(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	r := e.get(t, "/extension.zip")
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "application/zip" ||
		!strings.Contains(r.Header.Get("Content-Disposition"), "listok-extension-") {
		t.Fatalf("архив: %d %v", r.StatusCode, r.Header)
	}
	data, _ := io.ReadAll(r.Body)
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
}
