# workdesk

Self-hosted dashboard for daily briefings, to-do lists and timeboxes. Claude routines gather
email, Jira and calendar through their own connectors and post the results here through a
REST API / MCP server. People sign in with Google, check things off, and share boards.

- Build plan: [docs/PLAN.md](docs/PLAN.md)
- Runs as one small container on `docker-host` (192.168.50.85), public at
  `workdesk.voodootechlabs.com` via Cloudflare Tunnel.
