# workdesk

Read `docs/PLAN.md` before starting work; it holds the decisions, stack, data model, API,
MCP tools and milestones. Update it when a decision changes.

## Rules

- The app never stores email, calendar or Jira credentials. Claude posts data in; items keep a
  link back to the source.
- Keep it lightweight: one Go binary (UI + REST API + MCP), SQLite, embedded Preact frontend,
  one small container. Don't add services (Redis, Postgres, queues) without asking.
- Never commit secrets. Settings come from `.env` (gitignored); keep `.env.example` current.
- API tokens are stored hashed and scoped to one board.
- Posts from Claude must be idempotent (upsert by `source_id`).
- UI follows the "Option C, outdoors" design: CSS variables for the night/day themes,
  touch targets >= 44px, real buttons/labels.

## Environment

- Deploy target: `docker-host` VM, 192.168.50.85, AMD64, alongside other containers.
- Proxmox host: 192.168.50.115 (read-only API token `claude@pve!readonly`, secret kept on the
  VM in `~/.config/proxmox/env`, never in the repo).
- Work board schedule: 7:00–3:30 (sometimes 4:00). Time zone: America/Chicago (US Central).
