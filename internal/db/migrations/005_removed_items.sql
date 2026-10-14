-- To-dos removed from the dashboard (the × button). A routine re-posting the same source_id
-- to that list is skipped, so removed items don't come back on the next run.
CREATE TABLE removed_items (
  list_id    INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  source_id  TEXT NOT NULL,
  removed_at TEXT NOT NULL,
  PRIMARY KEY (list_id, source_id)
);
