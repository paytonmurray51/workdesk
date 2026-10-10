import { useEffect, useState, useCallback } from 'preact/hooks';
import { api, ApiError } from './api.js';
import { loadThemePref, saveThemePref, resolveTheme, applyTheme, nextPref } from './theme.js';
import { Markdown } from './markdown.jsx';
import { Settings } from './settings.jsx';

const BOARD_KEY = 'workdesk.board';

export function App() {
  const [state, setState] = useState({ phase: 'loading' });
  const [themePref, setThemePref] = useState(loadThemePref);

  useEffect(() => {
    const update = () => applyTheme(resolveTheme(themePref));
    update();
    const mq = window.matchMedia('(prefers-color-scheme: light)');
    mq.addEventListener('change', update);
    return () => mq.removeEventListener('change', update);
  }, [themePref]);

  const cycleTheme = () => {
    const v = nextPref[themePref];
    saveThemePref(v);
    setThemePref(v);
  };

  useEffect(() => {
    (async () => {
      try {
        const [me, boards] = await Promise.all([api.me(), api.boards()]);
        setState({ phase: 'ready', me, boards });
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) setState({ phase: 'signedOut' });
        else if (e instanceof ApiError && e.status === 403) setState({ phase: 'notInvited' });
        else setState({ phase: 'error', message: e.message });
      }
    })();
  }, []);

  const themeButton = <ThemeButton pref={themePref} onClick={cycleTheme} />;

  if (state.phase === 'loading') return <Shell themeButton={themeButton}><p class="muted center">Loading…</p></Shell>;
  if (state.phase === 'signedOut') return <Shell themeButton={themeButton}><SignIn /></Shell>;
  if (state.phase === 'notInvited')
    return (
      <Shell themeButton={themeButton}>
        <SignIn message="This account isn't invited yet. Sign in with an invited Google account." />
      </Shell>
    );
  if (state.phase === 'error')
    return <Shell themeButton={themeButton}><p class="center">Couldn't load Workdesk: {state.message}</p></Shell>;

  return <Desk me={state.me} boards={state.boards} themeButton={themeButton} />;
}

function Shell({ children, themeButton, nav, user }) {
  return (
    <div class="page">
      <header class="topbar">
        <div class="brand">
          <Peaks />
          <span>Workdesk</span>
        </div>
        {nav}
        <div class="topbar-actions">
          {themeButton}
          {user}
        </div>
      </header>
      <main>{children}</main>
    </div>
  );
}

function ThemeButton({ pref, onClick }) {
  const label = { auto: 'Theme: auto', day: 'Theme: trail day', night: 'Theme: forest night' }[pref];
  const icon = { auto: '◐', day: '☀', night: '☾' }[pref];
  return (
    <button type="button" class="btn icon-btn" onClick={onClick} aria-label={label} title={label}>
      <span aria-hidden="true">{icon}</span>
    </button>
  );
}

function SignIn({ message }) {
  return (
    <section class="tile signin">
      <Peaks large />
      <h1>Good to see you.</h1>
      <p class="muted">{message || 'Sign in to see your day: brief, to-dos and progress.'}</p>
      <a class="btn primary" href="/auth/login">Sign in with Google</a>
    </section>
  );
}

