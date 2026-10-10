import { useEffect, useState } from 'preact/hooks';
import { api } from './api.js';

function CopyField({ label, value }) {
  const [copied, setCopied] = useState(false);
  const id = 'copy-' + label.replace(/\W+/g, '-').toLowerCase();
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      document.getElementById(id)?.select();
    }
  };
  return (
    <div class="copy-field">
      <label for={id} class="small muted">{label}</label>
      <div class="add">
        <input id={id} type="text" value={value} readOnly onFocus={(e) => e.currentTarget.select()} />
        <button type="button" class="btn" onClick={copy}>{copied ? 'Copied' : 'Copy'}</button>
      </div>
    </div>
  );
}

function when(ts) {
  if (!ts) return 'never';
  return new Date(ts).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

export function Settings({ boards, onBack }) {
  const mcpURL = `${location.origin}/mcp`;
  return (
    <div class="desk">
      <section class="hello">
        <p class="eyebrow">Settings</p>
        <h1>Connect Claude</h1>
      </section>
      <div class="tiles settings">
        <section class="tile" aria-labelledby="connect-h">
          <header class="tile-head">
            <h2 id="connect-h">MCP server</h2>
          </header>
          <CopyField label="MCP server URL" value={mcpURL} />
          <p class="small">
            <strong>claude.ai or a cloud routine:</strong> Settings → Connectors → Add custom connector, paste the URL,
            then approve it here and pick its boards. No token needed.
          </p>
          <p class="small">
            <strong>Claude Code:</strong> create an API token below, then run
          </p>
          <pre class="cmd">claude mcp add --transport http workdesk {mcpURL} --header "Authorization: Bearer wdk_…"</pre>
        </section>
        <Connections boards={boards} />
        {boards.map((b) => (
          <Tokens key={b.id} board={b} />
        ))}
      </div>
      <p>
        <button type="button" class="btn ghost" onClick={onBack}>← Back to board</button>
      </p>
    </div>
  );
}

function Connections({ boards }) {
  const [grants, setGrants] = useState(null);
  const [err, setErr] = useState('');
  const load = () => api.connections().then(setGrants, (e) => setErr(e.message));
  useEffect(() => {
    load();
  }, []);
  const names = (ids) => ids.map((id) => boards.find((b) => b.id === id)?.name || `#${id}`).join(', ');
  const disconnect = async (g) => {
    if (!confirm(`Disconnect ${g.client_name}? It will lose access right away.`)) return;
    try {
      await api.disconnect(g.id);
      load();
    } catch (e) {
      setErr(e.message);
    }
  };
  return (
    <section class="tile" aria-labelledby="apps-h">
      <header class="tile-head">
        <h2 id="apps-h">Connected apps</h2>
      </header>
      {err && <p class="error small" role="alert">{err}</p>}
      {grants === null ? (
        <p class="muted">Loading…</p>
      ) : grants.length === 0 ? (
        <p class="muted">No connectors yet. Add the MCP URL as a connector in Claude to see it here.</p>
      ) : (
        <ul class="items">
          {grants.map((g) => (
            <li class="item row-item">
              <div class="row-main">
                <span class="item-title">{g.client_name}</span>
                <span class="small muted">{names(g.board_ids)} · last used {when(g.last_used_at)}</span>
              </div>
              <button type="button" class="btn ghost danger" onClick={() => disconnect(g)}>Disconnect</button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Tokens({ board }) {
  const [tokens, setTokens] = useState(null);
  const [name, setName] = useState('');
  const [fresh, setFresh] = useState(null);
  const [err, setErr] = useState('');
  const load = () => api.tokens(board.id).then(setTokens, (e) => setErr(e.message));
  useEffect(() => {
    load();
  }, [board.id]);

  const create = async (e) => {
    e.preventDefault();
    setErr('');
    try {
      const res = await api.createToken(board.id, name.trim());
      setFresh(res.secret);
      setName('');
      load();
    } catch (e) {
      setErr(e.message);
    }
  };
  const revoke = async (t) => {
    if (!confirm(`Revoke "${t.name}"? Anything using it stops working.`)) return;
    try {
      await api.deleteToken(t.id);
      load();
    } catch (e) {
      setErr(e.message);
    }
  };

  const inputId = `token-name-${board.id}`;
  return (
    <section class="tile" aria-labelledby={`tokens-h-${board.id}`}>
      <header class="tile-head">
        <h2 id={`tokens-h-${board.id}`}>API tokens · {board.name}</h2>
      </header>
      <form class="add" onSubmit={create}>
        <label for={inputId} class="visually-hidden">Token name</label>
        <input id={inputId} type="text" placeholder="Name, e.g. Morning brief" value={name} maxLength={100}
          onInput={(e) => setName(e.currentTarget.value)} />
        <button type="submit" class="btn primary" disabled={!name.trim()}>Create</button>
      </form>
      {err && <p class="error small" role="alert">{err}</p>}
      {fresh && (
        <div class="fresh" role="status">
          <CopyField label="New token (shown once; copy it now)" value={fresh} />
          <button type="button" class="btn ghost" onClick={() => setFresh(null)}>Done</button>
        </div>
      )}
      {tokens && tokens.length > 0 && (
        <ul class="items">
          {tokens.map((t) => (
            <li class="item row-item">
              <div class="row-main">
                <span class="item-title">{t.name}</span>
                <span class="small muted"><code>{t.prefix}…</code> · last used {when(t.last_used_at)}</span>
              </div>
              <button type="button" class="btn ghost danger" onClick={() => revoke(t)}>Revoke</button>
            </li>
          ))}
        </ul>
      )}
      {tokens && tokens.length === 0 && <p class="muted small">No tokens for this board.</p>}
    </section>
  );
}
