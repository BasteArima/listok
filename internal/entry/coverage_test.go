package entry

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"slices"
	"testing"
)

func d(v string) Entry { return Entry{v, KindDomain} }
func c(v string) Entry { return prefixEntry(netip.MustParsePrefix(v)) }

func values(refs []Ref) []string {
	var out []string
	for _, r := range refs {
		out = append(out, fmt.Sprintf("%d:%s", r.ListID, r.Entry.Value))
	}
	return out
}

func TestIndexCoverage(t *testing.T) {
	ix := NewIndex()
	ix.Add(1, d("youtube.com"))
	ix.Add(2, d("m.youtube.com"))
	ix.Add(2, d("discord.gg"))
	ix.Add(1, c("104.16.0.0/12"))
	ix.Add(2, c("104.29.0.0/16"))
	ix.Add(1, c("2a00:1450::/32"))

	cases := []struct {
		name string
		got  []Ref
		want []string
	}{
		{"поддомен покрыт", ix.CoveredBy(d("a.m.youtube.com")), []string{"1:youtube.com", "2:m.youtube.com"}},
		{"точное совпадение", ix.CoveredBy(d("discord.gg")), []string{"2:discord.gg"}},
		{"суффикс не по границе метки", ix.CoveredBy(d("notyoutube.com")), nil},
		{"не покрыт", ix.CoveredBy(d("example.org")), nil},
		{"IP в подсетях", ix.CoveredBy(c("104.29.1.1/32")), []string{"1:104.16.0.0/12", "2:104.29.0.0/16"}},
		{"подсеть шире существующей не покрыта", ix.CoveredBy(c("104.0.0.0/8")), nil},
		{"IPv6", ix.CoveredBy(c("2a00:1450:4001::/48")), []string{"1:2a00:1450::/32"}},
		{"IPv4 не матчит IPv6", ix.CoveredBy(c("42.0.0.0/8")), nil},
		{"домен покрывает поддомены", ix.Covers(d("youtube.com")), []string{"2:m.youtube.com"}},
		{"подсеть покрывает узкие", ix.Covers(c("104.0.0.0/8")), []string{"1:104.16.0.0/12", "2:104.29.0.0/16"}},
		{"сама себя не покрывает строго", ix.Covers(c("104.29.0.0/16")), nil},
	}
	for _, tc := range cases {
		if g := values(tc.got); !slices.Equal(g, tc.want) {
			t.Errorf("%s: %v, хотели %v", tc.name, g, tc.want)
		}
	}

	ix.Remove(1, d("youtube.com"))
	if g := values(ix.CoveredBy(d("www.youtube.com"))); g != nil {
		t.Errorf("после удаления ничего не должно покрывать, получено %v", g)
	}
	ix.Remove(2, c("104.29.0.0/16"))
	if g := values(ix.CoveredBy(c("104.29.1.1/32"))); !slices.Equal(g, []string{"1:104.16.0.0/12"}) {
		t.Errorf("после удаления подсети: %v", g)
	}
}

func TestIndexRemoveList(t *testing.T) {
	ix := NewIndex()
	ix.Add(1, d("youtube.com"))
	ix.Add(2, d("youtube.com"))
	ix.Add(1, c("104.29.0.0/16"))
	ix.Add(2, d("discord.gg"))

	ix.RemoveList(1)

	if got := values(ix.CoveredBy(d("youtube.com"))); !slices.Equal(got, []string{"2:youtube.com"}) {
		t.Errorf("общий домен: %v, ожидали только список 2", got)
	}
	if got := ix.CoveredBy(c("104.29.1.0/24")); len(got) != 0 {
		t.Errorf("подсеть удалённого списка осталась: %v", values(got))
	}
	if got := values(ix.CoveredBy(d("discord.gg"))); !slices.Equal(got, []string{"2:discord.gg"}) {
		t.Errorf("чужой список задет: %v", got)
	}
	if len(ix.domains) != 2 || len(ix.prefixes) != 0 {
		t.Errorf("пустые ключи не убраны: доменов %d, подсетей %d", len(ix.domains), len(ix.prefixes))
	}
}

func TestCompact(t *testing.T) {
	in := []Entry{
		d("youtube.com"), d("m.youtube.com"), d("discord.gg"), d("youtube.com"), d("gg.example"),
		c("10.1.0.0/16"), c("10.0.0.0/8"), c("10.2.3.0/24"), c("11.0.0.0/8"), c("9.9.9.9/32"),
		c("2a00::/16"), c("2a00:1450::/32"), c("2001:db8::/32"),
	}
	got := Compact(in)
	var g []string
	for _, e := range got {
		g = append(g, e.Value)
	}
	want := []string{
		"discord.gg", "gg.example", "youtube.com",
		"9.9.9.9/32", "10.0.0.0/8", "11.0.0.0/8",
		"2001:db8::/32", "2a00::/16",
	}
	if !slices.Equal(g, want) {
		t.Errorf("Compact:\n получили %v\n хотели   %v", g, want)
	}
}

// Сверка быстрого Compact с наивным O(n²) на случайных подсетях.
func TestCompactMatchesNaive(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for iter := range 200 {
		var in []Entry
		for range 60 {
			a := netip.AddrFrom4([4]byte{10, byte(r.IntN(4)), byte(r.IntN(256)), byte(r.IntN(256))})
			in = append(in, prefixEntry(netip.PrefixFrom(a, 8+r.IntN(25)).Masked()))
		}
		var fast []string
		for _, e := range Compact(in) {
			fast = append(fast, e.Value)
		}
		naive := map[string]bool{}
		for _, e := range in {
			p, _ := e.Prefix()
			covered := false
			for _, o := range in {
				q, _ := o.Prefix()
				if q != p && q.Bits() <= p.Bits() && q.Contains(p.Addr()) {
					covered = true
					break
				}
			}
			if !covered {
				naive[e.Value] = true
			}
		}
		if len(fast) != len(naive) {
			t.Fatalf("итерация %d: fast=%d naive=%d", iter, len(fast), len(naive))
		}
		for _, v := range fast {
			if !naive[v] {
				t.Fatalf("итерация %d: %s нет в наивном результате", iter, v)
			}
		}
	}
}
