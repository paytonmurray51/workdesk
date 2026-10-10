# Sample routine: Work morning brief

A prompt for a scheduled Claude routine (weekdays, about 6:30 America/Chicago) that reads
email, calendar and Jira through Claude's own connectors and posts the results to Workdesk.
Workdesk never sees those accounts; it only receives what Claude posts.

## Setup

1. Connect Workdesk so the routine can reach it, either:
   - **Connector (claude.ai / cloud routines):** Settings → Connectors → Add custom connector,
     URL `https://workdesk.voodootechlabs.com/mcp`. Approve it on the Workdesk page that opens
     and tick the **Work** board.
   - **Claude Code:** in Workdesk, Settings → API tokens · Work → Create. Then
     `claude mcp add --transport http workdesk https://workdesk.voodootechlabs.com/mcp --header "Authorization: Bearer wdk_…"`
2. Make sure the routine also has your Gmail, Google Calendar and Jira connectors.
3. Create the routine with the prompt below.

## Prompt

```text
You prepare my Work morning brief in Workdesk. Use the workdesk MCP tools to post; use the
Gmail, Google Calendar and Jira connectors only to read. Never send email, change calendar
events or edit Jira issues.

1. Call workdesk list_boards and use the "Work" board. Use its "today" date (America/Chicago).

2. Calendar: get today's events on my primary calendar. Call set_widget with name "calendar"
   and payload {"date": <today>, "events": [{"title", "start" (HH:MM), "end" (HH:MM),
   "location", "url", "id"}]}, in start order. Leave out declined events.

3. Jira: find issues assigned to me that are not Done, plus anything I was mentioned in during
   the last day. Call set_widget with name "jira" and payload {"issues": [{"key", "summary",
   "status", "priority", "due", "url"}]}.

4. Email: look at unread mail in my inbox from the last 24 hours (skip newsletters,
   notifications and promotions). For each email that needs action from me, call push_inbox
   with type "email", title (subject), snippet (one sentence, no quotes of sensitive content),
   url (the Gmail link), reason (why it needs me) and source_id (the Gmail message id).

5. To-dos: call list_open_items for the Work "Today" list. Then call add_items on that list for
   today's concrete tasks: Jira issues due today or marked highest priority, prep for meetings
   that need it, and replies that can't wait. Set source_type ("jira", "email", "calendar"),
   source_id (Jira key, Gmail message id or event id) and source_url on every item so re-runs
   update instead of duplicating. Don't re-add things already open.

6. Timeboxes: my workday is 07:00–15:30 (sometimes to 16:00), with a lunch hour and 15 minutes
   of buffer after meetings; the last 15 minutes is wrap-up. Call propose_timeboxes for today
   with meetings (kind "meeting") at their real times, breaks (kind "break"), and focus blocks
   (kind "task") for the to-dos, with item_ids linking the to-do ids from step 5. These are
   proposals; I accept them on the dashboard.

7. Briefing: call post_briefing on Work with source_id "brief-<today>" and Markdown like:

   ## Today at a glance
   - 3 meetings (first at 7:15 Standup), 2 Jira issues due, 4 emails need replies
   ## Do first
   - …the 1–3 most important things and why
   ## Meetings
   - 7:15–7:30 Standup — [link](…)
   ## Waiting on you
   - …emails and Jira mentions, each with a link
   ## Heads-up
   - …anything unusual (deadlines this week, conflicts, out-of-office replies)

   Keep it under 250 words. Include stats {"meetings": n, "emails": n, "jira_due": n}.

8. Finish with a one-line summary of what you posted. If any connector fails, still post what
   you have and say what was missing in the Heads-up section.
```

Re-running the routine the same day is safe: items, inbox entries and the briefing are
keyed by `source_id`, widgets and timeboxes are replaced.
