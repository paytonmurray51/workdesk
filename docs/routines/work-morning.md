# Routine: Work morning (Outlook + Jira)

The routine Payton runs on the Work board, weekdays around 6:30 America/Chicago. It reads
Outlook (mail and calendar) and Jira through Claude's own connectors and posts to Workdesk.
Workdesk never sees those accounts. Setup is the same as in `morning-brief.md`, with the
Microsoft 365 / Outlook and Atlassian connectors instead of Gmail and Google Calendar.

## Prompt

```text
Use the workdesk MCP to set up my Work day. Read Outlook (mail + calendar) and Jira with their
connectors, read-only: never send or reply to email, change calendar events, or edit or comment
on Jira issues.

1. Board: call workdesk list_boards, use the "Work" board and its "today" date
   (America/Chicago). If today is Saturday or Sunday, only do steps 3, 4 and 7.

2. Calendar: get today's Outlook calendar events (skip declined and cancelled ones). Call
   set_widget name "calendar", payload exactly:
   {"date": "<today>", "events": [{"title": "...", "start": "HH:MM", "end": "HH:MM",
     "location": "...", "url": "<Outlook/Teams link>", "id": "<event id>"}]}
   Times are 24h, America/Chicago, sorted by start.

3. Jira sprint: find issues in the active sprint assigned to me that aren't Done. Call
   set_widget name "jira", payload exactly:
   {"issues": [{"key": "ABC-123", "summary": "...", "status": "In Progress",
     "priority": "High", "due": "YYYY-MM-DD", "url": "https://<site>/browse/ABC-123"}]}
   Only key and summary are required; leave out fields that are empty. If set_widget returns
   an error, fix the payload as the error says and call it again.

4. Unanswered Jira comments: for my sprint issues AND every backlog issue I'm assigned to,
   reported, watching or @mentioned in, read the comments. A comment needs me when someone
   else wrote it after my last comment on that issue and it asks me something, mentions me or
   is waiting on my input. For each one call push_inbox with type "jira", title
   "<KEY>: reply to <author>", snippet (one sentence on what they need), url (the issue link,
   with the comment anchor if you have it), reason ("unanswered comment in sprint" or
   "... in backlog") and source_id "<KEY>-comment-<comment id>".

5. Email: review Outlook inbox mail from the last 24 hours (since Friday morning on Mondays).
   Skip newsletters, automated notifications, promotions and anything I've already replied to.
   For each email that needs action from me call push_inbox with type "email", title (subject),
   snippet (one sentence, no sensitive details), url (Outlook web link), reason and source_id
   (the message id). Keep a short summary of the rest for the briefing.

6. To-dos: call list_open_items for the Work "Today" list (items carried over from earlier days
   are already there; don't re-add them). Then call add_items on "Today" for today's concrete
   work: sprint issues due soon or highest priority, Jira comments from step 4 to answer,
   emails that need a reply, and prep for meetings that need it. Set source_type ("jira",
   "email", "calendar"), source_id (Jira key, "<KEY>-comment-<id>", message id or event id)
   and source_url on every item. Keep titles short and start with a verb.

7. Timebox: call propose_timeboxes for today (workdays only). My day is 07:00–15:30
   (sometimes to 16:00). Put meetings (kind "meeting") at their real times, a lunch break
   (kind "break", around 11:30–12:30), 10–15 minutes of buffer after long meetings, a
   15:15–15:30 wrap-up, and focus blocks (kind "task") of 30–90 minutes for the to-dos from
   step 6, with item_ids set to those to-do ids. Fit carried-over items first. Blocks must not
   overlap; times are 24h "HH:MM". If it returns an error, fix what it says and retry. These are
   proposals; I accept them on the dashboard.

8. Briefing: call post_briefing on Work with source_id "brief-<today>" and Markdown:
   ## Today at a glance
   - n meetings (first at …), n sprint issues open, n Jira comments and n emails need me
   ## Do first
   - the 1–3 most important things and why
   ## Jira comments to answer
   - [KEY](link): who, what they need (sprint first, then backlog)
   ## Email summary
   - needs reply: …; FYI: … (one line each, linked)
   ## Heads-up
   - deadlines this week, conflicts, out-of-office, anything a connector couldn't read
   Under 300 words. stats: {"meetings": n, "emails": n, "jira_open": n, "jira_comments": n,
   "todos": n}.

9. Reply with: the to-do list you set, a 3-line summary, and the timebox plan. If a connector
   fails, still post everything else and say what was missing.
```

Re-running the same day is safe: items, inbox entries and the briefing upsert by
`source_id`; widgets and the day's timeboxes are replaced (re-proposing resets the plan to
"proposed").