function Desk({ me, boards, themeButton }) {
  const [boardId, setBoardId] = useState(() => {
    let saved;
    try {
      saved = Number(localStorage.getItem(BOARD_KEY));
    } catch {}
    return boards.some((b) => b.id === saved) ? saved : boards[0]?.id;
  });
  const board = boards.find((b) => b.id === boardId);
  const [view, setView] = useState(() => (location.hash === '#settings' ? 'settings' : 'desk'));
  useEffect(() => {
    const onHash = () => setView(location.hash === '#settings' ? 'settings' : 'desk');
    addEventListener('hashchange', onHash);
    return () => removeEventListener('hashchange', onHash);
  }, []);
  const go = (v) => {
    location.hash = v === 'settings' ? 'settings' : '';
    setView(v);
  };

  const pick = (id) => {
    setBoardId(id);
    if (view !== 'desk') go('desk');
    try {
      localStorage.setItem(BOARD_KEY, String(id));
    } catch {}
  };

  const signOut = async () => {
    await api.logout().catch(() => {});
    location.href = '/';
  };

  const nav = (
    <nav class="boards" aria-label="Boards">
      {boards.map((b) => (
        <button
          type="button"
          class={'btn board-tab' + (view === 'desk' && b.id === boardId ? ' active' : '')}
          aria-pressed={view === 'desk' && b.id === boardId}
          onClick={() => pick(b.id)}
          style={{ '--board': b.color || 'var(--accent)' }}
        >
          {b.name}
        </button>
      ))}
    </nav>
  );
  const user = (
    <>
      <button type="button" class={'btn ghost' + (view === 'settings' ? ' active-ghost' : '')}
        aria-pressed={view === 'settings'} onClick={() => go(view === 'settings' ? 'desk' : 'settings')}>
        Settings
      </button>
      <button type="button" class="btn ghost" onClick={signOut} title={me.email}>
        Sign out
      </button>
    </>
  );

  return (
    <Shell themeButton={themeButton} nav={nav} user={user}>
      {view === 'settings' ? (
        <Settings boards={boards} onBack={() => go('desk')} />
      ) : board ? (
        <Board key={board.id} board={board} me={me} />
      ) : (
        <p class="center muted">No boards yet.</p>
      )}
    </Shell>
  );
}

function Board({ board, me }) {
  const [today, setToday] = useState(null);
  const [items, setItems] = useState(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const lists = await api.lists(board.id);
      const t = lists.find((l) => l.name === 'Today') || lists[0];
      setToday(t || null);
      setItems(t ? await api.items(t.id) : []);
    } catch (e) {
      setError(e.message);
    }
  }, [board.id]);

  useEffect(() => {
    load();
  }, [load]);

  const add = async (title) => {
    const [it] = await api.addItem(today.id, title);
    setItems((xs) => [...xs, it]);
  };

  const toggle = async (item) => {
    const done = !item.done;
    setItems((xs) => xs.map((x) => (x.id === item.id ? { ...x, done } : x)));
    try {
      const updated = await api.setDone(item.id, done);
      setItems((xs) => xs.map((x) => (x.id === item.id ? updated : x)));
    } catch (e) {
      setItems((xs) => xs.map((x) => (x.id === item.id ? item : x)));
      setError(e.message);
    }
  };

  const done = items ? items.filter((i) => i.done).length : 0;
  const total = items ? items.length : 0;

  return (
    <div class="desk">
      <Hello board={board} me={me} />
      {error && (
        <p class="tile error" role="alert">
          {error}{' '}
          <button type="button" class="btn ghost" onClick={() => setError('')}>Dismiss</button>
        </p>
      )}
      <div class="tiles">
        <BriefTile board={board} />
        <ProgressTile done={done} total={total} loading={items === null} />
        <TodayTile list={today} items={items} onAdd={add} onToggle={toggle} />
      </div>
    </div>
  );
}

function Hello({ board, me }) {
  const now = new Date();
  const tz = board.timezone || undefined;
  const hour = Number(new Intl.DateTimeFormat('en-US', { hour: 'numeric', hourCycle: 'h23', timeZone: tz }).format(now));
  const part = hour < 12 ? 'morning' : hour < 17 ? 'afternoon' : 'evening';
  const first = (me.name || '').split(' ')[0];
  const date = new Intl.DateTimeFormat('en-US', { weekday: 'long', month: 'long', day: 'numeric', timeZone: tz }).format(now);
  return (
    <section class="hello">
      <p class="eyebrow">{date} · {board.name}</p>
      <h1>
        Good {part}{first ? `, ${first}` : ''}.
      </h1>
    </section>
  );
}

