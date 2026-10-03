# HTTP-маршруты

Четыре группы маршрутов с разной аутентификацией. NPM пускает снаружи только публичные группы (см. `deploy.md`).

## 1. Публичные, по токену в пути (без сессии)

| Метод | Путь | Что делает |
|---|---|---|
| GET | `/f/{token}.lst` | Содержимое фида. `If-None-Match` → 304. `?wait=N` включает long-poll (см. `feeds.md`). |
| GET | `/install/{token}` | sh-установщик агента. Одноразовый, живёт 24 ч. |
| GET | `/agent/listok-agent.uc` | Текущая версия агента, для самообновления. |

Неизвестный токен → `404` без подробностей. Ограничение частоты по IP.

## 2. Агент: `/agent/v1`, заголовок `Authorization: Bearer <agent_token>`

| Метод | Путь | Тело / ответ |
|---|---|---|
| POST | `/agent/v1/hello` | `{agent_version, forkop_version, singbox_version, sections:[...]}` → `{feeds:[{section, url}], report_interval_s, report_enabled}` |
| POST | `/agent/v1/report` | `{observations:[{host, ip, network, port, rule, outbound, hits, bytes}]}` → 204 |
| POST | `/agent/v1/applied` | `{section, etag, ok, error?}`: результат `forkop list_update` → 204 |

## 3. JSON API: `/api/v1`, заголовок `Authorization: Bearer <api_token>`

Для iOS Shortcuts, бота и скриптов. Права те же, что у владельца токена.

| Метод | Путь | Назначение |
|---|---|---|
| GET | `/api/v1/lists` | Видимые списки |
| GET | `/api/v1/lists/{slug}/entries` | Записи |
| POST | `/api/v1/lists/{slug}/entries` | `{input, comment?, ttl?, exact_host?}`: сырой ввод, сервер нормализует. Ответ: что добавлено, что уже покрыто. Без права на запись → создаётся предложение. |
| DELETE | `/api/v1/lists/{slug}/entries/{value}` | Удалить |
| GET | `/api/v1/check?q=...` | Где покрыт домен или IP |
| POST | `/api/v1/quick` | `{input}`: добавить в список по умолчанию пользователя (для «Поделиться») |

Ошибки в формате `{"error": "code", "message": "по-русски"}`.

## 4. Веб-интерфейс (сессия, htmx)

Страницы отдают полный HTML. При заголовке `HX-Request` отдаётся только фрагмент (`partials/`). Все изменяющие запросы идут через POST/DELETE/PATCH и защищены `http.CrossOriginProtection`.

| Путь | Экран |
|---|---|
| `GET /setup`, `POST /setup` | Создание первого админа по setup-токену из лога. Доступно, только пока пользователей нет (`deploy.md`) |
| `GET /login`, `POST /login`, `POST /logout` | Вход. Ограничение частоты: 5 неудачных попыток за 15 мин на IP+логин, дальше пауза с ростом |
| `GET /` | Главная: быстрое добавление, последние изменения, статус роутеров, входящие предложения |
| `GET /lists`, `POST /lists` | Списки |
| `POST /preview` | Живой разбор ввода (`input`, `list`, `exact`) → фрагмент `preview`. Ничего не пишет |
| `POST /quick` | Быстрое добавление с главной → итог + OOB: очистка поля, лента изменений. Запоминает список в cookie `listok_last_list` |
| `GET /lists/{slug}` | Записи, поиск, фильтры (`q`, `kind` = domain/cidr) |
| `GET /lists/{slug}/rows` | Только `<tbody id="rows">` для поиска |
| `POST /lists/{slug}/entries` | Добавить пачку → итог + OOB: таблица (в `<template>`), счётчики |
| `GET /lists/{slug}/entries/{id}/row`, `…/edit` | Строка таблицы в режиме просмотра / правки комментария |
| `PATCH /lists/{slug}/entries/{id}` | Комментарий, вкл/выкл, срок |
| `DELETE /lists/{slug}/entries/{id}` | Удалить |
| `GET /lists/{slug}/history`, `GET /lists/{slug}/history/{v}` | История и diff |
| `POST /lists/{slug}/rollback/{v}` | Откат |
| `GET/POST /lists/{slug}/members` | Права |
| `GET /proposals`, `GET /proposals/{id}` | Предложения |
| `POST /proposals`, `POST /proposals/{id}/decide` | Создать / решить (всё или по строкам) |
| `GET /routers`, `GET /routers/{id}` | Роутеры, фиды, журнал опросов |
| `POST /routers/{id}/install-token` | Выдать ссылку установки |
| `POST /feeds/{id}/regenerate` | Перевыпустить токен фида |
| `GET /suggestions` | Подсказки из clash API: добавить / скрыть / игнорировать шаблон |
| `GET /check?q=` | «Покрыт ли» |
| `GET /events` | SSE: статусы роутеров, новые предложения, применение фида |
| `GET /settings`, `/settings/tokens`, `/users` | Настройки, API-токены, пользователи (админ) |
| `GET /share?url=&text=` | Цель Web Share Target из PWA → форма быстрого добавления |

Ошибки для htmx: 401 без сессии (app.js уводит на `/login`), 403 нет прав, 404 не найдено или не видно, 422 ошибка валидации (фрагмент с сообщением вставляется). Остальные коды показывает всплывашка из `app.js`.
