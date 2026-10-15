import { useRef, useState } from 'preact/hooks';
import { api } from './api.js';

const KINDS = [
  ['bug', 'Bug'],
  ['idea', 'Idea'],
  ['other', 'Other'],
];

// Small button in the top bar that opens a dialog; the server files the text as a GitHub
// issue together with who sent it, the board/page and the running version.
export function FeedbackButton({ me, board, view }) {
  const dlg = useRef(null);
  const [kind, setKind] = useState('bug');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [sent, setSent] = useState(null);

  const open = () => {
    setErr('');
    setSent(null);
    dlg.current?.showModal();
  };
  const close = () => dlg.current?.close();

  const submit = async (e) => {
    e.preventDefault();
    if (!message.trim()) return;
    setBusy(true);
    setErr('');
    try {
      const issue = await api.feedback({
        kind,
        message,
        board: board?.name || '',
        view,
        page: location.pathname + location.hash,
      });
      setSent(issue);
      setMessage('');
      setKind('bug');
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <button type="button" class="btn ghost feedback-btn" onClick={open} aria-haspopup="dialog" title="Send feedback">
        <svg viewBox="0 0 20 20" aria-hidden="true" width="18" height="18">
          <path d="M3 4.5A1.5 1.5 0 0 1 4.5 3h11A1.5 1.5 0 0 1 17 4.5v8a1.5 1.5 0 0 1-1.5 1.5H8l-3.5 3v-3h0A1.5 1.5 0 0 1 3 12.5z"
            fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" />
        </svg>
        <span class="feedback-label">Feedback</span>
      </button>
      <dialog ref={dlg} class="tile feedback-dialog" aria-labelledby="fb-h" onClose={() => setBusy(false)}>
        {sent ? (
          <div class="feedback-done">
            <h2 id="fb-h">Thanks, got it.</h2>
            <p class="muted">
              Filed as{' '}
              <a href={sent.url} target="_blank" rel="noopener noreferrer">issue #{sent.number}</a>.
            </p>
            <div class="feedback-actions">
              <button type="button" class="btn primary" onClick={close} autofocus>Done</button>
            </div>
          </div>
        ) : (
          <form onSubmit={submit}>
            <h2 id="fb-h">Send feedback</h2>
            <fieldset class="seg" disabled={busy}>
              <legend class="visually-hidden">Type</legend>
              {KINDS.map(([v, label]) => (
                <label class={'seg-opt' + (kind === v ? ' on' : '')}>
                  <input type="radio" name="fb-kind" value={v} checked={kind === v} onChange={() => setKind(v)} />
                  {label}
                </label>
              ))}
            </fieldset>
            <label for="fb-msg" class="visually-hidden">Your feedback</label>
            <textarea id="fb-msg" rows={6} maxLength={5000} required autofocus disabled={busy}
              placeholder={kind === 'bug' ? 'What happened, and what did you expect?' : 'What would make Workdesk better?'}
              value={message} onInput={(e) => setMessage(e.currentTarget.value)} />
            <p class="muted small">
              Opens a GitHub issue with your name ({me.email}), {view === 'settings' ? 'the Settings page' : `the ${board?.name || ''} board`} and
              the app version.
            </p>
            {err && <p class="error small" role="alert">{err}</p>}
            <div class="feedback-actions">
              <button type="button" class="btn ghost" onClick={close}>Cancel</button>
              <button type="submit" class="btn primary" disabled={busy || !message.trim()}>{busy ? 'Sending…' : 'Send'}</button>
            </div>
          </form>
        )}
      </dialog>
    </>
  );
}
