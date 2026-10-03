# Схема БД (SQLite)

PRAGMA при открытии: `journal_mode=WAL`, `foreign_keys=ON`, `busy_timeout=5000`, `synchronous=NORMAL`.
Время хранится в `INTEGER`: unix-секунды UTC. Булевы значения — `INTEGER` 0/1.
Токены, которые не нужно показывать повторно, хранятся как `sha256` (hex). Ссылки фидов хранятся открыто: их нужно показывать в интерфейсе (см. `decisions.md`, D-006).

## Связи

```
users ─┬─< sessions
       ├─< api_tokens
       ├─< lists (owner) ─┬─< entries
       │                  ├─< list_members >── users
       │                  ├─< list_includes (parent/child) >── lists
       │                  ├─< list_versions ─< entry_changes
       │                  └─< proposals ─< proposal_items
       └─< routers ─┬─< feeds ─┬─< feed_lists >── lists
                    │          └─< feed_fetches
                    └─< observations
audit_log, suggestion_ignores, settings — отдельно
```

## Пользователи и доступ

```sql
CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,              -- argon2id, формат PHC
  is_admin      INTEGER NOT NULL DEFAULT 0,
  tg_chat_id    INTEGER,                    -- для уведомлений, необязательно
  created_at    INTEGER NOT NULL,
  disabled_at   INTEGER
);

CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,            -- sha256 cookie-токена
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip           TEXT,
  user_agent   TEXT
);

CREATE TABLE api_tokens (                   -- для Shortcuts, скриптов, бота
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL,               -- первые 6 символов для показа
  scopes       TEXT NOT NULL DEFAULT 'entries:write', -- через пробел
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at   INTEGER
);
```

## Списки и записи

```sql
CREATE TABLE lists (
  id           INTEGER PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,        -- [a-z0-9-], используется в URL
  title        TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  owner_id     INTEGER NOT NULL REFERENCES users(id),
  kind         TEXT NOT NULL CHECK (kind IN ('manual','remote')),
  -- только для kind='remote':
  remote_url        TEXT,
  remote_interval_s INTEGER,                -- по умолчанию 86400
  remote_etag       TEXT,
  remote_fetched_at INTEGER,
  remote_error      TEXT,
  version      INTEGER NOT NULL DEFAULT 0,  -- растёт на каждое изменение содержимого
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  archived_at  INTEGER
);

CREATE TABLE list_members (                 -- владелец хранится в lists.owner_id, здесь остальные
  list_id INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role    TEXT NOT NULL CHECK (role IN ('editor','viewer')),
  PRIMARY KEY (list_id, user_id)
);

CREATE TABLE list_includes (                -- вложенный список: содержимое child входит в parent
  parent_id  INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  child_id   INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  added_by   INTEGER REFERENCES users(id),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (parent_id, child_id),
  CHECK (parent_id <> child_id)
);                                          -- циклы запрещаются в коде (обход графа перед вставкой)

CREATE TABLE entries (
  id         INTEGER PRIMARY KEY,
  list_id    INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  value      TEXT NOT NULL,                 -- нормализованное: 'youtube.com', '104.29.0.0/16'
  kind       TEXT NOT NULL CHECK (kind IN ('domain','cidr4','cidr6')),
  comment    TEXT NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 1,
  expires_at INTEGER,                       -- временная запись; expirer ставит enabled=0
  added_by   INTEGER REFERENCES users(id),  -- NULL для remote
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (list_id, value)
);
CREATE INDEX entries_value ON entries(value);         -- поиск «покрыт ли»
CREATE INDEX entries_expires ON entries(expires_at) WHERE expires_at IS NOT NULL;
```

Записи remote-списков лежат в той же `entries`. При обновлении они заменяются по разнице (diff), поэтому история работает одинаково. Редактировать remote-список руками нельзя.

## История

```sql
CREATE TABLE list_versions (
  id         INTEGER PRIMARY KEY,
  list_id    INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  version    INTEGER NOT NULL,              -- = lists.version после изменения
  user_id    INTEGER REFERENCES users(id),  -- NULL = система
  source     TEXT NOT NULL CHECK (source IN ('web','api','proposal','remote','expire','rollback','import')),
  message    TEXT NOT NULL DEFAULT '',
  proposal_id INTEGER REFERENCES proposals(id),
  created_at INTEGER NOT NULL,
  UNIQUE (list_id, version)
);

CREATE TABLE entry_changes (
  id          INTEGER PRIMARY KEY,
  version_id  INTEGER NOT NULL REFERENCES list_versions(id) ON DELETE CASCADE,
  op          TEXT NOT NULL CHECK (op IN ('add','remove','update')),
  value       TEXT NOT NULL,
  kind        TEXT NOT NULL,
  old_comment TEXT, new_comment TEXT,
  old_enabled INTEGER, new_enabled INTEGER,
  old_expires_at INTEGER, new_expires_at INTEGER
);
CREATE INDEX entry_changes_version ON entry_changes(version_id);
```

