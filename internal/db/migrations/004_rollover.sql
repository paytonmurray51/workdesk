-- Milestone 4: the local date each board last ran its midnight carry-over.
ALTER TABLE boards ADD COLUMN rolled_over_on TEXT;
