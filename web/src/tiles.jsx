import { useState } from 'preact/hooks';
import { Markdown } from './markdown.jsx';

// Current wall-clock date (YYYY-MM-DD) and time (HH:MM) in a board's time zone.
export function boardNow(tz) {
  const d = new Date();
  const date = new Intl.DateTimeFormat('en-CA', { timeZone: tz }).format(d);
  const time = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: tz }).format(d);
  return { date, time };
}

function fmtTime(hhmm) {
  const m = /^(\d{1,2}):(\d{2})/.exec(hhmm || '');
  if (!m) return hhmm || '';
  const h = Number(m[1]);
  return `${((h + 11) % 12) + 1}:${m[2]}${h < 12 ? 'a' : 'p'}`;
}

function safeURL(u) {
  return typeof u === 'string' && /^https?:\/\//i.test(u) ? u : null;
}

function TileHead({ id, title, children }) {
  return (
    <header class="tile-head">
      <h2 id={id}>{title}</h2>
      {children}
    </header>
  );
}

// Morning brief

export function BriefTile({ board, brief }) {
  const tz = board.timezone || undefined;
  const day = (d) => new Intl.DateTimeFormat('en-CA', { timeZone: tz }).format(d);
  const fresh = brief && day(new Date(brief.created_at)) === day(new Date());
  const posted = brief && new Date(brief.updated_at).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit', timeZone: tz });
  const stats = brief && brief.stats && typeof brief.stats === 'object' ? Object.entries(brief.stats).filter(([, v]) => typeof v === 'number') : [];

  return (
    <section class="tile brief span-8" aria-labelledby="brief-h">
      <TileHead id="brief-h" title="Morning brief">
        {brief === undefined ? null : brief ? (
          <span class={'chip' + (fresh ? '' : ' warn')}>{fresh ? `posted ${posted}` : 'from an earlier day'}</span>
        ) : (
          <span class="chip">waiting</span>
        )}
      </TileHead>
      {stats.length > 0 && (
        <ul class="stats" aria-label="At a glance">
          {stats.map(([k, v]) => (
            <li>
              <span class="stat-n">{v}</span>
              <span class="stat-k">{k.replace(/_/g, ' ')}</span>
            </li>
          ))}
        </ul>
      )}
      {brief ? (
        <Markdown text={brief.markdown} />
      ) : (
        <div class="brief-empty">
          <Trail />
          <p>{brief === undefined ? 'Loading…' : 'No brief yet today.'}</p>
          <p class="muted small">Your morning Claude routine will post a summary of email, calendar and Jira here.</p>
        </div>
      )}
    </section>
  );
}

// Progress

export function ProgressTile({ done, total, loading, inboxCount, carried }) {
  const pct = total ? Math.round((done / total) * 100) : 0;
  const r = 42;
  const c = 2 * Math.PI * r;
  return (
    <section class="tile progress span-4" aria-labelledby="progress-h">
      <TileHead id="progress-h" title="Progress" />
      <div class="progress-body">
        <svg viewBox="0 0 100 100" class="ring" role="img" aria-label={`${pct}% of today done`}>
          <circle cx="50" cy="50" r={r} class="ring-track" />
          <circle cx="50" cy="50" r={r} class="ring-fill" stroke-dasharray={`${(pct / 100) * c} ${c}`} transform="rotate(-90 50 50)" />
          <text x="50" y="50" class="ring-text" text-anchor="middle" dominant-baseline="central">{pct}%</text>
        </svg>
        <div>
          <p class="big">{loading ? '…' : `${done} of ${total}`}</p>
          <p class="muted">
            {loading ? 'Loading' : total === 0 ? 'Nothing planned yet' : done === total ? 'Summit reached. Nice.' : 'done today'}
          </p>
        </div>
      </div>
      {(inboxCount > 0 || carried > 0) && (
        <ul class="progress-notes small">
          {inboxCount > 0 && <li>{inboxCount} in the inbox to triage</li>}
          {carried > 0 && <li class="warn-text">{carried} carried over from earlier days</li>}
        </ul>
      )}
    </section>
  );
}

// Lists

