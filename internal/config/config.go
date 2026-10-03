// Package config читает настройки сервиса из переменных окружения (docs/deploy.md).
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr          string
	DataDir       string
	BaseURL       string
	AdminUser     string
	AdminPassword string
	TrustedProxy  []netip.Prefix
	LongPollMax   time.Duration
	TGBotToken    string
	BackupKeep    int
}

// FromEnv собирает конфиг. getenv передаётся снаружи, чтобы тесты не трогали окружение процесса.
func FromEnv(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Addr:          get("LISTOK_ADDR", ":8080"),
		DataDir:       get("LISTOK_DATA", "/data"),
		BaseURL:       strings.TrimRight(get("LISTOK_BASE_URL", ""), "/"),
		AdminUser:     get("LISTOK_ADMIN_USER", ""),
		AdminPassword: getenv("LISTOK_ADMIN_PASSWORD"), // пароль не тримим
		TGBotToken:    get("LISTOK_TG_BOT_TOKEN", ""),
	}

	var errs []error

	if cfg.BaseURL == "" {
		errs = append(errs, errors.New("LISTOK_BASE_URL обязателен"))
	} else if u, err := url.Parse(cfg.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("LISTOK_BASE_URL: ожидается http(s)://хост, получено %q", cfg.BaseURL))
	}

	if (cfg.AdminUser == "") != (cfg.AdminPassword == "") {
		errs = append(errs, errors.New("LISTOK_ADMIN_USER и LISTOK_ADMIN_PASSWORD задаются только вместе"))
	}

	for _, s := range strings.Split(get("LISTOK_TRUSTED_PROXY", "172.16.0.0/12"), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("LISTOK_TRUSTED_PROXY: %w", err))
			continue
		}
		cfg.TrustedProxy = append(cfg.TrustedProxy, p.Masked())
	}

	longPoll, err := strconv.Atoi(get("LISTOK_LONGPOLL_MAX", "55"))
	if err != nil || longPoll < 1 || longPoll > 600 {
		errs = append(errs, errors.New("LISTOK_LONGPOLL_MAX: целое число секунд от 1 до 600"))
	}
	cfg.LongPollMax = time.Duration(longPoll) * time.Second

	cfg.BackupKeep, err = strconv.Atoi(get("LISTOK_BACKUP_KEEP", "14"))
	if err != nil || cfg.BackupKeep < 0 {
		errs = append(errs, errors.New("LISTOK_BACKUP_KEEP: неотрицательное целое"))
	}

	return cfg, errors.Join(errs...)
}
