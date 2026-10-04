package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/BasteArima/listok/internal/auth"
	"github.com/BasteArima/listok/internal/store"
)

// expectHXRedirect — ответ на htmx-форму с hx-confirm: 204 и HX-Redirect.
func expectHXRedirect(t *testing.T, r *http.Response, to string) {
	t.Helper()
	if r.StatusCode != http.StatusNoContent || r.Header.Get("HX-Redirect") != to {
		t.Fatalf("ожидали HX-Redirect на %s, получено %d %q", to, r.StatusCode, r.Header.Get("HX-Redirect"))
	}
}

func TestClearList(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	ctx := context.Background()

	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Работа"}, "slug": {"work"}}), "/lists/work")
	page := body(t, e.get(t, "/lists/work"))
	if strings.Contains(page, "Очистить список") {
		t.Fatal("у пустого списка не должно быть кнопки очистки")
	}
	mustContain(t, page, "Удалить список")

	e.do(t, "POST", "/lists/work/entries", url.Values{"input": {"youtube.com discord.gg 104.29.0.0/16"}})
	e.do(t, "POST", "/lists/work/entries", url.Values{"input": {"anilist.co"}})
	m := entryIDRe.FindStringSubmatch(body(t, e.get(t, "/lists/work/rows?q=anilist")))
	e.do(t, "PATCH", "/lists/work/entries/"+m[1], url.Values{"enabled": {"0"}}) // выключенная тоже должна уйти
	mustContain(t, body(t, e.get(t, "/lists/work")), "Очистить список", "Удалить все записи (4)")

	expectHXRedirect(t, e.do(t, "POST", "/lists/work/clear", nil), "/lists/work")
	if rows := body(t, e.get(t, "/lists/work/rows")); !strings.Contains(rows, "Записей пока нет") {
		t.Fatal("после очистки записи остались")
	}

	// Очистка — одна версия со всеми удалениями.
	var version, removed int64
	var msg string
	e.srv.DB.QueryRowContext(ctx, `SELECT l.version, v.message,
		(SELECT count(*) FROM entry_changes c WHERE c.version_id = v.id AND c.op = 'remove')
		FROM lists l JOIN list_versions v ON v.list_id = l.id AND v.version = l.version WHERE l.slug = 'work'`).
		Scan(&version, &msg, &removed)
	if version != 4 || msg != "очистка списка" || removed != 4 {
		t.Fatalf("версия %d, сообщение %q, удалений %d; ожидали 4, «очистка списка», 4", version, msg, removed)
	}

	// Индекс покрытия очищен: домен больше не «уже покрыт в этом списке».
	var listID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug = 'work'`).Scan(&listID)
	if b := body(t, e.do(t, "POST", "/preview", url.Values{"input": {"youtube.com"}, "list": {itoa(listID)}})); strings.Contains(b, "покрыто") {
		t.Fatal("после очистки запись всё ещё в индексе покрытия")
	}

	// Пустой список очищать нечего: повтор ничего не меняет.
	expectHXRedirect(t, e.do(t, "POST", "/lists/work/clear", nil), "/lists/work")
	e.srv.DB.QueryRowContext(ctx, `SELECT version FROM lists WHERE slug = 'work'`).Scan(&version)
	if version != 4 {
		t.Fatalf("очистка пустого списка подняла версию до %d", version)
	}

	// Очистку можно откатить: вернутся все записи, включая выключенную.
	if r := e.do(t, "POST", "/lists/work/rollback/3", nil); r.StatusCode >= 400 {
		t.Fatalf("откат очистки: %d", r.StatusCode)
	}
	rows := body(t, e.get(t, "/lists/work/rows"))
	mustContain(t, rows, "youtube.com", "discord.gg", "104.29.0.0/16", "anilist.co")
}

func TestDeleteList(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	ctx := context.Background()

	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Работа"}, "slug": {"work"}}), "/lists/work")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Игры"}, "slug": {"games"}}), "/lists/games")
	e.do(t, "POST", "/lists/work/entries", url.Values{"input": {"youtube.com"}})
	e.do(t, "POST", "/lists/games/entries", url.Values{"input": {"steamcommunity.com"}})

	_, token, _ := setupRouter(t, e, "work", "games")
	_, feed := fetchFeed(t, e, token, "")
	mustContain(t, feed, "youtube.com", "steamcommunity.com")
	mustContain(t, body(t, e.get(t, "/lists/work")), "подключён к фидам: 1", "его записи сразу пропадут с роутеров")

	expectHXRedirect(t, e.do(t, "POST", "/lists/work/delete", nil), "/lists")

	if r := e.get(t, "/lists/work"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("удалённый список отдаёт %d", r.StatusCode)
	}
	if strings.Contains(body(t, e.get(t, "/lists")), `href="/lists/work"`) {
		t.Fatal("удалённый список остался в перечне")
	}
	// Фид продолжает работать, но без записей удалённого списка.
	r, feed := fetchFeed(t, e, token, "")
	if r.StatusCode != http.StatusOK || strings.Contains(feed, "youtube.com") || !strings.Contains(feed, "steamcommunity.com") {
		t.Fatalf("фид после удаления списка: %d\n%s", r.StatusCode, feed)
	}
	// Каскад: ни записей, ни истории не осталось.
	var left int
	e.srv.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM entries WHERE value = 'youtube.com')
		+ (SELECT count(*) FROM entry_changes WHERE value = 'youtube.com')`).Scan(&left)
	if left != 0 {
		t.Fatalf("после удаления осталось строк записей/истории: %d", left)
	}
	// Аудит.
	var details string
	if err := e.srv.DB.QueryRowContext(ctx, `SELECT details FROM audit_log WHERE action = 'list.delete'`).Scan(&details); err != nil {
		t.Fatal("нет записи list.delete в audit_log:", err)
	}
	mustContain(t, details, `"slug":"work"`, `"entries":1`, `"feeds":1`)
	// Индекс покрытия: удалённый список больше не упоминается.
	if b := body(t, e.do(t, "POST", "/preview", url.Values{"input": {"youtube.com"}})); strings.Contains(b, "Работа") {
		t.Fatal("удалённый список всё ещё в индексе покрытия")
	}
	// Адрес освободился.
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Работа заново"}, "slug": {"work"}}), "/lists/work")
	if rows := body(t, e.get(t, "/lists/work/rows")); !strings.Contains(rows, "Записей пока нет") {
		t.Fatal("новый список с тем же адресом унаследовал записи")
	}

	// SQLite отдаёт новой строке max(id)+1, поэтому после удаления последнего списка
	// следующий получает тот же id. Записи удалённого не должны «покрывать» записи нового.
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Временный"}, "slug": {"tmp"}}), "/lists/tmp")
	e.do(t, "POST", "/lists/tmp/entries", url.Values{"input": {"example.net"}})
	var oldID, newID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug = 'tmp'`).Scan(&oldID)
	expectHXRedirect(t, e.do(t, "POST", "/lists/tmp/delete", nil), "/lists")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Новый"}, "slug": {"fresh"}}), "/lists/fresh")
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug = 'fresh'`).Scan(&newID)
	if oldID != newID {
		t.Fatalf("сценарий не воспроизведён: id %d и %d различаются", oldID, newID)
	}
	b := body(t, e.do(t, "POST", "/lists/fresh/entries", url.Values{"input": {"example.net"}}))
	mustContain(t, b, "Добавлено: 1")
}

