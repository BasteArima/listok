package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/store"
)

func (e *env) login(t *testing.T, user, pass string) {
	t.Helper()
	expectRedirect(t, e.post(t, "/login", url.Values{"username": {user}, "password": {pass}}), "/")
}

func (e *env) do(t *testing.T, method, path string, form url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func mustContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("в ответе нет %q", sub)
		}
	}
}

var entryIDRe = regexp.MustCompile(`id="entry-(\d+)"`)

func TestListsFlow(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")

	// Создание списка: ошибки валидации и успех.
	r := e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"Bad Slug"}})
	if r.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, r), "латиница") {
		t.Fatalf("плохой slug: %d", r.StatusCode)
	}
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	r = e.post(t, "/lists", url.Values{"title": {"Ещё"}, "slug": {"common"}})
	if r.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, r), "занят") {
		t.Fatalf("занятый slug: %d", r.StatusCode)
	}

	page := body(t, e.get(t, "/lists/common"))
	listID := regexp.MustCompile(`name="list" value="(\d+)"`).FindStringSubmatch(page)
	if listID == nil {
		t.Fatal("нет id списка в форме")
	}

	// Предпросмотр ничего не пишет.
	r = e.do(t, "POST", "/preview", url.Values{"input": {"https://www.youtube.com/x 198.18.0.4 com"}, "list": {listID[1]}})
	mustContain(t, body(t, r), "youtube.com", "из www.youtube.com", "FakeIP", "публичный суффикс")
	if rows := body(t, e.get(t, "/lists/common/rows")); !strings.Contains(rows, "Записей пока нет") {
		t.Fatal("предпросмотр не должен ничего добавлять")
	}

	// Добавление пачкой: ответ содержит итог, новые строки (OOB в <template>) и счётчик.
	r = e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"youtube.com discord.gg 104.29.13.37/16 com"}, "comment": {"тест"}})
	b := body(t, r)
	mustContain(t, b, "Добавлено: 3", "ошибок: 1", `<template><tbody id="rows" hx-swap-oob="true">`, `id="list-stats" class="stats muted small" hx-swap-oob="true"`, "104.29.0.0/16")

	// Повтор и покрытое.
	r = e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"youtube.com m.youtube.com 104.29.1.1"}, "exact": {"1"}})
	mustContain(t, body(t, r), "Добавлено: 0", "уже было: 1", "уже покрыто: 2")

	// Поиск.
	rows := body(t, e.get(t, "/lists/common/rows?q=disc"))
	if !strings.Contains(rows, "discord.gg") || strings.Contains(rows, "youtube.com") {
		t.Fatal("поиск по подстроке не работает")
	}
	rows = body(t, e.get(t, "/lists/common/rows?kind=cidr"))
	if !strings.Contains(rows, "104.29.0.0/16") || strings.Contains(rows, "discord.gg") {
		t.Fatal("фильтр по типу не работает")
	}

	// Выключение: запись выпадает из индекса, значит повторное добавление того же поддомена не «покрыто».
	m := entryIDRe.FindStringSubmatch(body(t, e.get(t, "/lists/common/rows?q=youtube")))
	if m == nil {
		t.Fatal("нет строки youtube.com")
	}
	id := m[1]
	r = e.do(t, "PATCH", "/lists/common/entries/"+id, url.Values{"enabled": {"0"}})
	mustContain(t, body(t, r), `class="off"`, "выкл")
	r = e.do(t, "POST", "/preview", url.Values{"input": {"m.youtube.com"}, "list": {listID[1]}, "exact": {"1"}})
	if strings.Contains(body(t, r), "покрыто") {
		t.Fatal("выключенная запись не должна покрывать")
	}
	r = e.do(t, "PATCH", "/lists/common/entries/"+id, url.Values{"enabled": {"1"}})
	if strings.Contains(body(t, r), `class="off"`) {
		t.Fatal("запись не включилась")
	}

	// Комментарий: форма правки, слишком длинный (422 с формой), нормальный.
	mustContain(t, body(t, e.get(t, "/lists/common/entries/"+id+"/edit")), `name="comment"`, "Сохранить")
	r = e.do(t, "PATCH", "/lists/common/entries/"+id, url.Values{"comment": {strings.Repeat("я", 201)}})
	if r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body(t, r), "до 200") {
		t.Fatalf("длинный комментарий: %d", r.StatusCode)
	}
	r = e.do(t, "PATCH", "/lists/common/entries/"+id, url.Values{"comment": {"видео"}})
	mustContain(t, body(t, r), "видео")

	// Удаление.
	if r = e.do(t, "DELETE", "/lists/common/entries/"+id, nil); r.StatusCode != 200 {
		t.Fatalf("удаление: %d", r.StatusCode)
	}
	if strings.Contains(body(t, e.get(t, "/lists/common/rows")), "youtube.com") {
		t.Fatal("запись не удалилась")
	}
	if r = e.do(t, "DELETE", "/lists/common/entries/"+id, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("повторное удаление: %d", r.StatusCode)
	}

	// История: каждое действие — версия. 2 добавления (второе пустое не считается), 2 PATCH enabled, 1 комментарий, 1 удаление.
	var v int64
	e.srv.DB.QueryRowContext(context.Background(), `SELECT version FROM lists WHERE slug='common'`).Scan(&v)
	if v != 5 {
		t.Errorf("версия списка %d, ожидали 5 (add, off, on, comment, delete)", v)
	}

	// Главная: быстрое добавление и лента изменений.
	home := body(t, e.get(t, "/"))
	mustContain(t, home, `id="quick-input"`, "Общий", "Последние изменения")
	r = e.do(t, "POST", "/quick", url.Values{"input": {"anilist.co"}, "list": {listID[1]}})
	b = body(t, r)
	mustContain(t, b, "Добавлено: 1", `id="quick-input"`, `hx-swap-oob="true"`, `id="recent" hx-swap-oob="true"`, "anilist.co")
	var last *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == lastListCookie {
			last = c
		}
	}
	if last == nil || last.Value != listID[1] {
		t.Fatal("не запомнился последний список")
	}
}

