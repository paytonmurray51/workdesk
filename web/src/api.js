// Thin fetch wrapper for the Workdesk API (session cookie auth).
export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function request(method, url, body) {
  const opts = { method, headers: {}, credentials: 'same-origin' };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(url, opts);
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data.error || res.statusText);
  return data;
}

export const api = {
  me: () => request('GET', '/api/me'),
  boards: () => request('GET', '/api/boards'),
  lists: (boardId) => request('GET', `/api/boards/${boardId}/lists`),
  items: (listId) => request('GET', `/api/lists/${listId}/items`),
  addItem: (listId, title) => request('POST', `/api/lists/${listId}/items`, { title }),
  setDone: (id, done) => request('PATCH', `/api/items/${id}`, { done }),
  logout: () => request('POST', '/auth/logout'),
};