func TestBulkEntries(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	ctx := context.Background()

	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Работа"}, "slug": {"work"}}), "/lists/work")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Чужой"}, "slug": {"other"}}), "/lists/other")
	e.do(t, "POST", "/lists/work/entries", url.Values{"input": {"a.example b.example c.example d.example"}})
	e.do(t, "POST", "/lists/other/entries", url.Values{"input": {"z.example"}})
	ids := map[string]string{}
	for _, slug := range []string{"work", "other"} {
		for _, m := range regexp.MustCompile(`id="entry-(\d+)"[^>]*>\s*<td class="sel">.*?</td>\s*<td class="mono value">([^<]+)<`).
			FindAllStringSubmatch(body(t, e.get(t, "/lists/"+slug+"/rows")), -1) {
			ids[m[2]] = m[1]
		}
	}
	mustContain(t, body(t, e.get(t, "/lists/work")), "data-select-mode", `id="bulk-bar"`, `name="id" value="`+ids["a.example"]+`"`)
	version := func() (v int64) {
		e.srv.DB.QueryRowContext(ctx, `SELECT version FROM lists WHERE slug = 'work'`).Scan(&v)
		return
	}

	// Выключить две: одна версия, ответ — таблица и счётчики.
	b := body(t, e.do(t, "POST", "/lists/work/entries/bulk", url.Values{"op": {"disable"}, "id": {ids["a.example"], ids["b.example"]}}))
	mustContain(t, b, `<tbody id="rows"`, `id="list-stats" class="stats muted small" hx-swap-oob="true"`, "2</b> включено из 4")
	if v := version(); v != 2 {
		t.Fatalf("выключение двух записей: версия %d, ожидали 2", v)
	}
	// Включить обратно одну.
	e.do(t, "POST", "/lists/work/entries/bulk", url.Values{"op": {"enable"}, "id": {ids["a.example"]}})

	// Удалить: чужая запись, повтор и мусор пропускаются, таблица отдаётся с текущим фильтром.
	b = body(t, e.do(t, "POST", "/lists/work/entries/bulk", url.Values{
		"op": {"delete"}, "id": {ids["a.example"], ids["c.example"], ids["c.example"], ids["z.example"], "abc"}, "q": {"example"},
	}))
	if strings.Contains(b, "a.example") || strings.Contains(b, "c.example") || !strings.Contains(b, "b.example") || !strings.Contains(b, "d.example") {
		t.Fatalf("после удаления выбранных:\n%s", b)
	}
	if v := version(); v != 4 {
		t.Fatalf("удаление выбранных: версия %d, ожидали 4 (одна версия на действие)", v)
	}
	if !strings.Contains(body(t, e.get(t, "/lists/other/rows")), "z.example") {
		t.Fatal("массовое удаление задело чужой список")
	}
	var msg string
	var removed int
	e.srv.DB.QueryRowContext(ctx, `SELECT v.message, (SELECT count(*) FROM entry_changes c WHERE c.version_id = v.id)
		FROM list_versions v JOIN lists l ON l.id = v.list_id WHERE l.slug = 'work' AND v.version = 4`).Scan(&msg, &removed)
	if removed != 2 || !strings.HasPrefix(msg, "удаление выбранных") {
		t.Fatalf("версия удаления: %q, изменений %d", msg, removed)
	}

	// Ничего не выбрано — ничего не меняется; неизвестное действие — 400.
	e.do(t, "POST", "/lists/work/entries/bulk", url.Values{"op": {"delete"}})
	if v := version(); v != 4 {
		t.Fatalf("пустой выбор поднял версию до %d", v)
	}
	if r := e.do(t, "POST", "/lists/work/entries/bulk", url.Values{"op": {"drop"}, "id": {ids["b.example"]}}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("неизвестное действие: %d", r.StatusCode)
	}
}

