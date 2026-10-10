-- Milestone 3: snoozed inbox items come back as "new" once snoozed_until passes.
ALTER TABLE inbox_items ADD COLUMN snoozed_until TEXT;
