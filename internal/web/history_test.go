package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestHistoryAndRollback(t *testing.T) {
	e := newEnv(t, "admin", "long-enough-pass")
	e.login(t, "admin", "long-enough-pass")
	expectRedirect(t, e.post(t, "/lists", url.Values{"title": {"Общий"}, "slug": {"common"}}), "/lists/common")

	// v1: +a +b; v2: комментарий b; v3: выкл b; v4: −a; v5: +c
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"a.example b.example"}})
	ids := map[string]string{}
	for _, m := range regexp.MustCompile(`id="entry-(\d+)"[^>]*>\s*<td class="sel">.*?</td>\s*<td class="mono value">([^<]+)<`).FindAllStringSubmatch(body(t, e.get(t, "/lists/common/rows")), -1) {
		ids[m[2]] = m[1]
	}
	e.do(t, "PATCH", "/lists/common/entries/"+ids["b.example"], url.Values{"comment": {"бэ"}})
	e.do(t, "PATCH", "/lists/common/entries/"+ids["b.example"], url.Values{"enabled": {"0"}})
	e.do(t, "DELETE", "/lists/common/entries/"+ids["a.example"], nil)
	e.do(t, "POST", "/lists/common/entries", url.Values{"input": {"c.example"}})

	page := body(t, e.get(t, "/lists/common/history"))
	mustContain(t, page, `id="v5"`, `id="v1"`, "откатить к этой", `hx-post="/lists/common/rollback/0"`)
	if strings.Contains(page, `hx-post="/lists/common/rollback/5"`) {
		t.Error("к текущей версии откатываться нельзя")
	}

	// Изменения версий.
	mustContain(t, body(t, e.get(t, "/lists/common/history/1")), "a.example", "b.example", `class="plus"`)
	mustContain(t, body(t, e.get(t, "/lists/common/history/2")), "комментарий «» → «бэ»")
	mustContain(t, body(t, e.get(t, "/lists/common/history/3")), "выключена")
	mustContain(t, body(t, e.get(t, "/lists/common/history/4")), "a.example", `class="minus"`)
	if r := e.get(t, "/lists/common/history/99"); r.StatusCode != http.StatusNotFound {
		t.Errorf("несуществующая версия: %d", r.StatusCode)
	}

	// Откат к v1: a и b включены, без комментария, c нет. Это новая версия v6, через htmx — HX-Redirect.
	r := e.do(t, "POST", "/lists/common/rollback/1", nil)
	if r.StatusCode != http.StatusNoContent || r.Header.Get("HX-Redirect") != "/lists/common/history" {
		t.Fatalf("откат: %d %q", r.StatusCode, r.Header.Get("HX-Redirect"))
	}
	rows := body(t, e.get(t, "/lists/common/rows"))
	mustContain(t, rows, "a.example", "b.example")
	if strings.Contains(rows, "c.example") || strings.Contains(rows, `class="off"`) || strings.Contains(rows, "бэ") {
		t.Fatalf("состояние после отката неверное:\n%s", rows)
	}
	page = body(t, e.get(t, "/lists/common/history"))
	mustContain(t, page, `id="v6"`, "откат к версии 1", "откат")
	ch := body(t, e.get(t, "/lists/common/history/6"))
	mustContain(t, ch, "c.example", "a.example", "включена")

	// Индекс покрытия обновился: b.example снова покрывает поддомен, c.example — нет.
	pv := body(t, e.do(t, "POST", "/preview", url.Values{"input": {"x.b.example x.c.example"}, "exact": {"1"}}))
	if strings.Count(pv, "покрыто:") != 1 || !strings.Contains(pv, "покрыто: <span class=\"mono\">b.example") {
		t.Fatalf("индекс после отката:\n%s", pv)
	}

	// Откат отката: к v5 возвращает c и выключенный b с комментарием.
	e.do(t, "POST", "/lists/common/rollback/5", nil)
	rows = body(t, e.get(t, "/lists/common/rows"))
	mustContain(t, rows, "c.example", `class="off"`, "бэ")
	if strings.Contains(rows, "a.example") {
		t.Fatal("a.example должна снова исчезнуть")
	}

	// Откат к v0 очищает список; откат к текущему состоянию не создаёт пустую версию.
	e.do(t, "POST", "/lists/common/rollback/0", nil)
	if !strings.Contains(body(t, e.get(t, "/lists/common/rows")), "Записей пока нет") {
		t.Fatal("откат к v0 должен очистить список")
	}
	e.do(t, "POST", "/lists/common/rollback/8", nil) // v8 — это и есть текущее (пустое) состояние
	if strings.Contains(body(t, e.get(t, "/lists/common/history")), `id="v9"`) {
		t.Error("откат без изменений не должен создавать версию")
	}
	if r := e.do(t, "POST", "/lists/common/rollback/100", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("откат к будущей версии: %d", r.StatusCode)
	}
}