// Кеш фида держит ключ «id:версия». Удалённый список может отдать свой id новому,
// и если тот дорастёт до той же версии без опросов роутера, ключ совпадёт со старым.
func TestFeedCacheAfterListDelete(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	ctx := context.Background()

	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Старый"}, "slug": {"old"}}), "/lists/old")
	e.do(t, "POST", "/lists/old/entries", url.Values{"input": {"one.example"}})
	routerPath, token, feedID := setupRouter(t, e, "old")
	if _, feed := fetchFeed(t, e, token, ""); !strings.Contains(feed, "one.example") {
		t.Fatal("в фиде нет записи старого списка")
	}

	var oldID, newID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug = 'old'`).Scan(&oldID)
	expectHXRedirect(t, e.do(t, "POST", "/lists/old/delete", nil), "/lists")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Новый"}, "slug": {"new"}}), "/lists/new")
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug = 'new'`).Scan(&newID)
	if oldID != newID {
		t.Fatalf("сценарий не воспроизведён: id %d и %d различаются", oldID, newID)
	}
	e.do(t, "POST", "/lists/new/entries", url.Values{"input": {"two.example"}}) // та же версия 1
	expectRedirect(t, e.post(t, "/feeds/"+feedID+"/lists", url.Values{"list": {itoa(newID)}}), routerPath)

	_, feed := fetchFeed(t, e, token, "")
	if strings.Contains(feed, "one.example") || !strings.Contains(feed, "two.example") {
		t.Fatalf("фид отдал устаревший кеш удалённого списка:\n%s", feed)
	}
}

func TestClearDeletePermissions(t *testing.T) {
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
	e.do(t, "POST", "/lists/mine/entries", url.Values{"input": {"example.org"}})
	var listID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug = 'mine'`).Scan(&listID)

	f := newClient(e)
	f.login(t, "friend", "friend-password")

	// Viewer: ни кнопок, ни права.
	e.srv.DB.ExecContext(ctx, `INSERT INTO list_members (list_id, user_id, role) VALUES (?, ?, 'viewer')`, listID, friendID)
	if page := body(t, f.get(t, "/lists/mine")); strings.Contains(page, "Очистить или удалить") || strings.Contains(page, "data-select-mode") || strings.Contains(page, `name="id"`) {
		t.Fatal("viewer видит очистку, удаление или массовый выбор")
	}
	if r := f.do(t, "POST", "/lists/mine/entries/bulk", url.Values{"op": {"delete"}, "id": {"1"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer массово удаляет: %d", r.StatusCode)
	}
	for _, p := range []string{"/lists/mine/clear", "/lists/mine/delete"} {
		if r := f.do(t, "POST", p, nil); r.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer %s: %d", p, r.StatusCode)
		}
	}

	// Editor: очищать может, удалять список — нет.
	e.srv.DB.ExecContext(ctx, `UPDATE list_members SET role = 'editor' WHERE list_id = ? AND user_id = ?`, listID, friendID)
	page := body(t, f.get(t, "/lists/mine"))
	if !strings.Contains(page, "Очистить список") || strings.Contains(page, "Удалить список") {
		t.Fatal("editor должен видеть очистку, но не удаление")
	}
	if r := f.do(t, "POST", "/lists/mine/delete", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("editor удаляет список: %d", r.StatusCode)
	}
	expectHXRedirect(t, f.do(t, "POST", "/lists/mine/clear", nil), "/lists/mine")

	// Чужой пользователь без роли: списка для него нет вовсе.
	e.srv.DB.ExecContext(ctx, `DELETE FROM list_members WHERE list_id = ?`, listID)
	if r := f.do(t, "POST", "/lists/mine/delete", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("чужой список удаляется: %d", r.StatusCode)
	}
}
