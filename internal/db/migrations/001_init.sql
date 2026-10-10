CREATE TABLE users (
  id         INTEGER PRIMARY KEY,
  google_sub TEXT NOT NULL UNIQUE,
  email      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  name       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE boards (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  color        TEXT NOT NULL DEFAULT '',
  timezone     TEXT NOT NULL DEFAULT 'America/Chicago',
  has_schedule INTEGER NOT NULL DEFAULT 0,
  position     INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE board_members (
  board_id INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role     TEXT NOT NULL CHECK (role IN ('owner','editor','checker','viewer')),
  PRIMARY KEY (board_id, user_id)
);

CREATE TABLE lists (
  id         INTEGER PRIMARY KEY,
  board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('daily','standing')),
  sections   TEXT NOT NULL DEFAULT '[]',
  position   INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  UNIQUE (board_id, name)
);

CREATE TABLE items (
  id           INTEGER PRIMARY KEY,
  list_id      INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  title        TEXT NOT NULL,
  section      TEXT NOT NULL DEFAULT '',
  done         INTEGER NOT NULL DEFAULT 0,
  done_at      TEXT,
  due          TEXT,
  carried_days INTEGER NOT NULL DEFAULT 0,
  source_type  TEXT NOT NULL DEFAULT '',
  source_id    TEXT,
  source_url   TEXT NOT NULL DEFAULT '',
  position     INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- Idempotent posts: one item per (list, source_id).
CREATE UNIQUE INDEX items_source ON items (list_id, source_id) WHERE source_id IS NOT NULL;
CREATE INDEX items_list_open ON items (list_id, done, position);

INSERT INTO boards (name, color, timezone, has_schedule, position) VALUES
  ('Work',     '#4f8a5b', 'America/Chicago', 1, 0),
  ('Personal', '#c9803a', 'America/Chicago', 0, 1);

INSERT INTO lists (board_id, name, kind)
  SELECT id, 'Today', 'daily' FROM boards;
