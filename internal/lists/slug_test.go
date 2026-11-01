package lists

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Общий":                 "obshchii",
		"Общий список":          "obshchii-spisok",
		"YouTube и Discord":     "youtube-i-discord",
		"  гео — блок!! ":       "geo-blok",
		"Щука Ёжик Юла Яхта":    "shchuka-ezhik-iula-iakhta",
		"подъезд":               "podezd",
		"!!!":                   "list",
		"😀":                     "list",
		strings.Repeat("а", 60): strings.Repeat("a", maxSlug),
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, ожидали %q", in, got, want)
		}
		if got := slugify(in); !slugRe.MatchString(got) {
			t.Errorf("slugify(%q) = %q не проходит проверку адреса", in, got)
		}
	}
}

func TestWithSuffix(t *testing.T) {
	if got := withSuffix("obshchii", 2); got != "obshchii-2" {
		t.Errorf("withSuffix = %q", got)
	}
	long := strings.Repeat("a", maxSlug)
	if got := withSuffix(long, 12); len(got) != maxSlug || !strings.HasSuffix(got, "-12") || !slugRe.MatchString(got) {
		t.Errorf("withSuffix длинного = %q", got)
	}
}
