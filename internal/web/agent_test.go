package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BasteArima/listok/agent"
)

// fetchFeedWait — запрос роутера с long-poll.
func fetchFeedWait(t *testing.T, e *env, token, etag string, wait int) (*http.Response, string, time.Duration) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/f/"+token+".lst?wait="+itoa(int64(wait)), nil)
	req.Header.Set("If-None-Match", etag)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, sb.String(), time.Since(start)
}

func TestFeedLongPoll(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"one.example"}})
	_, token, _ := setupRouter(t, e, "common")
	r, _ := fetchFeed(t, e, token, "")
	etag := r.Header.Get("ETag")

	// Изменений нет: запрос ждёт wait и отвечает 304.
	r, _, took := fetchFeedWait(t, e, token, etag, 1)
	if r.StatusCode != http.StatusNotModified || took < 900*time.Millisecond {
		t.Fatalf("без изменений: %d за %v, ожидали 304 примерно через 1 с", r.StatusCode, took)
	}

	// Изменение во время ожидания: ответ 200 с новой версией почти сразу.
	go func() {
		time.Sleep(300 * time.Millisecond)
		e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"two.example"}})
	}()
	r, body, took := fetchFeedWait(t, e, token, etag, 5)
	if r.StatusCode != http.StatusOK || !strings.Contains(body, "two.example") || took > 3*time.Second {
		t.Fatalf("изменение во время ожидания: %d за %v\n%s", r.StatusCode, took, body)
	}
	if r.Header.Get("ETag") == etag {
		t.Fatal("ETag не изменился")
	}

	// Журнал: один итог на запрос, а не каждое пробуждение (304, 200 + первый fetchFeed = 3).
	var n int
	e.srv.DB.QueryRowContext(context.Background(), `SELECT count(*) FROM feed_fetches`).Scan(&n)
	if n != 3 {
		t.Errorf("записей в журнале опросов %d, ожидали 3", n)
	}

	// Перевыпуск ссылки будит ожидающих: старый токен сразу 404, а не ждёт до таймаута.
	go func() {
		time.Sleep(300 * time.Millisecond)
		var feedID int64
		e.srv.DB.QueryRowContext(context.Background(), `SELECT id FROM feeds`).Scan(&feedID)
		e.do(t, "POST", "/feeds/"+itoa(feedID)+"/regenerate", nil)
	}()
	r, _, took = fetchFeedWait(t, e, token, r.Header.Get("ETag"), 5)
	if r.StatusCode != http.StatusNotFound || took > 3*time.Second {
		t.Fatalf("перевыпуск во время ожидания: %d за %v", r.StatusCode, took)
	}
}

var installTokenRe = regexp.MustCompile(`/install/([0-9A-Za-z]{43})`)

func getUA(t *testing.T, e *env, path, ua string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, body(t, resp)
}

func agentPost(t *testing.T, e *env, token, path string, payload any) (*http.Response, string) {
	t.Helper()
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+path, strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, body(t, resp)
}

