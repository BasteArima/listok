# Развёртывание

## Где и как

домашний сервер (Portainer, x86_64, Docker 29), отдельный стек `listok`. Наружу через Nginx Proxy Manager на `listok.example.com`. Интерфейс открыт наружу и защищён паролем (D-012).

- **Образ**: `ghcr.io/bastearima/listok:latest` и `:sha-<7 символов>`. Его собирает GitHub Actions (`.github/workflows/image.yml`) на каждый push в `main`, после `go vet` и тестов. На сервере образ не собирается.
- **Dockerfile**: `deploy/Dockerfile`. Сборка `golang:1.27-alpine` → `gcr.io/distroless/static-debian12:nonroot` (без шелла, uid 65532). Порт 8080, том `/data`, healthcheck — сам бинарник (`/listok healthcheck`).
- **Стек**: `deploy/compose.yml`.
  - `network_mode: bridge`: на домашнем сервере исчерпан пул адресов Docker, своя сеть у стека не создастся.
  - Порт публикуется только на `172.17.0.1:8097`. Туда ходит NPM, а напрямую по http из LAN сервис недоступен. Порт 8096 занят Jellyfin.

## Переменные окружения

| Переменная | По умолчанию | Смысл |
|---|---|---|
| `LISTOK_ADDR` | `:8080` | адрес прослушивания |
| `LISTOK_DATA` | `/data` | БД `listok.db` и `backups/` |
| `LISTOK_BASE_URL` | — (обязательно) | `https://listok.example.com`, нужен для ссылок фидов и установщика |
| `LISTOK_ADMIN_USER` / `LISTOK_ADMIN_PASSWORD` | — | создать админа при первом запуске, если пользователей нет. Если не заданы — мастер `/setup` (см. ниже) |
| `LISTOK_TRUSTED_PROXY` | `172.16.0.0/12` | откуда доверять `X-Forwarded-For` (NPM) |
| `LISTOK_LONGPOLL_MAX` | `55` | верхний предел ожидания long-poll в секундах (`?wait=` у `/f/`, `wait` у `/agent/v1/wait`) |
| `LISTOK_TG_BOT_TOKEN` | — | уведомления (этап 5, ещё не сделано; пока не используется) |
| `LISTOK_BACKUP_KEEP` | `14` | сколько ежедневных бэкапов хранить; `0` — бэкапы выключены |
| `TZ` | UTC | часовой пояс: дата в имени бэкапа и время в логах |

## Первый запуск

1. **Образ.** Дождаться зелёного workflow `image` в GitHub после push. Проверить, что пакет `listok` в GHCR публичный: GitHub → профиль → Packages → listok → Package settings → Change visibility. Если пакет приватный, Portainer не скачает образ без авторизации.
2. **Стек.** Portainer → Stacks → Add stack `listok` → вставить `deploy/compose.yml` → Deploy.
3. **Админ.** Если `LISTOK_ADMIN_*` не заданы, сервис при старте печатает в лог одноразовый **setup-токен**: `docker logs listok | grep setup_token`. Все страницы ведут на `/setup`, там вводятся токен, логин и пароль. После создания админа `/setup` отключается навсегда. Токен живёт до рестарта контейнера, перебор ограничен.
4. **DNS.** A-запись `listok.example.com` → внешний IP дома.
5. **NPM.** Proxy host:
   - Domain `listok.example.com` → `http` `172.17.0.1` `8097`.
   - SSL: Let's Encrypt, Force SSL, HTTP/2.
   - Websockets не нужны.
   - Advanced (long-poll работает и без этого, пока `LISTOK_LONGPOLL_MAX` не больше 55):
     ```
     proxy_read_timeout 120s;
     proxy_buffering off;
     ```
     После этого `LISTOK_LONGPOLL_MAX` можно поднять до ~110.
   - Access list не ставим: добавлять записи нужно с телефона откуда угодно (D-012).
6. **Изнутри домашней сети.** Если домен из LAN не открывается (роутер не делает hairpin NAT), добавить на роутере подмену DNS домена → LAN-адрес сервера. Сначала проверить, нужно ли это вообще.
7. **Проверка.**
   - `curl -s https://listok.example.com/healthz` → `ok`;
   - в логе контейнера строка `listok запущен version=<sha>`;
   - `docker inspect --format '{{.State.Health.Status}}' listok` → `healthy`.

## Подключение роутера

Обычно — через агент: Роутеры → новый роутер → готовая команда установки на странице роутера, затем «Подключить секцию» и списки (`router-agent.md`).

Без агента: на странице роутера «Подключить секцию вручную» (название как в колонке «Name» в LuCI) → отметить списки → «Ссылки для forkop без агента» → «Копировать» → LuCI → forkop → секция → **Наборы правил** с флажком «Учитывать IP-адреса и подсети» (в forkop 1.x — **Domain and IP lists**) → «+» → Save & Apply. forkop перезапустится один раз.

Ограничения без агента:
- forkop перекачивает список по своему `update_interval` (1d): новые записи доезжают к ночи или после ручного обновления (в forkop 1.x — `forkop list_update`).
- forkop 1.x: после перезагрузки роутера список по URL может не подхватиться до повторного рестарта forkop (`forkop-integration.md`, гонка).

## Обновление

Push в `main` → workflow собирает `:latest`. Дальше один из вариантов:
- Portainer → стек `listok` → **Pull and redeploy**;
- автоматически: таймер на сервере тянет `:latest` и при смене образа передеплоивает стек через Portainer API. Пока не настроено.

Откат: в стеке заменить `:latest` на `:sha-<коммит>` и передеплоить. Миграции БД идут только вперёд: откат на версию старше последней миграции не поддерживается, поэтому перед таким обновлением нужен бэкап.

## Бэкапы

- **Когда**: раз в час фоновая задача проверяет, есть ли бэкап за сегодня. Если нет, делает `VACUUM INTO /data/backups/listok-YYYYMMDD.db` через временный файл, так что битый «сегодняшний» бэкап не появится. Хранится `LISTOK_BACKUP_KEEP` последних.
- **Где на хосте**: том `listok-data` → `docker volume inspect listok_listok-data` → `Mountpoint`. Его можно включить в общий бэкап домашний сервер.
- **Восстановление**:
  1. Остановить стек.
  2. Подменить `/data/listok.db` нужным бэкапом и удалить `listok.db-wal` и `listok.db-shm`.
  3. Запустить стек.
