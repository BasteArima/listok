-- Исходная схема. Описание и связи: docs/db-schema.md.
-- Время: unix-секунды UTC. Булевы: 0/1.

-- Пользователи и доступ ------------------------------------------------------

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  is_admin      INTEGER NOT NULL DEFAULT 0,
  tg_chat_id    INTEGER,
  created_at    INTEGER NOT NULL,
  disabled_at   INTEGER
);

CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip           TEXT,
  user_agent   TEXT
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL,
  scopes       TEXT NOT NULL DEFAULT 'entries:write',
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at   INTEGER
);

-- Списки и записи -----------------------------------------------------------

CREATE TABLE lists (
  id                INTEGER PRIMARY KEY,
  slug              TEXT NOT NULL UNIQUE,
  title             TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',
  owner_id          INTEGER NOT NULL REFERENCES users(id),
  kind              TEXT NOT NULL CHECK (kind IN ('manual','remote')),
  remote_url        TEXT,
  remote_interval_s INTEGER,
  remote_etag       TEXT,
  remote_fetched_at INTEGER,
  remote_error      TEXT,
  version           INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  archived_at       INTEGER,
  CHECK ((kind = 'remote') = (remote_url IS NOT NULL))
);

CREATE TABLE list_members (
  list_id INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role    TEXT NOT NULL CHECK (role IN ('editor','viewer')),
  PRIMARY KEY (list_id, user_id)
);

CREATE TABLE list_includes (
  parent_id  INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  child_id   INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  added_by   INTEGER REFERENCES users(id),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (parent_id, child_id),
  CHECK (parent_id <> child_id)
);

CREATE TABLE entries (
  id         INTEGER PRIMARY KEY,
  list_id    INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  value      TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('domain','cidr4','cidr6')),
  comment    TEXT NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 1,
  expires_at INTEGER,
  added_by   INTEGER REFERENCES users(id),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (list_id, value)
);
CREATE INDEX entries_value ON entries(value);
CREATE INDEX entries_expires ON entries(expires_at) WHERE expires_at IS NOT NULL;

-- Предложения ---------------------------------------------------------------

CREATE TABLE proposals (
  id             INTEGER PRIMARY KEY,
  target_list_id INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  author_id      INTEGER NOT NULL REFERENCES users(id),
  type           TEXT NOT NULL CHECK (type IN ('entries','include')),
  source_list_id INTEGER REFERENCES lists(id),
  message        TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL CHECK (status IN ('open','accepted','partial','rejected','withdrawn')),
  decided_by     INTEGER REFERENCES users(id),
  decided_at     INTEGER,
  decision_note  TEXT,
  created_at     INTEGER NOT NULL,
  CHECK ((type = 'include') = (source_list_id IS NOT NULL))
);
CREATE INDEX proposals_open ON proposals(target_list_id) WHERE status = 'open';

CREATE TABLE proposal_items (
  id          INTEGER PRIMARY KEY,
  proposal_id INTEGER NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
  op          TEXT NOT NULL CHECK (op IN ('add','remove')),
  value       TEXT NOT NULL,
  kind        TEXT NOT NULL CHECK (kind IN ('domain','cidr4','cidr6')),
  comment     TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','rejected'))
);
CREATE INDEX proposal_items_proposal ON proposal_items(proposal_id);

-- История -------------------------------------------------------------------

CREATE TABLE list_versions (
  id          INTEGER PRIMARY KEY,
  list_id     INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  version     INTEGER NOT NULL,
  user_id     INTEGER REFERENCES users(id),
  source      TEXT NOT NULL CHECK (source IN ('web','api','proposal','remote','expire','rollback','import')),
  message     TEXT NOT NULL DEFAULT '',
  proposal_id INTEGER REFERENCES proposals(id),
  created_at  INTEGER NOT NULL,
  UNIQUE (list_id, version)
);

CREATE TABLE entry_changes (
  id             INTEGER PRIMARY KEY,
  version_id     INTEGER NOT NULL REFERENCES list_versions(id) ON DELETE CASCADE,
  op             TEXT NOT NULL CHECK (op IN ('add','remove','update')),
  value          TEXT NOT NULL,
  kind           TEXT NOT NULL CHECK (kind IN ('domain','cidr4','cidr6')),
  old_comment    TEXT,
  new_comment    TEXT,
  old_enabled    INTEGER,
  new_enabled    INTEGER,
  old_expires_at INTEGER,
  new_expires_at INTEGER
);
CREATE INDEX entry_changes_version ON entry_changes(version_id);

-- Роутеры и фиды ------------------------------------------------------------

CREATE TABLE routers (
  id                 INTEGER PRIMARY KEY,
  name               TEXT NOT NULL,
  owner_id           INTEGER NOT NULL REFERENCES users(id),
  agent_token_hash   TEXT UNIQUE,
  install_token_hash TEXT UNIQUE,
  install_expires_at INTEGER,
  report_enabled     INTEGER NOT NULL DEFAULT 1,
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
  section       TEXT NOT NULL,
  token         TEXT NOT NULL UNIQUE,
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_fetch_at INTEGER,
  last_etag     TEXT,
  created_at    INTEGER NOT NULL,
  UNIQUE (router_id, section)
);

CREATE TABLE feed_lists (
  feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  list_id INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  PRIMARY KEY (feed_id, list_id)
);

CREATE TABLE feed_fetches (
  id      INTEGER PRIMARY KEY,
  feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  at      INTEGER NOT NULL,
  ip      TEXT,
  status  INTEGER NOT NULL,
  etag    TEXT
);
CREATE INDEX feed_fetches_feed_at ON feed_fetches(feed_id, at);

-- Подсказки из clash API ----------------------------------------------------

CREATE TABLE observations (
  id          INTEGER PRIMARY KEY,
  router_id   INTEGER NOT NULL REFERENCES routers(id) ON DELETE CASCADE,
  host        TEXT NOT NULL,
  sample_ip   TEXT,
  network     TEXT,
  sample_port INTEGER,
  rule        TEXT,
  outbound    TEXT,
  hits        INTEGER NOT NULL DEFAULT 1,
  bytes       INTEGER NOT NULL DEFAULT 0,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  status      TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new','added','dismissed')),
  UNIQUE (router_id, host)
);

CREATE TABLE suggestion_ignores (
  pattern    TEXT PRIMARY KEY,
  created_by INTEGER REFERENCES users(id),
  created_at INTEGER NOT NULL
);

-- Служебное -----------------------------------------------------------------

CREATE TABLE audit_log (
  id          INTEGER PRIMARY KEY,
  at          INTEGER NOT NULL,
  user_id     INTEGER REFERENCES users(id),
  action      TEXT NOT NULL,
  object_type TEXT,
  object_id   INTEGER,
  details     TEXT,
  ip          TEXT
);
CREATE INDEX audit_log_at ON audit_log(at);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
