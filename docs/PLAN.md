# Workdesk — build plan

Self-hosted dashboard where Claude routines post briefings, to-dos, timeboxes and
calendar/Jira snapshots, and people check things off. Design reference:
the "Chosen direction · Option C, outdoors" artboards in the Workdesk design canvas.

## Decisions so far

- Boards (Work, Personal, …), each with its own lists. Whole boards are shared per person.
- Sign-in with Google. Only invited emails can sign in.
- The app stores **no** email/calendar/Jira credentials. Claude reads those through its own
  connectors and posts results here. Items keep a link back (Jira issue, email, event).
- API for Claude: REST + an MCP server, with per-board tokens.
- Timeboxes live on the dashboard only (never written to Google Calendar).
- Unfinished "Today" items carry over at midnight and keep a carried-days count.
- Work board has the "Your day" timebox schedule; Personal does not.
- Light ("trail day") and dark ("forest night") themes.
- Drag-and-drop layout per user.
- Hosted on Proxmox, as light as possible.

## Stack (lightweight)

| Part | Choice | Why |
|---|---|---|
| Server | One Go binary (`net/http`) | ~15 MB file, ~20–40 MB RAM, no runtime to install |
| Database | SQLite (pure-Go driver, WAL mode) | One file, no DB server |
| Frontend | Preact + Vite, embedded in the binary | ~30 KB JS; same CSS variables as the mockup |
| Drag & drop | SortableJS | Small, touch-friendly |
| Sign-in | Google OpenID Connect | Only invited emails allowed |
| MCP | Streamable HTTP at `/mcp` in the same binary | One process serves the UI, the API and MCP |

## Data model

- `users` (google sub, email, name)
- `boards` (name, color, timezone, has_schedule)
- `board_members` (board, user, role: owner / editor / checker / viewer)
- `api_tokens` (board, name, hashed token, last_used) — one per Claude routine
- `lists` (board, name, kind: daily | standing, sections)
- `items` (list, title, section, done, due, carried_days, source_type, source_id, source_url, position)
  - `source_id` makes posts idempotent, so re-runs don't duplicate items
- `briefings` (board, markdown, stats JSON, created_at)
- `inbox_items` (board, type, title, snippet, url, reason, status: new / added / snoozed / dismissed)
- `widgets` (board, name, JSON payload) — calendar events, Jira issues snapshot
- `timeboxes` (board, date, start, end, kind: task / meeting / break, title, item_ids, accepted)
- `layouts` (user, board, JSON of box order and sizes)

## API (REST, bearer token or session cookie)

```
GET   /api/boards
POST  /api/boards/{board}/briefings
GET   /api/boards/{board}/lists
POST  /api/boards/{board}/lists
GET   /api/lists/{list}/items?open=true
POST  /api/lists/{list}/items            (one or many, upsert by source_id)
PATCH /api/items/{id}                    (done, title, move to list)
POST  /api/boards/{board}/inbox
PUT   /api/boards/{board}/widgets/{name}
PUT   /api/boards/{board}/days/{date}/timeboxes
```

## MCP tools (same logic as the API)

`list_boards`, `post_briefing`, `list_lists`, `create_list`, `list_open_items`,
`add_items`, `update_item`, `push_inbox`, `set_widget`, `propose_timeboxes`.

## Background jobs (inside the binary)

- Midnight per board timezone: move unchecked Today items to the next day (skip weekends on
  boards marked work-days-only), increment `carried_days`.
- Nightly SQLite backup to `/var/lib/workdesk/backups` (keep 14).

## Deploy on Proxmox

- Target: existing Docker VM `docker-host` at `192.168.50.85` (AMD64), alongside other
  containers. Proxmox host is `192.168.50.115`.
- One small container: a static linux/amd64 Go binary in a distroless/scratch image
  (~20 MB image, ~30 MB RAM), run with `docker compose`. Data in a named volume
  (`workdesk-data:/data`, SQLite file `/data/workdesk.db`). Settings via `.env`
  (Google OAuth client, session secret, base URL, time zone).
- Public hostname `workdesk.voodootechlabs.com` as a new public-hostname route on the
  existing `cloudflared` container on docker-host (workdesk joins its Docker network; no
  host port needs to be published). Needed so cloud Claude routines can reach the API and MCP.
- Updates: GitHub Actions builds and pushes the image to GHCR (private) on each tag;
  `docker compose pull && docker compose up -d` on the VM.
- Backups: Proxmox vzdump of the VM, plus nightly SQLite copies inside the data volume.

## Work board schedule defaults

- Time zone America/Chicago. Workday 7:00 – 3:30 (some days to 4:00), lunch hour, 15 min buffer after meetings,
  last 15 min = wrap-up and carry-over. Editable per board.

## Milestones

1. Skeleton: Go server, SQLite migrations, Google sign-in, boards and lists, Today list UI.
2. API + tokens + MCP server; a sample Claude routine prompt that posts a briefing.
3. Outdoors tile UI: brief, progress, lists, Jira/calendar widgets, inbox triage, themes.
4. Timeboxes ("Your day"), carry-over job, carry-over review.
5. Sharing and roles, drag-and-drop layouts, phone layout.
6. Proxmox install script, systemd unit, release workflow, backups.
