package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{"LISTOK_BASE_URL": "https://listok.example.org/"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.DataDir != "/data" || cfg.BackupKeep != 14 {
		t.Errorf("неожиданные значения по умолчанию: %+v", cfg)
	}
	if cfg.BaseURL != "https://listok.example.org" {
		t.Errorf("BaseURL должен быть без слэша в конце, получено %q", cfg.BaseURL)
	}
	if cfg.LongPollMax != 55*time.Second {
		t.Errorf("LongPollMax = %v", cfg.LongPollMax)
	}
	if len(cfg.TrustedProxy) != 1 || cfg.TrustedProxy[0].String() != "172.16.0.0/12" {
		t.Errorf("TrustedProxy = %v", cfg.TrustedProxy)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"нет base url", map[string]string{}, "LISTOK_BASE_URL обязателен"},
		{"кривой base url", map[string]string{"LISTOK_BASE_URL": "listok.example.org"}, "ожидается http(s)"},
		{"админ без пароля", map[string]string{"LISTOK_BASE_URL": "https://x.org", "LISTOK_ADMIN_USER": "a"}, "только вместе"},
		{"кривой прокси", map[string]string{"LISTOK_BASE_URL": "https://x.org", "LISTOK_TRUSTED_PROXY": "nope"}, "LISTOK_TRUSTED_PROXY"},
		{"long-poll 0", map[string]string{"LISTOK_BASE_URL": "https://x.org", "LISTOK_LONGPOLL_MAX": "0"}, "LISTOK_LONGPOLL_MAX"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FromEnv(env(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ожидалась ошибка с %q, получено %v", tc.want, err)
			}
		})
	}
}
