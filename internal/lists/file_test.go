package lists

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BasteArima/listok/internal/entry"
	"github.com/BasteArima/listok/internal/store"
)

func TestSplitComment(t *testing.T) {
	cases := []struct{ line, value, comment string }{
		{"youtube.com", "youtube.com", ""},
		{"youtube.com # видео", "youtube.com", "видео"},
		{"youtube.com\t#видео", "youtube.com", "видео"},
		{"youtube.com # a # b", "youtube.com", "a # b"},
		{"https://x.example/#frag", "https://x.example/#frag", ""},
		{"https://x.example/#frag # c", "https://x.example/#frag", "c"},
		{"a.example b.example # общий", "a.example b.example", "общий"},
		{"x.example #", "x.example", ""},
		{"x.example # " + strings.Repeat("я", 250), "x.example", strings.Repeat("я", maxComment)},
	}
	for _, c := range cases {
		v, cm := splitComment(c.line)
		if v != c.value || cm != c.comment {
			t.Errorf("splitComment(%q) = %q, %q; ожидали %q, %q", c.line, v, cm, c.value, c.comment)
		}
	}
}

func TestParseFile(t *testing.T) {
	text := "\uFEFF# listok: Общий\n# описание\n\n" +
		"music.youtube.com # только музыка\r\n" +
		"discord.gg, discord.com # оба\n" +
		"104.29.13.37/16\n" +
		"MUSIC.youtube.com\n" +
		"com\n" +
		"https://www.reddit.com/r/x # ссылка\n"
	items := parseFile(text)
	type want struct {
		value, comment string
		err            error
	}
	exp := []want{
		{"music.youtube.com", "только музыка", nil}, // как есть, без сокращения до youtube.com
		{"discord.gg", "оба", nil},
		{"discord.com", "оба", nil},
		{"104.29.0.0/16", "", nil},
		{"", "", entry.ErrDuplicate},
		{"", "", entry.ErrPublicSuffix},
		{"reddit.com", "ссылка", nil}, // ссылка сокращается, как при обычном добавлении
	}
	if len(items) != len(exp) {
		t.Fatalf("разобрано %d записей, ожидали %d: %+v", len(items), len(exp), items)
	}
	for i, w := range exp {
		it := items[i]
		if w.err != nil {
			if !errors.Is(it.Err, w.err) {
				t.Errorf("%d: ошибка %v, ожидали %v", i, it.Err, w.err)
			}
			continue
		}
		if it.Err != nil || it.Entry.Value != w.value || it.Comment != w.comment {
			t.Errorf("%d: %q %q %v; ожидали %q %q", i, it.Entry.Value, it.Comment, it.Err, w.value, w.comment)
		}
	}
}

func TestFormatFileRoundTrip(t *testing.T) {
	l := store.List{Slug: "common", Title: "Общий", Description: "для\nвсех", Version: 7}
	es := []store.Entry{
		{Value: "2001:db8::/32", Kind: "cidr6"},
		{Value: "104.29.0.0/16", Kind: "cidr4", Comment: "cloudflare"},
		{Value: "9.0.0.0/8", Kind: "cidr4"},
		{Value: "youtube.com", Kind: "domain", Comment: "видео #1"},
		{Value: "discord.gg", Kind: "domain", Comment: "две\nстроки"},
	}
	sortForFile(es)
	got := string(formatFile(l, es, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)))
	want := "# listok: Общий\n" +
		"# для всех\n" +
		"# адрес common, версия 7, выгружено 2026-10-31 12:00, записей: 5\n" +
		"discord.gg # две строки\n" +
		"youtube.com # видео #1\n" +
		"9.0.0.0/8\n" +
		"104.29.0.0/16 # cloudflare\n" +
		"2001:db8::/32\n"
	if got != want {
		t.Fatalf("файл:\n%s\nожидали:\n%s", got, want)
	}

	items := parseFile(got)
	if len(items) != len(es) {
		t.Fatalf("после разбора %d записей, ожидали %d", len(items), len(es))
	}
	for i, it := range items {
		if it.Err != nil || it.Entry.Value != es[i].Value || it.Comment != oneLine(es[i].Comment) {
			t.Errorf("%d: %q %q %v, ожидали %q %q", i, it.Entry.Value, it.Comment, it.Err, es[i].Value, es[i].Comment)
		}
	}
}

func TestImportMessage(t *testing.T) {
	cases := map[string]string{
		"":                      "импорт из файла",
		"common.txt":            "импорт из файла common.txt",
		`C:\Users\x\common.txt`: "импорт из файла common.txt",
		"/tmp/a b\n.txt":        "импорт из файла a b .txt",
		strings.Repeat("я", 70): "импорт из файла " + strings.Repeat("я", 59) + "…",
	}
	for in, want := range cases {
		if got := importMessage(in); got != want {
			t.Errorf("importMessage(%q) = %q, ожидали %q", in, got, want)
		}
	}
}
