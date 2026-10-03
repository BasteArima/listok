package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/store"
)

var (
	feedURLRe   = regexp.MustCompile(`/f/([0-9A-Za-z]{43})\.lst`)
	feedIDRe    = regexp.MustCompile(`id="feed-(\d+)"`)
	routerLocRe = regexp.MustCompile(`^/routers/(\d+)$`)
)

// fetchFeed — запрос роутера: без cookie, как curl на OpenWrt.
func fetchFeed(t *testing.T, e *env, token, etag string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/f/"+token+".lst", nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func setupRouter(t *testing.T, e *env, slugs ...string) (routerPath, token, feedID string) {
	t.Helper()
	r := e.post(t, "/routers", url.Values{"name": {"дом"}, "notes": {"NanoPi"}})
	if r.StatusCode != http.StatusSeeOther || !routerLocRe.MatchString(r.Header.Get("Location")) {
		t.Fatalf("создание роутера: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	routerPath = r.Header.Get("Location")
	page := body(t, e.get(t, "/lists"))
	form := url.Values{"section": {"main"}}
	for _, slug := range slugs {
		id := regexp.MustCompile(`href="/lists/` + slug + `"`).FindString(page)
		if id == "" {
			t.Fatalf("нет списка %s", slug)
		}
		var listID int64
		e.srv.DB.QueryRowContext(context.Background(), `SELECT id FROM lists WHERE slug = ?`, slug).Scan(&listID)
		form.Add("list", itoa(listID))
	}
	expectRedirect(t, e.post(t, routerPath+"/feeds", form), routerPath)
	rp := body(t, e.get(t, routerPath))
	m := feedURLRe.FindStringSubmatch(rp)
	f := feedIDRe.FindStringSubmatch(rp)
	if m == nil || f == nil {
		t.Fatalf("на странице роутера нет ссылки фида")
	}
	return routerPath, m[1], f[1]
}

func TestFeedServing(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Игры"}, "slug": {"games"}}), "/lists/games")
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"youtube.com m.youtube.com 104.29.0.0/16 104.29.1.0/24"}, "exact": {"1"}})
	e.do(t, "POST", "/lists/games/entries", url.Values{"input": {"youtube.com steampowered.com 2a00:1450::/32"}})

	routerPath, token, feedID := setupRouter(t, e, "common", "games")

	if strings.Contains(body(t, e.get(t, routerPath)), "забрал актуальную") {
		t.Fatal("до первого опроса статус должен быть «ещё не забирал»")
	}

	// Содержимое: сторожевая запись, затем объединение без повторов и покрытого, домены → IPv4 → IPv6.
	r, lst := fetchFeed(t, e, token, "")
	want := "listok-sentinel.invalid\nsteampowered.com\nyoutube.com\n104.29.0.0/16\n2a00:1450::/32\n"
	if r.StatusCode != 200 || lst != want {
		t.Fatalf("фид %d:\n%s\nхотели:\n%s", r.StatusCode, lst, want)
	}
	if ct := r.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	etag := r.Header.Get("ETag")

	// 304 по ETag, статус «актуальная».
	if r, _ := fetchFeed(t, e, token, etag); r.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", r.StatusCode)
	}
	page := body(t, e.get(t, routerPath))
	mustContain(t, page, "забрал актуальную версию", "без изменений", "скачал", "<b>4</b> записей")

	// Правка списка → новый ETag, роутер «отстаёт», пока не заберёт.
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"discord.gg"}})
	mustContain(t, body(t, e.get(t, routerPath)), "забрал устаревшую версию")
	r, lst = fetchFeed(t, e, token, etag)
	if r.StatusCode != 200 || !strings.Contains(lst, "discord.gg") || r.Header.Get("ETag") == etag {
		t.Fatalf("после правки: %d %q", r.StatusCode, lst)
	}
	// Выключенная запись в фид не попадает.
	id := entryIDRe.FindStringSubmatch(body(t, e.get(t, "/lists/common/rows?q=discord")))[1]
	e.do(t, "PATCH", "/lists/common/entries/"+id, url.Values{"enabled": {"0"}})
	if _, lst = fetchFeed(t, e, token, ""); strings.Contains(lst, "discord.gg") {
		t.Fatal("выключенная запись в фиде")
	}

	// Состав фида: только games.
	var gamesID int64
	e.srv.DB.QueryRowContext(context.Background(), `SELECT id FROM lists WHERE slug='games'`).Scan(&gamesID)
	expectRedirect(t, e.post(t, "/feeds/"+feedID+"/lists", url.Values{"list": {itoa(gamesID)}}), routerPath)
	if _, lst = fetchFeed(t, e, token, ""); strings.Contains(lst, "104.29.0.0/16") || !strings.Contains(lst, "steampowered.com") {
		t.Fatalf("после смены состава:\n%s", lst)
	}

	// Выключение фида → 404, включение → снова отдаётся.
	expectRedirect(t, e.post(t, "/feeds/"+feedID+"/toggle", url.Values{"enabled": {"0"}}), routerPath)
	if r, _ := fetchFeed(t, e, token, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("выключенный фид: %d", r.StatusCode)
	}
	expectRedirect(t, e.post(t, "/feeds/"+feedID+"/toggle", url.Values{"enabled": {"1"}}), routerPath)

	// Новая ссылка: старая сразу 404.
	r = e.do(t, "POST", "/feeds/"+feedID+"/regenerate", nil)
	if r.Header.Get("HX-Redirect") != routerPath {
		t.Fatalf("regenerate через htmx: %d %q", r.StatusCode, r.Header.Get("HX-Redirect"))
	}
	if r, _ := fetchFeed(t, e, token, ""); r.StatusCode != http.StatusNotFound {
		t.Fatal("старая ссылка должна перестать работать")
	}
	newToken := feedURLRe.FindStringSubmatch(body(t, e.get(t, routerPath)))[1]
	if r, _ := fetchFeed(t, e, newToken, ""); r.StatusCode != 200 {
		t.Fatal("новая ссылка не работает")
	}

	// Мусорные токены.
	for _, p := range []string{"/f/nope.lst", "/f/" + newToken, "/f/.lst"} {
		resp, err := http.Get(e.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}

	// Вторая секция; повтор секции — ошибка; плохое имя секции — ошибка.
	expectRedirect(t, e.post(t, routerPath+"/feeds", url.Values{"section": {"geo"}}), routerPath)
	if r := e.post(t, routerPath+"/feeds", url.Values{"section": {"geo"}}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("повтор секции: %d", r.StatusCode)
	}
	if r := e.post(t, routerPath+"/feeds", url.Values{"section": {"bad name"}}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("плохая секция: %d", r.StatusCode)
	}

	// Удаление фида и роутера.
	e.do(t, "POST", "/feeds/"+feedID+"/delete", nil)
	if r, _ := fetchFeed(t, e, newToken, ""); r.StatusCode != http.StatusNotFound {
		t.Fatal("удалённый фид отдаётся")
	}
	e.do(t, "POST", routerPath+"/delete", nil)
	if r := e.get(t, routerPath); r.StatusCode != http.StatusNotFound {
		t.Fatalf("удалённый роутер: %d", r.StatusCode)
	}
}