func TestListsPermissions(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	ctx := context.Background()
	st := store.New(e.srv.DB)
	hash, _ := auth.HashPassword("friend-password")
	friendID, err := st.CreateUser(ctx, "friend", hash, false, timeNow())
	if err != nil {
		t.Fatal(err)
	}
	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Мой"}, "slug": {"mine"}}), "/lists/mine")
	e.do(t, "POST", "/lists/mine/entries", url.Values{"input": {"secret.example"}})

	// Друг: чужой список не видит вовсе.
	f := newClient(e)
	f.login(t, "friend", "friend-password")
	if r := f.get(t, "/lists/mine"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("чужой список должен быть 404, получено %d", r.StatusCode)
	}
	if strings.Contains(body(t, f.get(t, "/lists")), "Мой") {
		t.Fatal("чужой список виден в перечне")
	}
	r := f.do(t, "POST", "/preview", url.Values{"input": {"a.secret.example"}})
	if strings.Contains(body(t, r), "Мой") {
		t.Fatal("покрытие не должно раскрывать чужие списки")
	}

	// Viewer: видит, но не правит.
	var listID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug='mine'`).Scan(&listID)
	e.srv.DB.ExecContext(ctx, `INSERT INTO list_members (list_id, user_id, role) VALUES (?, ?, 'viewer')`, listID, friendID)
	page := body(t, f.get(t, "/lists/mine"))
	if !strings.Contains(page, "secret.example") || strings.Contains(page, `id="add-form"`) || strings.Contains(page, "hx-delete") {
		t.Fatal("viewer должен видеть записи без кнопок правки")
	}
	if r := f.do(t, "POST", "/lists/mine/entries", url.Values{"input": {"x.example"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer добавляет: %d", r.StatusCode)
	}
	id := entryIDRe.FindStringSubmatch(page)[1]
	if r := f.do(t, "DELETE", "/lists/mine/entries/"+id, nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer удаляет: %d", r.StatusCode)
	}
	if r := f.do(t, "POST", "/quick", url.Values{"input": {"x.example"}, "list": {itoa(listID)}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer через быстрое добавление: %d", r.StatusCode)
	}

	if r := f.do(t, "POST", "/lists/mine/rollback/0", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer откатывает: %d", r.StatusCode)
	}
	if strings.Contains(body(t, f.get(t, "/lists/mine/history")), "откатить к этой") {
		t.Fatal("viewer не должен видеть кнопку отката")
	}

	// Без сессии htmx получает 401, а не страницу входа во фрагмент.
	anon := newClient(e)
	if r := anon.do(t, "POST", "/preview", url.Values{"input": {"x.example"}}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("аноним htmx: %d", r.StatusCode)
	}
}

// newClient — тот же сервер, но отдельный браузер со своими cookie.
func newClient(e *env) *env {
	c := *e
	jar, _ := cookiejar.New(nil)
	c.client = &http.Client{Jar: jar, CheckRedirect: e.client.CheckRedirect}
	return &c
}

func timeNow() time.Time { return time.Now() }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
