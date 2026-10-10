-- Milestone 2: API tokens, OAuth for MCP connectors, and the data Claude posts.

-- Bearer tokens for Claude routines, one board each. Only a SHA-256 hash is stored.
CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  board_id     INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL,
  created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  last_used_at TEXT
);

CREATE TABLE briefings (
  id         INTEGER PRIMARY KEY,
  board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  markdown   TEXT NOT NULL,
  stats      TEXT NOT NULL DEFAULT '{}',
  source_id  TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE UNIQUE INDEX briefings_source ON briefings (board_id, source_id) WHERE source_id IS NOT NULL;
CREATE INDEX briefings_recent ON briefings (board_id, created_at);

CREATE TABLE inbox_items (
  id         INTEGER PRIMARY KEY,
  board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  type       TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL,
  snippet    TEXT NOT NULL DEFAULT '',
  url        TEXT NOT NULL DEFAULT '',
  reason     TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new','added','snoozed','dismissed')),
  source_id  TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE UNIQUE INDEX inbox_source ON inbox_items (board_id, source_id) WHERE source_id IS NOT NULL;

CREATE TABLE widgets (
  board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  payload    TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  PRIMARY KEY (board_id, name)
);

CREATE TABLE timeboxes (
  id       INTEGER PRIMARY KEY,
  board_id INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  date     TEXT NOT NULL,
  start    TEXT NOT NULL,
  "end"    TEXT NOT NULL,
  kind     TEXT NOT NULL CHECK (kind IN ('task','meeting','break')),
  title    TEXT NOT NULL,
  item_ids TEXT NOT NULL DEFAULT '[]',
  accepted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX timeboxes_day ON timeboxes (board_id, date, start);

-- OAuth 2.1 for MCP connectors (e.g. claude.ai). Clients register dynamically (RFC 7591).
CREATE TABLE oauth_clients (
  id            INTEGER PRIMARY KEY,
  client_id     TEXT NOT NULL UNIQUE,
  secret_hash   TEXT,
  name          TEXT NOT NULL DEFAULT '',
  redirect_uris TEXT NOT NULL,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- A grant is one user's consent for one client to use the chosen boards.
CREATE TABLE oauth_grants (
  id           INTEGER PRIMARY KEY,
  client_id    TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  last_used_at TEXT
);

CREATE TABLE oauth_grant_boards (
  grant_id INTEGER NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
  board_id INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  PRIMARY KEY (grant_id, board_id)
);

-- Authorization codes, access tokens and refresh tokens, all stored hashed.
CREATE TABLE oauth_tokens (
  hash           TEXT PRIMARY KEY,
  kind           TEXT NOT NULL CHECK (kind IN ('code','access','refresh')),
  grant_id       INTEGER NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
  expires_at     TEXT NOT NULL,
  redirect_uri   TEXT NOT NULL DEFAULT '',
  code_challenge TEXT NOT NULL DEFAULT '',
  resource       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX oauth_tokens_grant ON oauth_tokens (grant_id);
