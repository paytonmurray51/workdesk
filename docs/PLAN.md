# Workdesk — build plan

Self-hosted dashboard where Claude routines post briefings, to-dos, timeboxes and
calendar/Jira snapshots, and people check things off. Design reference:
the "Chosen direction · Option C, outdoors" artboards in the Workdesk design canvas.

## Decisions so far

- Boards (Work, Personal, …), each with its own lists. Whole boards are shared per person.
- Sign-in with Google. Only invited emails can sign in.
- The app stores **no** email/calendar/Jira credentials. Claude reads those through its own
  connectors and posts results here. Items keep a link back (Jira issue, email, event).
- API for Claude: REST + an MCP server. Two ways in (decided 2026-10-10):
  - **Board API tokens** (`wdk_…`) for Claude Code / scripts: one board each, created and
    revoked in Settings, stored as SHA-256 hashes, secret shown once.
  - **OAuth 2.1** so workdesk can be added as a claude.ai custom connector (claude.ai can't
    send a fixed bearer header). Built into the binary: dynamic client registration, code +
    S256 PKCE, 1 h access / rotating 30-day refresh tokens, all hashed. Consent needs Google
    sign-in, and the person **picks which boards** the connector gets (can be several).
    Revoke under Settings → Connected apps. Client ID Metadata Documents aren't supported
    yet (DCR only).
  - Tokens and grants can never create or list other tokens (session-only endpoints).
- Timeboxes live on the dashboard only (never written to Google Calendar).
- Unfinished "Today" items carry over at midnight and keep a carried-days count.
- Work board has the "Your day" timebox schedule; Personal does not.
- Light ("trail day") and dark ("forest night") themes.
- Drag-and-drop layout per user.
- Hosted on Proxmox, as light as possible.

## Stack (lightweight)

| Part | Choice | Why |
|---|---|---|
| Server | One Go binary (`net/http`), Go 1.26 | ~15 MB file, ~20–40 MB RAM, no runtime to install |
| Database | SQLite (pure-Go `modernc.org/sqlite`, WAL mode) | One file, no DB server |
| Frontend | Preact + Vite, embedded in the binary | ~30 KB JS; same CSS variables as the mockup |
| Drag & drop | SortableJS | Small, touch-friendly |
| Sign-in | Google OpenID Connect | Only invited emails allowed |
| MCP | Streamable HTTP at `/mcp` in the same binary | One process serves the UI, the API and MCP |

## Data model

- `users` (google sub, email, name)
- `boards` (name, color, timezone, has_schedule)
- `board_members` (board, user, role: owner / editor / checker / viewer)
- `api_tokens` (board, name, hashed token, prefix, last_used) — one per Claude routine
- `oauth_clients`, `oauth_grants` (+ `oauth_grant_boards`), `oauth_tokens` (codes, access and
  refresh tokens, hashed) — for MCP connectors
- `lists` (board, name, kind: daily | standing, sections)
- `items` (list, title, section, done, done_at, due, carried_days, source_type, source_id, source_url, position)
  - `done_at` lets a daily list show open items plus those checked since local midnight
  - `source_id` makes posts idempotent, so re-runs don't duplicate items
- `briefings` (board, markdown, stats JSON, source_id, created_at) — upsert by source_id
- `inbox_items` (board, type, title, snippet, url, reason, status: new / added / snoozed / dismissed,
  source_id) — re-push keeps status
- `widgets` (board, name, JSON payload) — calendar events, Jira issues snapshot
- `timeboxes` (board, date, start, end, kind: task / meeting / break, title, item_ids, accepted)
- `layouts` (user, board, JSON of box order and sizes)

## API (REST, bearer token or session cookie)

`{board}`, `{list}`, `{id}` are numeric ids. Built in milestone 2.

```
GET    /api/me
GET    /api/boards
GET    /api/boards/{board}/lists
POST   /api/boards/{board}/lists                 {name, kind}; same name returns existing (200)
GET    /api/lists/{list}/items?open=true
POST   /api/lists/{list}/items                   one or many, upsert by source_id
PATCH  /api/items/{id}                           {done, title, list_id}
POST   /api/boards/{board}/briefings             {markdown, stats, source_id}
GET    /api/boards/{board}/briefings/latest
POST   /api/boards/{board}/inbox                 one or many, upsert by source_id
GET    /api/boards/{board}/inbox?status=new
PATCH  /api/inbox/{id}                           {status: new|snoozed|dismissed, until}
POST   /api/inbox/{id}/add                       {list_id}; default Today, same board only
PUT    /api/boards/{board}/widgets/{name}        body = JSON payload
GET    /api/boards/{board}/widgets
PUT    /api/boards/{board}/days/{date}/timeboxes array or {boxes: [...]}, replaces the day
GET    /api/boards/{board}/days/{date}/timeboxes
# session only (Settings UI)
GET|POST /api/boards/{board}/tokens   DELETE /api/tokens/{id}
GET      /api/connections             DELETE /api/connections/{id}
```

