// Package entry разбирает пользовательский ввод в записи списков и проверяет покрытие.
// Чистые функции без БД. Правила описаны в docs/normalization.md.
package entry

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

type Kind string

const (
	KindDomain Kind = "domain"
	KindCIDR4  Kind = "cidr4"
	KindCIDR6  Kind = "cidr6"
)

// Entry — нормализованная запись: домен ("youtube.com") или подсеть ("104.29.0.0/16").
type Entry struct {
	Value string
	Kind  Kind
}

func (e Entry) IsDomain() bool { return e.Kind == KindDomain }

// Prefix возвращает подсеть записи. ok=false для доменов.
func (e Entry) Prefix() (p netip.Prefix, ok bool) {
	if e.IsDomain() {
		return netip.Prefix{}, false
	}
	p, err := netip.ParsePrefix(e.Value)
	return p, err == nil
}

var (
	ErrEmpty        = errors.New("пустая строка")
	ErrInvalid      = errors.New("не похоже ни на домен, ни на IP/подсеть")
	ErrPublicSuffix = errors.New("это публичный суффикс целиком (как com или co.uk)")
	ErrTooBroad     = errors.New("слишком широкая подсеть (IPv4 короче /8, IPv6 короче /16)")
	ErrDuplicate    = errors.New("повтор во вводе")
)

type Warning string

const (
	WarnPrivate Warning = "private" // частный или служебный диапазон: через туннель смысла нет
	WarnFakeIP  Warning = "fakeip"  // пересекается с FakeIP-диапазоном sing-box
)

type Options struct {
	// ExactHost: не сокращать хост до регистрируемого домена (eTLD+1).
	ExactHost bool
}

// Result — разбор одного токена ввода.
type Result struct {
	Input    string
	Entry    Entry  // заполнено, если Err == nil
	Host     string // исходный хост, если Entry сокращён до eTLD+1
	Warnings []Warning
	Err      error
}

// Parse разбирает один токен: подсеть, IP, URL или домен.
func Parse(raw string, opt Options) Result {
	res := Result{Input: raw}
	s := strings.Trim(strings.TrimSpace(raw), `"'<>`)
	if s == "" {
		res.Err = ErrEmpty
		return res
	}

	if p, err := netip.ParsePrefix(s); err == nil {
		return fromPrefix(res, p)
	}
	if a, err := netip.ParseAddr(s); err == nil {
		if a.Zone() != "" {
			res.Err = ErrInvalid
			return res
		}
		return fromPrefix(res, netip.PrefixFrom(a, a.BitLen()))
	}

	if strings.ContainsAny(s, "/:?#@") {
		host := hostFromURL(s)
		if host == "" {
			res.Err = ErrInvalid
			return res
		}
		if a, err := netip.ParseAddr(host); err == nil && a.Zone() == "" {
			return fromPrefix(res, netip.PrefixFrom(a, a.BitLen()))
		}
		s = host
	}

	return fromDomain(res, s, opt)
}

// ParseMany разбирает пачку: разделители — перевод строки, пробелы, запятые.
// Строки, начинающиеся с #, пропускаются. Повторы помечаются ErrDuplicate.
func ParseMany(text string, opt Options) []Result {
	var out []Result
	seen := map[string]bool{}
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t'
		}) {
			r := Parse(tok, opt)
			if r.Err == nil {
				if seen[r.Entry.Value] {
					r.Err = ErrDuplicate
				}
				seen[r.Entry.Value] = true
			}
			out = append(out, r)
		}
	}
	return out
}

func hostFromURL(s string) string {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func fromPrefix(res Result, p netip.Prefix) Result {
	addr, bits := p.Addr(), p.Bits()
	if addr.Is4In6() {
		if bits < 96 {
			res.Err = ErrInvalid
			return res
		}
		addr, bits = addr.Unmap(), bits-96
	}
	p = netip.PrefixFrom(addr, bits).Masked()

	if (p.Addr().Is4() && bits < 8) || (p.Addr().Is6() && bits < 16) {
		res.Err = ErrTooBroad
		return res
	}

	res.Entry = Entry{Value: p.String(), Kind: KindCIDR6}
	if p.Addr().Is4() {
		res.Entry.Kind = KindCIDR4
	}
	res.Warnings = prefixWarnings(p)
	return res
}

var (
	fakeIPRanges = []netip.Prefix{
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("fc00::/18"),
	}
	specialRanges = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("ff00::/8"),
	}
)

func prefixWarnings(p netip.Prefix) []Warning {
	var w []Warning
	for _, r := range fakeIPRanges {
		if p.Overlaps(r) {
			w = append(w, WarnFakeIP)
			break
		}
	}
	for _, r := range specialRanges {
		if p.Overlaps(r) {
			w = append(w, WarnPrivate)
			break
		}
	}
	return w
}

// Нестрогий профиль: допускает "_" в метках, остальное проверяем сами.
var idnaProfile = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.StrictDomainName(false))

func fromDomain(res Result, s string, opt Options) Result {
	d := strings.ToLower(s)
	d = strings.TrimSuffix(d, ".")
	d = strings.TrimPrefix(d, "*.")
	d = strings.TrimLeft(d, ".")

	ascii, err := idnaProfile.ToASCII(d)
	if err != nil || !validDomain(ascii) {
		res.Err = ErrInvalid
		return res
	}
	if ps, _ := publicsuffix.PublicSuffix(ascii); ps == ascii {
		res.Err = ErrPublicSuffix
		return res
	}

	value := ascii
	if !opt.ExactHost {
		if etld1, err := publicsuffix.EffectiveTLDPlusOne(ascii); err == nil && etld1 != ascii {
			value = etld1
			res.Host = ascii
		}
	}
	res.Entry = Entry{Value: value, Kind: KindDomain}
	return res
}

func validDomain(d string) bool {
	if d == "" || len(d) > 253 {
		return false
	}
	labels := strings.Split(d, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	// "1.2.3" — не домен и не IP: TLD из одних цифр не бывает.
	tld := labels[len(labels)-1]
	return strings.Trim(tld, "0123456789") != ""
}
