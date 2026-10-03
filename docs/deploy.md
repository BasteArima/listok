# Развёртывание

## Где

homesrv (OMV + Portainer), отдельный стек. Наружу через Nginx Proxy Manager на `listok.baste.ru`. Весь интерфейс открыт наружу и защищён паролем (D-012).

## Образ

Multi-stage: `golang:1.25` → `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"` → `gcr.io/distroless/static:nonroot`. Порт 8080, том `/data`.

## Переменные окружения

| Переменная | По умолчанию | Смысл |
|---|---|---|
| `LISTOK_ADDR` | `:8080` | адрес прослушивания |
| `LISTOK_DATA` | `/data` | БД `listok.db`, бэкапы |
| `LISTOK_BASE_URL` | — (обязательно) | `https://listok.baste.ru`, нужен для ссылок фидов и установщика |
| `LISTOK_ADMIN_USER` / `LISTOK_ADMIN_PASSWORD` | — | создать админа при первом запуске, если пользователей нет. Если не заданы — мастер `/setup` (см. ниже) |
| `LISTOK_TRUSTED_PROXY` | `172.16.0.0/12` | откуда доверять `X-Forwarded-For` (NPM) |
| `LISTOK_LONGPOLL_MAX` | `55` | верхний предел `?wait=` в секундах |
| `LISTOK_TG_BOT_TOKEN` | — | уведомления, необязательно |
| `LISTOK_BACKUP_KEEP` | `14` | сколько ночных бэкапов хранить |

## NPM

- Proxy host `listok.baste.ru` → `homesrv:<порт>`, SSL Let's Encrypt, Websockets не нужны (SSE работает поверх обычного HTTP).
- Для long-poll и SSE во вкладке Advanced: `proxy_read_timeout 120s; proxy_buffering off;`. После этого `LISTOK_LONGPOLL_MAX` можно поднять до ~110.
- Access list NPM не используется: добавлять записи нужно с телефона откуда угодно (D-012).

## Первый запуск

1. Если заданы `LISTOK_ADMIN_USER` и `LISTOK_ADMIN_PASSWORD`, а пользователей в БД нет, админ создаётся из env. После этого переменные можно убрать из стека.
2. Иначе сервис при старте печатает в лог одноразовый **setup-токен**, и все страницы перенаправляют на `/setup`. На этой странице вводятся токен из `docker logs`, логин и пароль. После создания админа `/setup` отключается навсегда. Без токена занять админку раньше владельца нельзя, хотя сервис уже открыт наружу.

## Бэкапы

Ночью `VACUUM INTO /data/backups/listok-YYYYMMDD.db`, хранится `LISTOK_BACKUP_KEEP` штук. Сам `/data` можно включить в общий бэкап homesrv.