export function ListTile({ list, items, onAdd, onToggle, wide }) {
  const [title, setTitle] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const hid = `list-h-${list.id}`;
  const inputId = `new-item-${list.id}`;

  const submit = async (e) => {
    e.preventDefault();
    const t = title.trim();
    if (!t) return;
    setBusy(true);
    setErr('');
    try {
      await onAdd(list, t);
      setTitle('');
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  const open = (items || []).filter((i) => !i.done);
  const done = (items || []).filter((i) => i.done);
  // Group open items by section, keeping first-seen order; unsectioned first.
  const groups = [];
  for (const it of open) {
    const key = it.section || '';
    let g = groups.find((x) => x.key === key);
    if (!g) groups.push((g = { key, items: [] }));
    g.items.push(it);
  }
  groups.sort((a, b) => (a.key === '' ? -1 : b.key === '' ? 1 : 0));

  return (
    <section class={'tile list-tile ' + (wide ? 'span-8' : 'span-4')} aria-labelledby={hid}>
      <TileHead id={hid} title={list.name}>{items && <span class="chip">{open.length} open</span>}</TileHead>
      <form class="add" onSubmit={submit}>
        <label for={inputId} class="visually-hidden">New item on {list.name}</label>
        <input id={inputId} type="text" placeholder="Add a to-do…" value={title} maxLength={500} autocomplete="off"
          onInput={(e) => setTitle(e.currentTarget.value)} />
        <button type="submit" class="btn primary" disabled={busy || !title.trim()}>Add</button>
      </form>
      {err && <p class="error small" role="alert">{err}</p>}
      {items === undefined ? (
        <p class="muted">Loading…</p>
      ) : items.length === 0 ? (
        <p class="muted empty">Clear trail ahead. Add the first thing you want to get done.</p>
      ) : (
        <>
          {groups.map((g) => (
            <div class="group">
              {g.key && <h3 class="section">{g.key}</h3>}
              <ul class="items">
                {g.items.map((it) => <Item key={it.id} item={it} onToggle={onToggle} />)}
              </ul>
            </div>
          ))}
          {done.length > 0 && (
            <ul class="items done-list" aria-label="Done today">
              {done.map((it) => <Item key={it.id} item={it} onToggle={onToggle} />)}
            </ul>
          )}
        </>
      )}
    </section>
  );
}

function Item({ item, onToggle }) {
  const id = `item-${item.id}`;
  const url = safeURL(item.source_url);
  return (
    <li class={'item' + (item.done ? ' done' : '')}>
      <input id={id} type="checkbox" checked={item.done} onChange={() => onToggle(item)} />
      <label for={id}>
        <span class="item-title">{item.title}</span>
        {item.source_type && <span class="chip">{item.source_type}</span>}
        {item.carried_days > 0 && <span class="chip warn">carried {item.carried_days}d</span>}
      </label>
      {url && (
        <a class="btn ghost source" href={url} target="_blank" rel="noopener noreferrer" aria-label={`Open source for ${item.title}`}>↗</a>
      )}
    </li>
  );
}

// Calendar widget: payload {date, events: [{title, start, end, location, url}]}

export function CalendarTile({ widget, board }) {
  const p = widget.payload || {};
  const events = Array.isArray(p.events) ? [...p.events].sort((a, b) => String(a.start).localeCompare(String(b.start))) : [];
  const now = boardNow(board.timezone || undefined);
  const stale = p.date && p.date !== now.date;
  const nextIdx = stale ? -1 : events.findIndex((e) => String(e.end || e.start) > now.time);

  return (
    <section class="tile calendar span-4" aria-labelledby="cal-h">
      <TileHead id="cal-h" title="Calendar">
        <span class={'chip' + (stale ? ' warn' : '')}>{stale ? `for ${p.date}` : `${events.length} today`}</span>
      </TileHead>
      {events.length === 0 ? (
        <p class="muted">No events. Open trail all day.</p>
      ) : (
        <ol class="events">
          {events.map((e, i) => {
            const past = !stale && String(e.end || e.start) <= now.time;
            const nowOn = !stale && String(e.start) <= now.time && now.time < String(e.end || e.start);
            const url = safeURL(e.url);
            const Title = url ? 'a' : 'span';
            return (
              <li class={'event' + (past ? ' past' : '') + (nowOn ? ' now' : i === nextIdx ? ' next' : '')}>
                <span class="event-time">{fmtTime(e.start)}{e.end ? `–${fmtTime(e.end)}` : ''}</span>
                <span class="event-body">
                  <Title class="event-title" {...(url ? { href: url, target: '_blank', rel: 'noopener noreferrer' } : {})}>
                    {e.title || 'Untitled'}
                  </Title>
                  {e.location && <span class="small muted">{e.location}</span>}
                </span>
                {nowOn ? <span class="chip now-chip">now</span> : i === nextIdx ? <span class="chip">next</span> : null}
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}

// Jira widget: payload {issues: [{key, summary, status, priority, due, url}]}

const PRIORITY = { highest: 0, high: 1, medium: 2, low: 3, lowest: 4 };

export function JiraTile({ widget, board }) {
  const issues = Array.isArray(widget.payload?.issues) ? [...widget.payload.issues] : [];
  const today = boardNow(board.timezone || undefined).date;
  issues.sort((a, b) => {
    const ad = a.due || '9999', bd = b.due || '9999';
    if (ad !== bd) return ad.localeCompare(bd);
    return (PRIORITY[String(a.priority).toLowerCase()] ?? 5) - (PRIORITY[String(b.priority).toLowerCase()] ?? 5);
  });
  return (
    <section class="tile jira span-6" aria-labelledby="jira-h">
      <TileHead id="jira-h" title="Jira">
        <span class="chip">{issues.length} open</span>
      </TileHead>
      {issues.length === 0 ? (
        <p class="muted">Nothing assigned. Enjoy the view.</p>
      ) : (
        <ul class="issues">
          {issues.map((it) => {
            const url = safeURL(it.url);
            const overdue = it.due && it.due < today;
            const dueToday = it.due === today;
            const Key = url ? 'a' : 'span';
            return (
              <li class="issue">
                <Key class="issue-key" {...(url ? { href: url, target: '_blank', rel: 'noopener noreferrer' } : {})}>{it.key}</Key>
                <span class="issue-summary">{it.summary}</span>
                <span class="issue-meta">
                  {it.status && <span class="chip">{it.status}</span>}
                  {it.priority && /^(highest|high)$/i.test(it.priority) && <span class="chip warn">{it.priority}</span>}
                  {it.due && <span class={'chip' + (overdue ? ' danger-chip' : dueToday ? ' warn' : '')}>{overdue ? 'overdue ' : 'due '}{it.due}</span>}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

// Inbox triage

export function InboxTile({ items, onAdd, onSnooze, onDismiss }) {
  return (
    <section class="tile inbox span-6" aria-labelledby="inbox-h">
      <TileHead id="inbox-h" title="Inbox">
        {items && <span class="chip">{items.length} to triage</span>}
      </TileHead>
      {!items ? (
        <p class="muted">Loading…</p>
      ) : items.length === 0 ? (
        <p class="muted">All clear. Nothing waiting for a decision.</p>
      ) : (
        <ul class="triage">
          {items.map((it) => {
            const url = safeURL(it.url);
            return (
              <li class="triage-item">
                <div class="triage-main">
                  <span class="triage-title">
                    {it.type && <span class="chip">{it.type}</span>}{' '}
                    {url ? <a href={url} target="_blank" rel="noopener noreferrer">{it.title}</a> : it.title}
                  </span>
                  {it.reason && <span class="small">{it.reason}</span>}
                  {it.snippet && <span class="small muted snippet">{it.snippet}</span>}
                </div>
                <div class="triage-actions">
                  <button type="button" class="btn primary" onClick={() => onAdd(it)} aria-label={`Add "${it.title}" to Today`}>Add to Today</button>
                  <button type="button" class="btn" onClick={() => onSnooze(it)} aria-label={`Snooze "${it.title}" until tomorrow`}>Snooze</button>
                  <button type="button" class="btn ghost" onClick={() => onDismiss(it)} aria-label={`Dismiss "${it.title}"`}>Dismiss</button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function Trail() {
  return (
    <svg class="trail" viewBox="0 0 120 48" aria-hidden="true">
      <path d="M0 40 C 25 40, 30 18, 55 22 S 90 8, 120 10" class="trail-path" />
      <circle cx="118" cy="10" r="3" class="trail-end" />
    </svg>
  );
}
