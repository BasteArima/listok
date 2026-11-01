package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BasteArima/listok/agent"
)

// Секции forkop на странице роутера (D-038): агент ставится без секций, присылает секции forkop,
// секцию подключают кнопкой, состав меняется переключателями.
func TestForkopSectionsFlow(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	ctx := context.Background()
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"one.example"}})
	var listID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug = 'common'`).Scan(&listID)

	r := e.post(t, "/routers", url.Values{"name": {"дом"}})
	routerPath := r.Header.Get("Location")
	agentToken, script := installAgent(t, e, routerPath, url.Values{})
	mustContain(t, script, "SECTIONS=''")

	// Агент 0.3.x секций не присылает — страница просит переустановить.
	agentPost(t, e, agentToken, "/agent/v1/hello", map[string]any{"agent_version": "0.3.1"})
	mustContain(t, body(t, e.get(t, routerPath)), "не присылает список секций forkop")

	// Агент 0.4.0 присылает секции: по карточке на каждую, неподходящее название не подключить.
	hello := func(sections any) {
		t.Helper()
		r, b := agentPost(t, e, agentToken, "/agent/v1/hello", map[string]any{"agent_version": agent.Version, "forkop_sections": sections})
		if r.StatusCode != 200 {
			t.Fatalf("hello: %d %s", r.StatusCode, b)
		}
	}
	hello([]map[string]string{{"name": "main", "action": "connection"}, {"name": "geo", "action": "dns"},
		{"name": "Bad name", "action": "connection"}, {"name": "main", "action": "dns"}, {"name": " "}})
	page := body(t, e.get(t, routerPath))
	mustContain(t, page, "список пришёл с роутера", ">main<", ">geo<", ">DNS<", "Bad name", "Название не подходит",
		`name="section" value="main"`, `name="section" value="geo"`, "Подключить секцию")
	if strings.Contains(page, `name="section" value="Bad name"`) || strings.Count(page, ">main<") != 1 {
		t.Fatal("неподходящее название нельзя подключить, повторы секций схлопываются")
	}

	// Агент без секций ждёт новые; старому (без флага added) они не сообщаются.
	waitAdded := func(feeds map[string]string) waitResponse {
		t.Helper()
		_, b := agentPost(t, e, agentToken, "/agent/v1/wait", map[string]any{"feeds": feeds, "wait": 0, "added": true})
		var resp waitResponse
		json.Unmarshal([]byte(b), &resp)
		return resp
	}
	if resp := waitAdded(map[string]string{}); len(resp.Added) != 0 {
		t.Fatalf("до подключения: %+v", resp)
	}

	// «Подключить секцию» → у агента она в added, на странице — «подключается» и переключатели.
	expectHXRedirect(t, e.do(t, "POST", routerPath+"/feeds", url.Values{"section": {"main"}}), routerPath)
	if resp := waitAdded(map[string]string{}); strings.Join(resp.Added, ",") != "main" {
		t.Fatalf("added после подключения: %+v", resp)
	}
	if resp, _, _ := agentWait(t, e, agentToken, map[string]string{}, 0); len(resp.Added) != 0 {
		t.Fatalf("агенту без флага added не сообщается: %+v", resp)
	}
	page = body(t, e.get(t, routerPath))
	mustContain(t, page, "Подключается", `hx-post="/feeds/`, `name="list" value="`+itoa(listID)+`"`)
	f := feedIDRe.FindStringSubmatch(page)
	if f == nil {
		t.Fatal("нет карточки подключённой секции")
	}

	// Переключатель списка сохраняет сразу и возвращает карточку с отмеченным списком.
	card := body(t, e.do(t, "POST", "/feeds/"+f[1]+"/lists", url.Values{"list": {itoa(listID)}}))
	mustContain(t, card, `id="section-`+f[1]+`"`, `value="`+itoa(listID)+`" checked`, "1 запись")
	if strings.Contains(card, "<html") {
		t.Fatal("htmx получает только карточку")
	}

	// Секция есть в listok, но агент её в forkop не видит.
	expectRedirect(t, e.post(t, routerPath+"/feeds", url.Values{"section": {"other"}}), routerPath)
	mustContain(t, body(t, e.get(t, routerPath)), "В forkop на роутере нет секции")

	// hello без forkop_sections (старый агент) список секций не стирает.
	agentPost(t, e, agentToken, "/agent/v1/hello", map[string]any{"agent_version": "0.3.1"})
	mustContain(t, body(t, e.get(t, routerPath)), "список пришёл с роутера", `name="section" value="geo"`)

	// Секция, подключённая по UCI-имени (geo, а в LuCI она geoblock), — та же секция, а не две.
	hello([]map[string]string{{"name": "main", "uci": "cfg01a2b3", "action": "connection"}, {"name": "geoblock", "uci": "geo", "action": "dns"}})
	expectRedirect(t, e.post(t, routerPath+"/feeds", url.Values{"section": {"geo"}}), routerPath)
	page = body(t, e.get(t, routerPath))
	mustContain(t, page, ">geoblock<")
	if strings.Count(page, "В forkop на роутере нет секции") != 1 || strings.Contains(page, `name="section" value="geoblock"`) {
		t.Fatal("geo должна показаться подключённой карточкой geoblock, а не отдельной секцией")
	}

	// Отключить от listok → у агента секция в gone.
	var token string
	e.srv.DB.QueryRowContext(ctx, `SELECT token FROM feeds WHERE section = 'main'`).Scan(&token)
	fr, _ := fetchFeed(t, e, token, "")
	expectHXRedirect(t, e.do(t, "POST", "/feeds/"+f[1]+"/delete", nil), routerPath)
	if resp := waitAdded(map[string]string{"main": fr.Header.Get("ETag"), "other": `"x"`}); strings.Join(resp.Gone, ",") != "main" {
		t.Fatalf("gone после отключения: %+v", resp)
	}
	if r := e.do(t, "GET", routerPath+"/sections/"+f[1], nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("карточка отключённой секции: %d", r.StatusCode)
	}
}

// Команда установки видна на странице роутера сразу (D-039): та же при повторном открытии,
// новая — после установки или когда прежней осталось жить меньше часа.
func TestInstallLinkAlwaysShown(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	r := e.post(t, "/routers", url.Values{"name": {"дом"}})
	routerPath := r.Header.Get("Location")

	tokenOf := func() string {
		t.Helper()
		m := installTokenRe.FindStringSubmatch(body(t, e.get(t, routerPath)))
		if m == nil {
			t.Fatal("на странице роутера нет готовой команды установки")
		}
		return m[1]
	}
	first := tokenOf()
	if tokenOf() != first {
		t.Fatal("повторное открытие страницы не должно менять ссылку")
	}
	page := body(t, e.get(t, routerPath))
	mustContain(t, page, `data-token="`+first+`"`, `data-base="`+e.ts.URL+`"`, "другая ссылка")

	// Скоро истечёт — заменяется.
	e.srv.DB.ExecContext(context.Background(), `UPDATE routers SET install_expires_at = unixepoch() + 600`)
	second := tokenOf()
	if second == first {
		t.Fatal("ссылку, которой осталось жить 10 минут, нужно заменить")
	}

	// Использованная — тоже.
	if r, _ := getUA(t, e, "/install/"+second, "curl/8.12.1"); r.StatusCode != http.StatusOK {
		t.Fatalf("установка: %d", r.StatusCode)
	}
	if third := tokenOf(); third == second {
		t.Fatal("после установки на странице должна быть новая ссылка")
	}
}