func TestAgentInstallAndAPI(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	ctx := context.Background()
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"one.example"}})

	// Роутер без фидов: ссылку выдать можно, но установка её не тратит и объясняет почему.
	r := e.post(t, "/routers", url.Values{"name": {"дача"}})
	emptyRouter := r.Header.Get("Location")
	page := body(t, e.post(t, emptyRouter+"/install", url.Values{}))
	m := installTokenRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("нет команды установки на странице")
	}
	if r, b := getUA(t, e, "/install/"+m[1], "curl/8.12.1"); r.StatusCode != http.StatusConflict || !strings.Contains(b, "нет включённых фидов") {
		t.Fatalf("установка без фидов: %d %s", r.StatusCode, b)
	}

	routerPath, feedToken, _ := setupRouter(t, e, "common")
	mustContain(t, body(t, e.get(t, routerPath)), "Агент на роутере", "не установлен", "Выдать ссылку установки")

	// Неверный IP сервера — ошибка формы. Верный — команда с --resolve и ?ip=.
	if r := e.post(t, routerPath+"/install", url.Values{"server_ip": {"192.168.0"}}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("плохой IP: %d", r.StatusCode)
	}
	page = body(t, e.post(t, routerPath+"/install", url.Values{"server_ip": {"192.168.1.10"}}))
	host := strings.TrimPrefix(e.ts.URL, "http://")
	hostname, port, _ := strings.Cut(host, ":")
	mustContain(t, page, "curl -fsSL --resolve "+hostname+":"+port+":192.168.1.10 &#39;"+e.ts.URL+"/install/", "?ip=192.168.1.10&#39; | sh")
	m = installTokenRe.FindStringSubmatch(page)
	install := "/install/" + m[1] + "?ip=192.168.1.10"

	// Браузер (или предпросмотр ссылки) получает подсказку и ссылку не тратит.
	if r, b := getUA(t, e, install, "Mozilla/5.0 TelegramBot"); r.StatusCode != http.StatusOK || !strings.Contains(b, "выполнить <b>на роутере</b>") {
		t.Fatalf("страница для браузера: %d", r.StatusCode)
	}
	// curl получает установщик со всеми токенами.
	r, script := getUA(t, e, install, "curl/8.12.1")
	if r.StatusCode != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/x-shellscript") {
		t.Fatalf("установщик: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	mustContain(t, script, "SERVER='"+e.ts.URL+"'", "SECTIONS='main'", "option server_ip '192.168.1.10'",
		"option token '"+feedToken+"'", "const VERSION = '"+agent.Version+"';")
	at := regexp.MustCompile(`config agent 'agent'[\s\S]*?option token '([0-9A-Za-z]{43})'`).FindStringSubmatch(script)
	if at == nil {
		t.Fatal("в установщике нет токена агента")
	}
	agentToken := at[1]
	// Ссылка одноразовая.
	if r, b := getUA(t, e, install, "curl/8.12.1"); r.StatusCode != http.StatusNotFound || !strings.Contains(b, "exit 1") {
		t.Fatalf("повторная установка: %d", r.StatusCode)
	}
	// Хранится только хеш токена агента.
	var stored string
	e.srv.DB.QueryRowContext(ctx, `SELECT coalesce(agent_token_hash, '') FROM routers WHERE agent_token_hash IS NOT NULL`).Scan(&stored)
	if stored == "" || stored == agentToken {
		t.Fatal("токен агента должен храниться как хеш")
	}

	// hello: без токена 401, с токеном — фиды и версия агента на сервере.
	if r, _ := agentPost(t, e, "wrong", "/agent/v1/hello", map[string]any{}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("hello с чужим токеном: %d", r.StatusCode)
	}
	r, b := agentPost(t, e, agentToken, "/agent/v1/hello", map[string]any{
		"agent_version": agent.Version, "forkop_version": "1.0.5", "singbox_version": "1.14.1~extended~2.7.2\x00", "sections": []string{"main"},
	})
	var hello helloResponse
	if r.StatusCode != http.StatusOK || json.Unmarshal([]byte(b), &hello) != nil {
		t.Fatalf("hello: %d %s", r.StatusCode, b)
	}
	if len(hello.Feeds) != 1 || hello.Feeds[0].Section != "main" || !strings.Contains(hello.Feeds[0].URL, feedToken) || hello.AgentVersion != agent.Version {
		t.Fatalf("ответ hello: %+v", hello)
	}

	// applied: ошибка и успех видны на карточке роутера.
	resp, _ := fetchFeed(t, e, feedToken, "")
	etag := resp.Header.Get("ETag")
	if r, _ := agentPost(t, e, agentToken, "/agent/v1/applied", map[string]any{"section": "nope", "etag": etag, "ok": true}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("applied чужой секции: %d", r.StatusCode)
	}
	agentPost(t, e, agentToken, "/agent/v1/applied", map[string]any{"section": "main", "etag": etag, "ok": false, "error": "forkop list_update не пересобрал rule-set секции main"})
	mustContain(t, body(t, e.get(t, routerPath)), "агент не смог применить: forkop list_update не пересобрал")
	if r, _ := agentPost(t, e, agentToken, "/agent/v1/applied", map[string]any{"section": "main", "etag": etag, "ok": true}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("applied: %d", r.StatusCode)
	}
	page = body(t, e.get(t, routerPath))
	mustContain(t, page, "● на связи", "агент "+agent.Version, "forkop 1.0.5", "sing-box 1.14.1~extended~2.7.2<", "● применено на роутере", "Отвязать агент")

	// Новая версия списка: на роутере ещё прежняя.
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"two.example"}})
	mustContain(t, body(t, e.get(t, routerPath)), "на роутере применена версия "+html.EscapeString(etag))

	// Отвязать агент: его токен больше не работает.
	expectHXRedirect(t, e.do(t, "POST", routerPath+"/agent/revoke", nil), routerPath)
	if r, _ := agentPost(t, e, agentToken, "/agent/v1/hello", map[string]any{}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("hello после отвязки: %d", r.StatusCode)
	}

	// Просроченная ссылка не работает.
	page = body(t, e.post(t, routerPath+"/install", url.Values{}))
	m = installTokenRe.FindStringSubmatch(page)
	e.srv.DB.ExecContext(ctx, `UPDATE routers SET install_expires_at = 1 WHERE install_token_hash IS NOT NULL`)
	if r, _ := getUA(t, e, "/install/"+m[1], "curl/8.12.1"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("просроченная ссылка: %d", r.StatusCode)
	}
}

