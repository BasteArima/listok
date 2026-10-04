# HTTP-маршруты

Четыре группы маршрутов с разной аутентификацией. NPM пускает снаружи только публичные группы (см. `deploy.md`).

## 1. Публичные, по токену в пути (без сессии)

| Метод | Путь | Что делает |
|---|---|---|
| GET | `/f/{token}.lst` | Содержимое фида, `text/plain`. `ETag` + `If-None-Match` → 304. `?wait=N` — long-poll: при совпавшей версии ждёт изменения до N с (не больше `LISTOK_LONGPOLL_MAX`), потом 304. В журнал опросов пишется один итог запроса. |
| GET | `/install/{token}` | sh-установщик агента (`?ip=` — IP сервера в сети роутера, D-028; `?mode=poll&interval=<сек>` — режим опроса, 60…86400). Одноразовый, 24 ч; тратится только запросом curl или wget, остальным — страница-подсказка (D-030). 404 — ссылка недействительна, 409 — у роутера нет включённых фидов (ссылка не тратится). Ошибки — телом `echo …; exit 1`. |
| GET | `/agent/listok-agent.uc` | Текущая версия агента, для самообновления (этап 5, ещё не сделано). |

Неизвестный токен → `404` без подробностей. `/f/`, `/install/`, `/agent/v1` ограничены 120 запросами в минуту с IP (429 + `Retry-After`).

## 2. Агент: `/agent/v1`, заголовок `Authorization: Bearer <agent_token>`

| Метод | Путь | Тело / ответ |
|---|---|---|
| POST | `/agent/v1/hello` | `{agent_version, forkop_version, singbox_version, mode, interval_s, sections:[...]}` → `{feeds:[{section, token, url}], agent_version, report_interval_s, report_enabled}`. Обновляет `last_seen_at`, `last_ip`, версии и режим роутера (пустые не затирают). `token` — актуальный токен фида: агент подхватывает перевыпущенную ссылку. `agent_version` в ответе — актуальная на сервере |
| POST | `/agent/v1/wait` | `{feeds: {секция: etag}, wait}` → `{changed:[...], gone:[...]}`. Long-poll по всем секциям роутера сразу (D-032): ответ, как только изменилась или пропала (фид удалён или выключен) хоть одна, или пустой по истечении `wait` (не больше `LISTOK_LONGPOLL_MAX`). `wait: 0` — проверить и ответить сразу (режим `poll`). До 64 секций; плохое имя секции → 400. Обновляет `last_seen_at` |
| POST | `/agent/v1/report` | `{observations:[{host, ip, network, port, rule, outbound, hits, bytes}]}` → 204 (этап 4, ещё не сделано) |
| POST | `/agent/v1/applied` | `{section, etag, ok, error?}`: результат `forkop list_update` → 204. Неизвестная секция → 400 |

Неверный или отозванный токен агента → 401. Тело JSON до 64 КБ. В БД хранится только sha256 токена агента.

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
| `POST /lists/{slug}/entries/bulk` | Массовое действие над выбранными: `op` = delete / disable / enable, `id` повторяется. Одна версия на действие (`message` «удаление выбранных (N)» и т.п.). id чужого списка или уже удалённой записи пропускается, больше 5000 id или неизвестный `op` — 400. Ответ: `<tbody id="rows">` с текущим фильтром (`q`, `kind` из формы фильтров) + OOB счётчики. Нужна роль owner/editor/admin |
| `POST /lists/{slug}/clear` | Удалить все записи одной версией (`message` «очистка списка»), список остаётся. Откатывается через историю. htmx: 204 + `HX-Redirect` на список. Нужна роль owner/editor/admin |
| `POST /lists/{slug}/delete` | Удалить список насовсем вместе с историей (D-027), запись `list.delete` в `audit_log`. htmx: 204 + `HX-Redirect` на `/lists`. Только owner/admin |
| `GET /lists/{slug}/history` | Лента версий (по 50, дальше `…/history/more?before=N` по прокрутке) |
| `GET /lists/{slug}/history/{v}` | Фрагмент: изменения версии v |
| `POST /lists/{slug}/rollback/{v}` | Откат к состоянию после версии v (0 = пустой список). htmx: 204 + `HX-Redirect` на историю. Нужна роль owner/editor/admin |
| `GET/POST /lists/{slug}/members` | Права |
| `GET /proposals`, `GET /proposals/{id}` | Предложения |
| `POST /proposals`, `POST /proposals/{id}/decide` | Создать / решить (всё или по строкам) |
| `GET /routers`, `POST /routers` | Роутеры пользователя (админ видит все), создание |
| `GET /routers/{id}`, `POST /routers/{id}`, `POST /routers/{id}/delete` | Карточка роутера: фиды со статусом, ссылками и журналом; правка; удаление |
| `POST /routers/{id}/feeds` | Новый фид: `section` + `list` (несколько) |
| `POST /feeds/{id}/lists` | `toggle` | `regenerate` | `delete` | Состав, вкл/выкл, новая ссылка (старая сразу 404), удаление |
| `POST /routers/{id}/install` | Выдать ссылку установки агента: `mode` (`wait` или `poll`), `interval` (минут, 1…1440, для `poll`), `server_ip` (необязательно). Ответ — карточка роутера с командой установки (показывается один раз) |
| `POST /routers/{id}/agent/revoke` | Отвязать агент: забыть его токен. htmx: 204 + `HX-Redirect` |

Действия с подтверждением (`hx-confirm`) приходят от htmx и получают 204 + `HX-Redirect`, обычные формы — 303.
| `GET /suggestions` | Подсказки из clash API: добавить / скрыть / игнорировать шаблон |
| `GET /check?q=` | «Покрыт ли» |
| `GET /events` | SSE: статусы роутеров, новые предложения, применение фида |
| `GET /settings`, `/settings/tokens`, `/users` | Настройки, API-токены, пользователи (админ) |
| `GET /share?url=&text=` | Цель Web Share Target из PWA → форма быстрого добавления |

Ошибки для htmx: 401 без сессии (app.js уводит на `/login`), 403 нет прав, 404 не найдено или не видно, 422 ошибка валидации (фрагмент с сообщением вставляется). Остальные коды показывает всплывашка из `app.js`.
