-- A board for the homelab fleet (briefings, incidents, to-dos), added 2026-11-01.
-- Workdesk has no "new board" UI yet, so boards are seeded here like Work and Personal.
INSERT INTO boards (name, color, timezone, has_schedule, position)
  SELECT 'Homelab', '#4a7a8c', 'America/Chicago', 0, 2
  WHERE NOT EXISTS (SELECT 1 FROM boards WHERE name = 'Homelab');

INSERT INTO lists (board_id, name, kind)
  SELECT id, 'Today', 'daily' FROM boards
  WHERE name = 'Homelab' AND NOT EXISTS (SELECT 1 FROM lists WHERE board_id = boards.id AND name = 'Today');