func TestPublicRateLimit(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.srv.publicLimit = newRateLimit(3)
	for i := 1; i <= 4; i++ {
		r, _ := fetchFeed(t, e, "nope", "")
		want := http.StatusNotFound
		if i == 4 {
			want = http.StatusTooManyRequests
		}
		if r.StatusCode != want {
			t.Fatalf("запрос %d: %d, ожидали %d", i, r.StatusCode, want)
		}
	}
}

// installAgent выдаёт ссылку установки, «запускает» её как curl и возвращает токен агента и установщик.
func installAgent(t *testing.T, e *env, routerPath string, form url.Values) (string, string) {
	t.Helper()
	page := body(t, e.post(t, routerPath+"/install", form))
	m := regexp.MustCompile(`/install/[0-9A-Za-z]{43}(\?[^&#]*(&amp;[^&#']*)*)?`).FindString(page)
	if m == "" {
		t.Fatalf("нет команды установки:\n%s", page)
	}
	_, script := getUA(t, e, html.UnescapeString(m), "curl/8.12.1")
	at := regexp.MustCompile(`config agent 'agent'[\s\S]*?option token '([0-9A-Za-z]{43})'`).FindStringSubmatch(script)
	if at == nil {
		t.Fatalf("в установщике нет токена агента:\n%s", script)
	}
	return at[1], script
}

func agentWait(t *testing.T, e *env, token string, feeds map[string]string, wait int) (waitResponse, time.Duration, int) {
	t.Helper()
	start := time.Now()
	r, b := agentPost(t, e, token, "/agent/v1/wait", map[string]any{"feeds": feeds, "wait": wait})
	var resp waitResponse
	if r.StatusCode == http.StatusOK {
		if err := json.Unmarshal([]byte(b), &resp); err != nil {
			t.Fatalf("ответ wait: %v\n%s", err, b)
		}
	}
	return resp, time.Since(start), r.StatusCode
}

