package entry

import (
	"errors"
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in    string
		exact bool
		value string
		kind  Kind
		host  string
		warn  []Warning
		err   error
	}{
		// Домены
		{in: "youtube.com", value: "youtube.com", kind: KindDomain},
		{in: "  YouTube.COM. ", value: "youtube.com", kind: KindDomain},
		{in: "*.discord.gg", value: "discord.gg", kind: KindDomain},
		{in: ".anilist.co", value: "anilist.co", kind: KindDomain},
		{in: "www.youtube.com", value: "youtube.com", kind: KindDomain, host: "www.youtube.com"},
		{in: "rr3---sn-abc.googlevideo.com", value: "googlevideo.com", kind: KindDomain, host: "rr3---sn-abc.googlevideo.com"},
		{in: "www.youtube.com", exact: true, value: "www.youtube.com", kind: KindDomain},
		{in: "www.bbc.co.uk", value: "bbc.co.uk", kind: KindDomain, host: "www.bbc.co.uk"},
		// Частная часть PSL — обычные домены для маршрутизации (D-026).
		{in: "githubusercontent.com", value: "githubusercontent.com", kind: KindDomain},
		{in: "raw.githubusercontent.com", value: "githubusercontent.com", kind: KindDomain, host: "raw.githubusercontent.com"},
		{in: "raw.githubusercontent.com", exact: true, value: "raw.githubusercontent.com", kind: KindDomain},
		{in: "user.github.io", value: "github.io", kind: KindDomain, host: "user.github.io"},
		{in: "notion.site", value: "notion.site", kind: KindDomain},
		{in: "akamaized.net", value: "akamaized.net", kind: KindDomain},
		{in: "supabase.co", value: "supabase.co", kind: KindDomain},
		{in: "ondigitalocean.app", value: "ondigitalocean.app", kind: KindDomain},
		{in: "x.y.compute.amazonaws.com", value: "amazonaws.com", kind: KindDomain, host: "x.y.compute.amazonaws.com"},
		{in: "router.lan", value: "router.lan", kind: KindDomain},
		{in: "a.b.router.lan", value: "router.lan", kind: KindDomain, host: "a.b.router.lan"},
		{in: "пример.рф", value: "xn--e1afmkfd.xn--p1ai", kind: KindDomain},
		{in: "my_host.example.com", exact: true, value: "my_host.example.com", kind: KindDomain},
		{in: `"example.org"`, value: "example.org", kind: KindDomain},

		// URL
		{in: "https://www.youtube.com/watch?v=abc", value: "youtube.com", kind: KindDomain, host: "www.youtube.com"},
		{in: "youtube.com/shorts/xyz", value: "youtube.com", kind: KindDomain},
		{in: "example.org:8443", value: "example.org", kind: KindDomain},
		{in: "https://user:pass@sub.example.org:443/p", value: "example.org", kind: KindDomain, host: "sub.example.org"},
		{in: "http://1.2.3.4:8080/x", value: "1.2.3.4/32", kind: KindCIDR4},
		{in: "http://[2001:db8::1]:443/", value: "2001:db8::1/128", kind: KindCIDR6},

		// IP и подсети
		{in: "104.29.0.0/16", value: "104.29.0.0/16", kind: KindCIDR4},
		{in: "104.29.13.37/16", value: "104.29.0.0/16", kind: KindCIDR4},
		{in: "8.8.8.8", value: "8.8.8.8/32", kind: KindCIDR4},
		{in: "2a00:1450::/32", value: "2a00:1450::/32", kind: KindCIDR6},
		{in: "2a00:1450:4001::1", value: "2a00:1450:4001::1/128", kind: KindCIDR6},
		{in: "::ffff:1.2.3.4", value: "1.2.3.4/32", kind: KindCIDR4},
		{in: "::ffff:1.2.3.0/120", value: "1.2.3.0/24", kind: KindCIDR4},

		// Предупреждения
		{in: "192.168.0.1", value: "192.168.0.1/32", kind: KindCIDR4, warn: []Warning{WarnPrivate}},
		{in: "198.18.0.4", value: "198.18.0.4/32", kind: KindCIDR4, warn: []Warning{WarnFakeIP}},
		{in: "fc00::3", value: "fc00::3/128", kind: KindCIDR6, warn: []Warning{WarnFakeIP, WarnPrivate}},
		{in: "100.64.1.1", value: "100.64.1.1/32", kind: KindCIDR4, warn: []Warning{WarnPrivate}},

		// Ошибки
		{in: "", err: ErrEmpty},
		{in: "   ", err: ErrEmpty},
		{in: "com", err: ErrPublicSuffix},
		{in: "co.uk", err: ErrPublicSuffix},
		{in: "localhost", err: ErrPublicSuffix},
		{in: "app", err: ErrPublicSuffix},
		{in: "gov.uk", err: ErrPublicSuffix},
		{in: "com.br", err: ErrPublicSuffix},
		{in: "0.0.0.0/0", err: ErrTooBroad},
		{in: "10.0.0.0/7", err: ErrTooBroad},
		{in: "2000::/3", err: ErrTooBroad},
		{in: "1.2.3", err: ErrInvalid},
		{in: "exa mple.com", err: ErrInvalid},
		{in: "-bad.example.com", err: ErrInvalid},
		{in: "bad!.example.com", err: ErrInvalid},
		{in: "fe80::1%eth0", err: ErrInvalid},
		{in: "http://", err: ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			r := Parse(tc.in, Options{ExactHost: tc.exact})
			if tc.err != nil {
				if !errors.Is(r.Err, tc.err) {
					t.Fatalf("ошибка: хотели %v, получили %v (entry %+v)", tc.err, r.Err, r.Entry)
				}
				return
			}
			if r.Err != nil {
				t.Fatalf("неожиданная ошибка %v", r.Err)
			}
			if r.Entry.Value != tc.value || r.Entry.Kind != tc.kind {
				t.Errorf("entry = %+v, хотели %q %s", r.Entry, tc.value, tc.kind)
			}
			if r.Host != tc.host {
				t.Errorf("host = %q, хотели %q", r.Host, tc.host)
			}
			if !slices.Equal(r.Warnings, tc.warn) {
				t.Errorf("warnings = %v, хотели %v", r.Warnings, tc.warn)
			}
		})
	}
}

func TestParseMany(t *testing.T) {
	in := "# комментарий\nyoutube.com, www.youtube.com\n\n  8.8.8.8 1.1.1.1;nope!\n"
	got := ParseMany(in, Options{})
	type row struct {
		value string
		err   error
	}
	want := []row{
		{"youtube.com", nil},
		{"youtube.com", ErrDuplicate},
		{"8.8.8.8/32", nil},
		{"1.1.1.1/32", nil},
		{"", ErrInvalid},
	}
	if len(got) != len(want) {
		t.Fatalf("получено %d результатов, хотели %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if !errors.Is(got[i].Err, w.err) || (w.err == nil && got[i].Entry.Value != w.value) {
			t.Errorf("[%d] %q: %+v, хотели %+v", i, got[i].Input, got[i], w)
		}
	}
}
