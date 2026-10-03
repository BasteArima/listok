# Структура проекта

```
listok/
├── AGENTS.md, CLAUDE.md        # точка входа для агентов
├── docs/                        # документация (карта в AGENTS.md)
├── cmd/listok/main.go           # запуск: конфиг, БД, миграции, роутер, фоновые задачи
├── internal/
│   ├── config/                  # чтение env (см. deploy.md)
│   ├── db/
│   │   ├── migrations/*.sql     # 0001_init.sql, ... (embed)
│   │   ├── migrate.go           # свой простой раннер, таблица schema_migrations
│   │   └── db.go                # открытие SQLite, PRAGMA, WAL
│   ├── store/                   # доступ к данным: по файлу на агрегат (lists.go, entries.go, ...)
│   ├── entry/                   # нормализация и классификация ввода, проверка покрытия
│   ├── lists/                   # сервис списков: права (CanEdit), предпросмотр, добавление, правка; держит entry.Index
│   ├── feed/                    # сборка фида (Builder, кеш по версиям списков), Render со сторожевой записью, токены; позже Notifier
│   ├── history/                 # чистые функции: восстановить состояние на версию (Reconstruct), план отката (Diff)
│   ├── proposal/                # предложения и их принятие
│   ├── routers/                 # роутеры и фиды: права, состав, отдача по токену (Serve), статус; позже отчёты агента
│   ├── remote/                  # загрузка внешних списков
│   ├── auth/                    # пароли (argon2id), токены, сессии, /setup, лимитер входа; позже API-токены и права
│   ├── jobs/                    # фоновые задачи (expirer, retention, backup, remote)
│   ├── web/                     # htmx-обработчики, шаблоны, статика
│   │   ├── handlers_*.go
│   │   ├── templates/           # layout.html, pages/*.html, partials/*.html
│   │   └── static/              # htmx.min.js (2.0.11), app.js, app.css, favicon.svg; позже manifest.webmanifest, sw.js
│   └── api/                     # JSON API /api/v1 и /agent/v1, отдача фидов /f/
├── agent/
│   ├── listok-agent.uc          # агент на роутере (ucode)
│   ├── listok-agent.init        # procd init-скрипт
│   └── install.sh.tmpl          # шаблон установщика, отдаётся по /install/<token>
└── deploy/
    ├── Dockerfile               # multi-stage, итог: distroless/static
    └── compose.yml              # стек Portainer на homesrv
```

## Правила слоёв

- `web` и `api` → сервисы (`entry`, `lists`, `feed`, `history`, `routers`, `proposal`) → `store` → `db`. Обратных зависимостей нет.
- SQL живёт только в `store`. Обработчики SQL не пишут.
- Проверка прав (`auth.Can(user, list, action)`) выполняется в сервисном слое, а не в шаблонах. Шаблоны только прячут кнопки.
- `entry` — чистые функции без БД, покрываются табличными тестами.

## Зависимости (минимум)

- `modernc.org/sqlite` — SQLite без CGO.
- `golang.org/x/crypto/argon2` — хеши паролей.
- `golang.org/x/net/idna`, `golang.org/x/net/publicsuffix` — IDN и регистрируемый домен (eTLD+1).
- Роутинг: stdlib `http.ServeMux` с шаблонами путей Go 1.22+. Защита от CSRF: `http.CrossOriginProtection` (Go 1.25+).
- Фронт без сборки: `htmx` + расширение `sse`, `alpine.js` — файлы лежат в `static/`, без CDN.
