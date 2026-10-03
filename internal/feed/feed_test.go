package feed

import (
	"regexp"
	"testing"

	"github.com/BasteArima/listok/internal/entry"
)

func TestRender(t *testing.T) {
	c := Render([]entry.Entry{{Value: "youtube.com", Kind: entry.KindDomain}, {Value: "104.29.0.0/16", Kind: entry.KindCIDR4}})
	want := "listok-sentinel.invalid\nyoutube.com\n104.29.0.0/16\n"
	if string(c.Body) != want {
		t.Fatalf("тело:\n%q\nхотели\n%q", c.Body, want)
	}
	if c.Entries != 2 || !regexp.MustCompile(`^"[0-9a-f]{16}"$`).MatchString(c.ETag) {
		t.Fatalf("entries=%d etag=%s", c.Entries, c.ETag)
	}
	empty := Render(nil)
	if string(empty.Body) != Sentinel+"\n" || empty.Entries != 0 {
		t.Fatalf("пустой фид: %q", empty.Body)
	}
	if empty.ETag == c.ETag {
		t.Fatal("разное содержимое — разный ETag")
	}
	if again := Render([]entry.Entry{{Value: Sentinel, Kind: entry.KindDomain}}); again.Entries != 0 {
		t.Fatal("сторожевая запись не должна дублироваться")
	}
}

func TestNewToken(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-Za-z]{43}$`)
	seen := map[string]bool{}
	for range 100 {
		tok := NewToken()
		if !re.MatchString(tok) {
			t.Fatalf("токен %q не base62/43", tok)
		}
		if seen[tok] {
			t.Fatal("повтор токена")
		}
		seen[tok] = true
	}
}