func TestAgentWaitAllSections(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	ctx := context.Background()
	for _, l := range []string{"main", "geo"} {
		expectRedirect(t, e.post(t, "/lists", url.Values{"title": {l}, "slug": {"l" + l}}), "/lists/l"+l)
		e.do(t, "POST", "/lists/l"+l+"/entries", url.Values{"input": {l + ".example"}})
	}
	routerPath, mainToken, _ := setupRouter(t, e, "lmain")
	var geoListID int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE slug = 'lgeo'`).Scan(&geoListID)
	expectRedirect(t, e.post(t, routerPath+"/feeds", url.Values{"section": {"geo"}, "list": {itoa(geoListID)}}), routerPath)
	agentToken, _ := installAgent(t, e, routerPath, url.Values{})

	etagOf := func(section string) string {
		var token string
		e.srv.DB.QueryRowContext(ctx, `SELECT token FROM feeds WHERE section = ?`, section).Scan(&token)
		r, _ := fetchFeed(t, e, token, "")
		return r.Header.Get("ETag")
	}
	cur := map[string]string{"main": etagOf("main"), "geo": etagOf("geo")}

	// Без изменений: ждёт и отвечает пустым списком. wait = 0 (режим poll) — сразу.
	resp, took, code := agentWait(t, e, agentToken, cur, 1)
	if code != 200 || len(resp.Changed)+len(resp.Gone) != 0 || took < 900*time.Millisecond {
		t.Fatalf("без изменений: %d %+v за %v", code, resp, took)
	}
	if resp, took, _ := agentWait(t, e, agentToken, cur, 0); len(resp.Changed) != 0 || took > 500*time.Millisecond {
		t.Fatalf("poll: %+v за %v", resp, took)
	}

	// Изменение одной секции во время ожидания — ответ сразу и только про неё.
	go func() {
		time.Sleep(300 * time.Millisecond)
		e.do(t, "POST", "/lists/lgeo/entries", url.Values{"input": {"geo2.example"}})
	}()
	resp, took, _ = agentWait(t, e, agentToken, cur, 5)
	if strings.Join(resp.Changed, ",") != "geo" || took > 3*time.Second {
		t.Fatalf("изменилась geo: %+v за %v", resp, took)
	}

	// Обе устарели — обе в ответе, без ожидания.
	if resp, _, _ := agentWait(t, e, agentToken, map[string]string{"main": `"old"`, "geo": `"old"`}, 5); strings.Join(resp.Changed, ",") != "geo,main" {
		t.Fatalf("обе устарели: %+v", resp)
	}

	// Выключенный фид — «пропал».
	var geoFeed int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM feeds WHERE section = 'geo'`).Scan(&geoFeed)
	e.post(t, "/feeds/"+itoa(geoFeed)+"/toggle", url.Values{"enabled": {"0"}})
	cur["geo"] = etagOf("geo")
	if resp, _, _ := agentWait(t, e, agentToken, map[string]string{"main": etagOf("main"), "geo": `"x"`}, 5); strings.Join(resp.Gone, ",") != "geo" {
		t.Fatalf("выключенный фид: %+v", resp)
	}

	// Ошибки запроса.
	if _, _, code := agentWait(t, e, agentToken, map[string]string{}, 0); code != http.StatusBadRequest {
		t.Fatalf("пустой список секций: %d", code)
	}
	if _, _, code := agentWait(t, e, agentToken, map[string]string{"bad name": ""}, 0); code != http.StatusBadRequest {
		t.Fatalf("плохая секция: %d", code)
	}
	if _, _, code := agentWait(t, e, "wrong", cur, 0); code != http.StatusUnauthorized {
		t.Fatalf("чужой токен: %d", code)
	}

	// hello отдаёт токены фидов (агент подхватывает перевыпущенную ссылку) и запоминает режим.
	var mainFeed int64
	e.srv.DB.QueryRowContext(ctx, `SELECT id FROM feeds WHERE section = 'main'`).Scan(&mainFeed)
	e.do(t, "POST", "/feeds/"+itoa(mainFeed)+"/regenerate", nil)
	r, b := agentPost(t, e, agentToken, "/agent/v1/hello", map[string]any{"agent_version": agent.Version, "mode": "poll", "interval_s": 600})
	var hello helloResponse
	json.Unmarshal([]byte(b), &hello)
	if r.StatusCode != 200 || len(hello.Feeds) != 1 || hello.Feeds[0].Token == "" || hello.Feeds[0].Token == mainToken {
		t.Fatalf("hello после перевыпуска: %d %s", r.StatusCode, b)
	}
	mustContain(t, body(t, e.get(t, routerPath)), "проверка раз в 10 мин")
}

func TestInstallModes(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")
	routerPath, _, _ := setupRouter(t, e, "common")

	if r := e.post(t, routerPath+"/install", url.Values{"mode": {"poll"}, "interval": {"0"}}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("интервал 0: %d", r.StatusCode)
	}
	page := body(t, e.post(t, routerPath+"/install", url.Values{"mode": {"poll"}, "interval": {"10"}}))
	mustContain(t, page, "mode=poll", "interval=600", "при проверке раз в 10 мин")
	_, script := installAgent(t, e, routerPath, url.Values{"mode": {"poll"}, "interval": {"10"}})
	mustContain(t, script, "\toption mode 'poll'\n\toption interval '600'\n")
	_, script = installAgent(t, e, routerPath, url.Values{})
	mustContain(t, script, "\toption mode 'wait'\n")
	_, cfg, _ := strings.Cut(script, "cat > /etc/config/listok <<'LISTOK_CONFIG_EOF'")
	if cfg, _, _ = strings.Cut(cfg, "LISTOK_CONFIG_EOF"); strings.Contains(cfg, "interval") {
		t.Errorf("в мгновенном режиме интервал не нужен:\n%s", cfg)
	}
	// Подделанный режим в ссылке — ошибка, а не установка.
	page = body(t, e.post(t, routerPath+"/install", url.Values{}))
	tok := installTokenRe.FindStringSubmatch(page)[1]
	if r, _ := getUA(t, e, "/install/"+tok+"?mode=fast", "curl/8.12.1"); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("подделанный режим: %d", r.StatusCode)
	}
}