Откат к версии N — это одна новая версия с `source='rollback'`. Она применяет обратные операции всех версий после N в обратном порядке. Историю не переписываем.

## Предложения (аналог PR)

```sql
CREATE TABLE proposals (
  id             INTEGER PRIMARY KEY,
  target_list_id INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  author_id      INTEGER NOT NULL REFERENCES users(id),
  type           TEXT NOT NULL CHECK (type IN ('entries','include')),
  source_list_id INTEGER REFERENCES lists(id),  -- для type='include'
  message        TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL CHECK (status IN ('open','accepted','partial','rejected','withdrawn')),
  decided_by     INTEGER REFERENCES users(id),
  decided_at     INTEGER,
  decision_note  TEXT,
  created_at     INTEGER NOT NULL
);
CREATE INDEX proposals_open ON proposals(target_list_id) WHERE status = 'open';

CREATE TABLE proposal_items (               -- для type='entries'
  id          INTEGER PRIMARY KEY,
  proposal_id INTEGER NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
  op          TEXT NOT NULL CHECK (op IN ('add','remove')),
  value       TEXT NOT NULL,
  kind        TEXT NOT NULL,
  comment     TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','rejected'))
);
```

## Роутеры и фиды

```sql
CREATE TABLE routers (
  id                 INTEGER PRIMARY KEY,
  name               TEXT NOT NULL,         -- 'дом', 'дача', 'офис'
  owner_id           INTEGER NOT NULL REFERENCES users(id),
  agent_token_hash   TEXT UNIQUE,           -- агент шлёт его в отчётах
  install_token_hash TEXT UNIQUE,           -- одноразовый, для /install/<token>
  install_expires_at INTEGER,
  report_enabled     INTEGER NOT NULL DEFAULT 1, -- отправлять ли выжимку из clash API
  last_seen_at       INTEGER,
  last_ip            TEXT,
  agent_version      TEXT,
  forkop_version     TEXT,
  singbox_version    TEXT,
  notes              TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL
);

CREATE TABLE feeds (
  id            INTEGER PRIMARY KEY,
  router_id     INTEGER NOT NULL REFERENCES routers(id) ON DELETE CASCADE,
  section       TEXT NOT NULL,              -- секция forkop: 'main', 'geo', ...
  token         TEXT NOT NULL UNIQUE,       -- 32 байта base62, открыто (D-006)
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_fetch_at INTEGER,
  last_etag     TEXT,                       -- что роутер получил последним
  created_at    INTEGER NOT NULL,
  UNIQUE (router_id, section)
);

CREATE TABLE feed_lists (
  feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  list_id INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  PRIMARY KEY (feed_id, list_id)
);

CREATE TABLE feed_fetches (                 -- журнал, хранение 30 дней
  id      INTEGER PRIMARY KEY,
  feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  at      INTEGER NOT NULL,
  ip      TEXT,
  status  INTEGER NOT NULL,                 -- 200 / 304
  etag    TEXT
);
CREATE INDEX feed_fetches_feed_at ON feed_fetches(feed_id, at);
```

Фид может включать только списки, которые владелец роутера видит. Проверка выполняется при сохранении фида и при отзыве прав. Если права отозвали, список выпадает из фида, а событие пишется в `audit_log`.

## Подсказки из clash API

```sql
CREATE TABLE observations (
  id         INTEGER PRIMARY KEY,
  router_id  INTEGER NOT NULL REFERENCES routers(id) ON DELETE CASCADE,
  host       TEXT NOT NULL,                 -- metadata.host из clash API (уже нормализованный)
  sample_ip  TEXT,
  network    TEXT,                          -- tcp/udp
  sample_port INTEGER,
  rule       TEXT,                          -- например 'final'
  outbound   TEXT,                          -- например 'direct-out'
  hits       INTEGER NOT NULL DEFAULT 1,
  bytes      INTEGER NOT NULL DEFAULT 0,
  first_seen INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  status     TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new','added','dismissed')),
  UNIQUE (router_id, host)
);

CREATE TABLE suggestion_ignores (           -- глобально не предлагать: '*.apple.com', 'yandex.ru'
  pattern    TEXT PRIMARY KEY,
  created_by INTEGER REFERENCES users(id),
  created_at INTEGER NOT NULL
);
```

## Служебное

```sql
CREATE TABLE audit_log (
  id          INTEGER PRIMARY KEY,
  at          INTEGER NOT NULL,
  user_id     INTEGER REFERENCES users(id),
  action      TEXT NOT NULL,                -- 'login', 'token.create', 'feed.regenerate', ...
  object_type TEXT, object_id INTEGER,
  details     TEXT,                         -- JSON
  ip          TEXT
);

CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
-- schema_migrations (version, applied_at) создаёт сам раннер миграций (internal/db/migrate.go), в 0001 её нет.
```