func TestFeedPermissionsAndIncludes(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	ctx := context.Background()
	st := store.New(e.srv.DB)
	hash, _ := auth.HashPassword("friend-password")
	friendID, _ := st.CreateUser(ctx, "friend", hash, false, timeNow())

	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Секрет"}, "slug": {"secret"}}), "/lists/secret")
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"common.example"}})
	e.do(t, "POST", "/lists/secret/entries", url.Values{"input": {"secret.example"}})
	var commonID, secretID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug='common'`).Scan(&commonID)
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug='secret'`).Scan(&secretID)
	e.srv.DB.ExecContext(ctx, `INSERT INTO list_members (list_id, user_id, role) VALUES (?, ?, 'viewer')`, commonID, friendID)

	adminRouter, _, _ := setupRouter(t, e, "common")

	// Друг: чужой роутер не видит, свой создаёт, чужой список в фид не включить.
	f := newClient(e)
	f.login(t, "friend", "friend-password")
	if r := f.get(t, adminRouter); r.StatusCode != http.StatusNotFound {
		t.Fatalf("чужой роутер: %d", r.StatusCode)
	}
	if strings.Contains(body(t, f.get(t, "/routers")), `href="`+adminRouter+`"`) {
		t.Fatal("чужой роутер в перечне")
	}
	r := f.post(t, "/routers", url.Values{"name": {"друг"}})
	friendRouter := r.Header.Get("Location")
	if r := f.post(t, friendRouter+"/feeds", url.Values{"section": {"main"}, "list": {itoa(secretID)}}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("невидимый список в фиде: %d", r.StatusCode)
	}
	expectRedirect(t, f.post(t, friendRouter+"/feeds", url.Values{"section": {"main"}, "list": {itoa(commonID)}}), friendRouter)
	token := feedURLRe.FindStringSubmatch(body(t, f.get(t, friendRouter)))[1]
	if _, lst := fetchFeed(t, e, token, ""); !strings.Contains(lst, "common.example") {
		t.Fatalf("фид друга: %q", lst)
	}

	// Вложенный список: secret внутри common — попадает и в фид друга (доступ наследуется от common).
	e.srv.DB.ExecContext(ctx, `INSERT INTO list_includes (parent_id, child_id, created_at) VALUES (?, ?, 0)`, commonID, secretID)
	e.srv.DB.ExecContext(ctx, `INSERT INTO list_includes (parent_id, child_id, created_at) VALUES (?, ?, 0)`, secretID, commonID) // цикл
	if _, lst := fetchFeed(t, e, token, ""); !strings.Contains(lst, "secret.example") || strings.Count(lst, "common.example") != 1 {
		t.Fatalf("вложенный список (с циклом): %q", lst)
	}

	// Отзыв доступа: common пропадает из фида друга без правки фида.
	e.srv.DB.ExecContext(ctx, `DELETE FROM list_members WHERE user_id = ?`, friendID)
	if _, lst := fetchFeed(t, e, token, ""); lst != "listok-sentinel.invalid\n" {
		t.Fatalf("после отзыва доступа фид должен быть пустым: %q", lst)
	}
}