function BriefTile({ board }) {
  const [brief, setBrief] = useState(undefined);
  useEffect(() => {
    api.latestBriefing(board.id).then(setBrief, () => setBrief(null));
  }, [board.id]);

  const tz = board.timezone || undefined;
  const day = (d) => new Intl.DateTimeFormat('en-CA', { timeZone: tz }).format(d);
  const fresh = brief && day(new Date(brief.created_at)) === day(new Date());
  const posted = brief && new Date(brief.updated_at).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit', timeZone: tz });

  return (
    <section class="tile brief" aria-labelledby="brief-h">
      <header class="tile-head">
        <h2 id="brief-h">Morning brief</h2>
        {brief === undefined ? null : brief ? (
          <span class={'chip' + (fresh ? '' : ' warn')}>{fresh ? `posted ${posted}` : 'from an earlier day'}</span>
        ) : (
          <span class="chip">waiting</span>
        )}
      </header>
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

function ProgressTile({ done, total, loading }) {
  const pct = total ? Math.round((done / total) * 100) : 0;
  const r = 42;
  const c = 2 * Math.PI * r;
  return (
    <section class="tile progress" aria-labelledby="progress-h">
      <header class="tile-head">
        <h2 id="progress-h">Progress</h2>
      </header>
      <div class="progress-body">
        <svg viewBox="0 0 100 100" class="ring" role="img" aria-label={`${pct}% of today done`}>
          <circle cx="50" cy="50" r={r} class="ring-track" />
          <circle
            cx="50" cy="50" r={r} class="ring-fill"
            stroke-dasharray={`${(pct / 100) * c} ${c}`}
            transform="rotate(-90 50 50)"
          />
          <text x="50" y="50" class="ring-text" text-anchor="middle" dominant-baseline="central">{pct}%</text>
        </svg>
        <div>
          <p class="big">{loading ? '…' : `${done} of ${total}`}</p>
          <p class="muted">
            {loading ? 'Loading' : total === 0 ? 'Nothing planned yet' : done === total ? 'Summit reached. Nice.' : 'done today'}
          </p>
        </div>
      </div>
    </section>
  );
}

function TodayTile({ list, items, onAdd, onToggle }) {
  const [title, setTitle] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  const submit = async (e) => {
    e.preventDefault();
    const t = title.trim();
    if (!t || !list) return;
    setBusy(true);
    setErr('');
    try {
      await onAdd(t);
      setTitle('');
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  const open = (items || []).filter((i) => !i.done);
  const done = (items || []).filter((i) => i.done);

  return (
    <section class="tile today" aria-labelledby="today-h">
      <header class="tile-head">
        <h2 id="today-h">{list ? list.name : 'Today'}</h2>
        {items && <span class="chip">{open.length} open</span>}
      </header>

      <form class="add" onSubmit={submit}>
        <label for="new-item" class="visually-hidden">New to-do</label>
        <input
          id="new-item"
          type="text"
          placeholder="Add a to-do…"
          value={title}
          maxLength={500}
          autocomplete="off"
          onInput={(e) => setTitle(e.currentTarget.value)}
          disabled={!list}
        />
        <button type="submit" class="btn primary" disabled={busy || !title.trim() || !list}>Add</button>
      </form>
      {err && <p class="error small" role="alert">{err}</p>}

      {items === null ? (
        <p class="muted">Loading…</p>
      ) : items.length === 0 ? (
        <p class="muted empty">Clear trail ahead. Add the first thing you want to get done.</p>
      ) : (
        <ul class="items">
          {[...open, ...done].map((it) => (
            <Item key={it.id} item={it} onToggle={onToggle} />
          ))}
        </ul>
      )}
    </section>
  );
}

function Item({ item, onToggle }) {
  const id = `item-${item.id}`;
  return (
    <li class={'item' + (item.done ? ' done' : '')}>
      <input id={id} type="checkbox" checked={item.done} onChange={() => onToggle(item)} />
      <label for={id}>
        <span class="item-title">{item.title}</span>
        {item.carried_days > 0 && <span class="chip warn">carried {item.carried_days}d</span>}
      </label>
      {item.source_url && (
        <a class="btn ghost source" href={item.source_url} target="_blank" rel="noopener noreferrer" aria-label={`Open source for ${item.title}`}>
          ↗
        </a>
      )}
    </li>
  );
}

function Peaks({ large }) {
  return (
    <svg class={'peaks' + (large ? ' large' : '')} viewBox="0 0 32 24" aria-hidden="true">
      <path d="M1 22 11 6l5 8 4-5 11 13z" class="peaks-hill" />
      <circle cx="24" cy="5" r="3" class="peaks-sun" />
    </svg>
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
