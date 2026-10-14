import { useEffect, useState, useCallback } from 'preact/hooks';
import { api, ApiError } from './api.js';
import { loadThemePref, saveThemePref, resolveTheme, applyTheme, nextPref } from './theme.js';
import { BriefTile, ProgressTile, ListTile, CalendarTile, JiraTile, InboxTile, DayTile, boardNow } from './tiles.jsx';
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

const REFRESH_MS = 60_000;

function Board({ board, me }) {
  const [lists, setLists] = useState(null);
  const [items, setItems] = useState({}); // list id -> items
  const [brief, setBrief] = useState(undefined);
  const [widgets, setWidgets] = useState([]);
  const [inbox, setInbox] = useState(null);
  const [boxes, setBoxes] = useState(undefined);
  const [error, setError] = useState('');

  // Loads everything for the board. Claude posts in the background, so this runs on a
  // timer and when the tab comes back into view.
  const load = useCallback(async () => {
    try {
      const [ls, br, ws, ib] = await Promise.all([
        api.lists(board.id),
        api.latestBriefing(board.id).catch((e) => (e.status === 404 ? null : Promise.reject(e))),
        api.widgets(board.id),
        api.inbox(board.id),
      ]);
      const its = await Promise.all(ls.map((l) => api.items(l.id)));
      if (board.has_schedule) setBoxes(await api.timeboxes(board.id, boardNow(board.timezone || undefined).date));
      setLists(ls);
      setItems(Object.fromEntries(ls.map((l, i) => [l.id, its[i]])));
      setBrief(br);
      setWidgets(ws);
      setInbox(ib);
    } catch (e) {
      setError(e.message);
    }
  }, [board.id, board.has_schedule, board.timezone]);

  useEffect(() => {
    load();
    const t = setInterval(() => document.visibilityState === 'visible' && load(), REFRESH_MS);
    const onVis = () => document.visibilityState === 'visible' && load();
    document.addEventListener('visibilitychange', onVis);
    return () => {
      clearInterval(t);
      document.removeEventListener('visibilitychange', onVis);
    };
  }, [load]);

  const setListItems = (listId, fn) => setItems((m) => ({ ...m, [listId]: fn(m[listId] || []) }));

  const add = async (list, title) => {
    const [it] = await api.addItem(list.id, title);
    setListItems(list.id, (xs) => [...xs, it]);
  };

  const toggle = async (item) => {
    const done = !item.done;
    setListItems(item.list_id, (xs) => xs.map((x) => (x.id === item.id ? { ...x, done } : x)));
    try {
      const updated = await api.setDone(item.id, done);
      setListItems(item.list_id, (xs) => xs.map((x) => (x.id === item.id ? updated : x)));
    } catch (e) {
      setListItems(item.list_id, (xs) => xs.map((x) => (x.id === item.id ? item : x)));
      setError(e.message);
    }
  };

  // Removing (×) drops the item without counting it as done; a routine won't add it back.
  const remove = async (item) => {
    setListItems(item.list_id, (xs) => xs.filter((x) => x.id !== item.id));
    try {
      await api.deleteItem(item.id);
    } catch (e) {
      if (e.status !== 404) {
        setListItems(item.list_id, (xs) => [...xs, item].sort((a, b) => a.position - b.position));
        setError(e.message);
      }
    }
  };

  // Triage removes the entry right away and restores it if the server says no.
  const triage = (fn) => async (entry) => {
    setInbox((xs) => xs.filter((x) => x.id !== entry.id));
    try {
      const res = await fn(entry);
      if (res && res.list_id) setListItems(res.list_id, (xs) => (xs.some((x) => x.id === res.id) ? xs : [...xs, res]));
    } catch (e) {
      setInbox((xs) => [entry, ...xs]);
      setError(e.message);
    }
  };

  const today = lists?.find((l) => l.name === 'Today') || lists?.[0];
  const others = (lists || []).filter((l) => l !== today);
  const todayItems = today ? items[today.id] : undefined;
  const done = todayItems ? todayItems.filter((i) => i.done).length : 0;
  const total = todayItems ? todayItems.length : 0;
  const carried = todayItems ? todayItems.filter((i) => !i.done && i.carried_days > 0).length : 0;
  const itemsById = Object.fromEntries(Object.values(items).flat().map((i) => [i.id, i]));
  const acceptPlan = async (accepted) => {
    try {
      setBoxes(await api.acceptTimeboxes(board.id, boardNow(board.timezone || undefined).date, accepted));
    } catch (e) {
      setError(e.message);
    }
  };
  const calendar = widgets.find((w) => w.name === 'calendar');
  const jira = widgets.find((w) => w.name === 'jira');

  return (
    <div class="desk">
      <Hello board={board} me={me} />
      {error && (
        <p class="tile error" role="alert">
          {error}{' '}
          <button type="button" class="btn ghost" onClick={() => setError('')}>Dismiss</button>
        </p>
      )}
      <div class="tiles grid">
        <BriefTile board={board} brief={brief} />
        <ProgressTile done={done} total={total} loading={lists === null} inboxCount={inbox?.length || 0} carried={carried} />
        {today && <ListTile list={today} items={todayItems} onAdd={add} onToggle={toggle} onRemove={remove} wide />}
        {calendar && <CalendarTile widget={calendar} board={board} />}
        {board.has_schedule && (
          <DayTile board={board} boxes={boxes} itemsById={itemsById} onAccept={acceptPlan} onToggle={toggle} />
        )}
        {(inbox === null || inbox.length > 0 || brief) && (
          <InboxTile
            items={inbox}
            onAdd={triage((e) => api.inboxAdd(e.id))}
            onSnooze={triage((e) => api.inboxStatus(e.id, 'snoozed'))}
            onDismiss={triage((e) => api.inboxStatus(e.id, 'dismissed'))}
          />
        )}
        {jira && <JiraTile widget={jira} board={board} />}
        {others.map((l) => (
          <ListTile key={l.id} list={l} items={items[l.id]} onAdd={add} onToggle={toggle} onRemove={remove} />
        ))}
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

function Peaks({ large }) {
  return (
    <svg class={'peaks' + (large ? ' large' : '')} viewBox="0 0 32 24" aria-hidden="true">
      <path d="M1 22 11 6l5 8 4-5 11 13z" class="peaks-hill" />
      <circle cx="24" cy="5" r="3" class="peaks-sun" />
    </svg>
  );
}
