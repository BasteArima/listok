package web

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/lists"
	"github.com/BasteArima/listok/internal/store"
)

// upload — htmx-запрос с файлом в поле file.
func (e *env) upload(t *testing.T, path, name string, data []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestExportImport(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"youtube.com 104.29.13.37/16"}, "comment": {"видео"}})
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"music.example.org"}, "exact": {"1"}})
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"off.example"}})
	id := entryIDRe.FindStringSubmatch(body(t, e.get(t, "/lists/common/rows?q=off.example")))[1]
	e.do(t, "PATCH", "/lists/common/entries/"+id, url.Values{"enabled": {"0"}})

	// Экспорт: файл с заголовком, комментариями, без выключенных.
	r := e.get(t, "/lists/common/export")
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Disposition") != `attachment; filename="common.txt"` {
		t.Fatalf("экспорт: %d %q", r.StatusCode, r.Header.Get("Content-Disposition"))
	}
	file := body(t, r)
	mustContain(t, file, "# listok: Общий\n", "записей: 3\n", "music.example.org\n", "youtube.com # видео\n", "104.29.0.0/16 # видео\n")
	if strings.Contains(file, "off.example") {
		t.Fatal("выключенная запись попала в экспорт")
	}

	// Импорт в новый список: поддомен не сокращается, комментарии сохраняются, ошибки и повторы считаются.
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Копия"}, "slug": {"copy"}}), "/lists/copy")
	page := body(t, e.get(t, "/lists/copy"))
	mustContain(t, page, `hx-post="/lists/copy/import"`, `hx-encoding="multipart/form-data"`)
	r = e.upload(t, "/lists/copy/import", "common.txt", []byte(file+"com\nyoutube.com\n"))
	b := body(t, r)
	mustContain(t, b, "Добавлено: 3", "ошибок: 2", "публичный суффикс", "повтор", `<template><tbody id="rows" hx-swap-oob="true">`, `id="list-stats"`)
	if strings.Contains(b, "st-added") {
		t.Fatal("в итоге импорта показываются добавленные строки")
	}
	rows := body(t, e.get(t, "/lists/copy/rows"))
	mustContain(t, rows, "music.example.org", "youtube.com", "104.29.0.0/16", "видео")

	// Одна версия с source=import и именем файла.
	var n int
	var source, msg string
	e.srv.DB.QueryRowContext(context.Background(),
		`SELECT count(*), max(v.source), max(v.message) FROM list_versions v JOIN lists l ON l.id = v.list_id WHERE l.slug = 'copy'`).Scan(&n, &source, &msg)
	if n != 1 || source != "import" || msg != "импорт из файла common.txt" {
		t.Fatalf("версии после импорта: %d %q %q", n, source, msg)
	}

	// Повторный импорт ничего не добавляет и версию не создаёт.
	mustContain(t, body(t, e.upload(t, "/lists/copy/import", "common.txt", []byte(file))), "Добавлено: 0", "уже было: 3")

	// Не текст и без файла — 422 с сообщением.
	if r := e.upload(t, "/lists/copy/import", "x.bin", []byte{0xff, 0xfe, 0}); r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body(t, r), "UTF-8") {
		t.Fatalf("бинарный файл: %d", r.StatusCode)
	}
	if r := e.do(t, "POST", "/lists/copy/import", url.Values{}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("без файла: %d", r.StatusCode)
	}
	big := bytes.Repeat([]byte("a.example\n"), lists.MaxImportBytes/10+1)
	if r := e.upload(t, "/lists/copy/import", "big.txt", big); r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body(t, r), "МБ") {
		t.Fatalf("большой файл: %d", r.StatusCode)
	}
}

func TestImportViewer(t *testing.T) {
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

	f := newClient(e)
	f.login(t, "friend", "friend-password")
	if r := f.get(t, "/lists/mine/export"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("экспорт чужого списка: %d", r.StatusCode)
	}

	var listID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug='mine'`).Scan(&listID)
	e.srv.DB.ExecContext(ctx, `INSERT INTO list_members (list_id, user_id, role) VALUES (?, ?, 'viewer')`, listID, friendID)
	page := body(t, f.get(t, "/lists/mine"))
	if !strings.Contains(page, "/lists/mine/export") || strings.Contains(page, "/lists/mine/import") {
		t.Fatal("viewer должен видеть экспорт без импорта")
	}
	if !strings.Contains(body(t, f.get(t, "/lists/mine/export")), "secret.example") {
		t.Fatal("viewer не может выгрузить список")
	}
	if r := f.upload(t, "/lists/mine/import", "x.txt", []byte("x.example\n")); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer импортирует: %d", r.StatusCode)
	}
}