OAuth endpoints: `/.well-known/oauth-protected-resource[/mcp]`,
`/.well-known/oauth-authorization-server`, `/oauth/register`, `/oauth/authorize`, `/oauth/token`.

## MCP tools (same logic as the API)

`list_boards`, `post_briefing`, `list_lists`, `create_list`, `list_open_items`,
`add_items`, `update_item`, `push_inbox`, `set_widget`, `propose_timeboxes`,
`clear_board` (added 2026-10-10: erases a board's items, briefings, inbox, widgets and
timeboxes; keeps the board, lists, tokens and connections; `confirm` must be the exact board
name; annotated destructive; no undo; logged with who did it. REST:
`POST /api/boards/{board}/clear {confirm}`).

Streamable HTTP at `/mcp` (official Go SDK, stateless, JSON responses). Boards can be named
by name or id; with a single-board token the board may be omitted. Lists default to the
board's "Today". Unauthenticated calls get `401` with `WWW-Authenticate: Bearer
resource_metadata=…` so connectors discover OAuth. Sample routine prompt:
[docs/routines/morning-brief.md](routines/morning-brief.md).

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
  existing `cloudflared` container on docker-host. Needed so cloud Claude routines can reach
  the API and MCP.
  - cloudflared (token mode, plain `docker run`) is only on Docker's default `bridge`
    network, which has no container-name DNS, and the other services on docker-host are
    reached by host port. So workdesk does the same: compose publishes
    `172.17.0.1:8080` (docker0 gateway) and `127.0.0.1:8080`, not the LAN, and the tunnel
    route is `http://172.17.0.1:8080`. No `TUNNEL_NETWORK` setting; cloudflared is untouched.
- Container: `gcr.io/distroless/static-debian12:nonroot` (built image ~14 MB),
  `container_name: workdesk`, read-only root fs, all capabilities dropped, 128 MB memory
  limit, healthcheck via `/workdesk healthcheck` (→ `GET /healthz`).
  `no-new-privileges` is off because on docker-host it makes exec fail.
- Updates: GitHub Actions builds and pushes the image to GHCR (private) on each tag;
  `docker compose pull && docker compose up -d` on the VM.
- Backups: Proxmox vzdump of the VM, plus nightly SQLite copies inside the data volume.

## Work board schedule defaults

- Time zone America/Chicago. Workday 7:00 – 3:30 (some days to 4:00), lunch hour, 15 min buffer after meetings,
  last 15 min = wrap-up and carry-over. Editable per board.

## Milestones

1. Skeleton: Go server, SQLite migrations, Google sign-in, boards and lists, Today list UI.
   **Done 2026-10-10.** Only `WORKDESK_ADMIN_EMAILS` can sign in (rechecked on every
   request) until invites land in milestone 5; admins see every board. Seeds Work
   (`has_schedule`) and Personal, each with a daily "Today" list. API so far (session cookie
   only): `GET /api/me`, `GET /api/boards`, `GET /api/boards/{board}/lists`,
   `GET|POST /api/lists/{list}/items`, `PATCH /api/items/{id}` (done, title).
2. API + tokens + MCP server; a sample Claude routine prompt that posts a briefing.
   **Done 2026-10-10.** Also: OAuth for claude.ai connectors (see Decisions), Settings page
   (MCP URL, tokens, connected apps), and the Morning brief tile shows the latest briefing.
3. Outdoors tile UI: brief, progress, lists, Jira/calendar widgets, inbox triage, themes.
   **Done 2026-10-10.** 12-column tile grid (single column on phones), refresh every minute
   and on tab focus. Inbox triage: Add to Today / Snooze (until 7:00 tomorrow, board time
   zone; `inbox_items.snoozed_until`) / Dismiss, via `PATCH /api/inbox/{id}` and
   `POST /api/inbox/{id}/add`. Widget payloads the UI understands:
   - `calendar`: `{date, events: [{title, start "HH:MM", end, location, url, id}]}`
   - `jira`: `{issues: [{key, summary, status, priority, due "YYYY-MM-DD", url}]}`
   Calendar/Jira tiles appear once a routine has posted them.
4. Timeboxes ("Your day"), carry-over job, carry-over review.
5. Sharing and roles, drag-and-drop layouts, phone layout.
6. Proxmox install script, systemd unit, release workflow, backups.
