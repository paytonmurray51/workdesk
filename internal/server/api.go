package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/paytonmurray51/workdesk/internal/db"
)

func (s *Server) apiRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/me", s.authed(s.me))
	mux.Handle("GET /api/boards", s.authed(s.listBoards))
	mux.Handle("GET /api/boards/{board}/lists", s.authed(s.listLists))
	mux.Handle("POST /api/boards/{board}/lists", s.authed(s.createList))
	mux.Handle("GET /api/lists/{list}/items", s.authed(s.listItems))
	mux.Handle("POST /api/lists/{list}/items", s.authed(s.addItems))
	mux.Handle("PATCH /api/items/{id}", s.authed(s.patchItem))

	mux.Handle("POST /api/boards/{board}/briefings", s.authed(s.postBriefing))
	mux.Handle("GET /api/boards/{board}/briefings/latest", s.authed(s.latestBriefing))
	mux.Handle("POST /api/boards/{board}/inbox", s.authed(s.pushInbox))
	mux.Handle("GET /api/boards/{board}/inbox", s.authed(s.listInbox))
	mux.Handle("PUT /api/boards/{board}/widgets/{name}", s.authed(s.putWidget))
	mux.Handle("GET /api/boards/{board}/widgets", s.authed(s.listWidgets))
	mux.Handle("PUT /api/boards/{board}/days/{date}/timeboxes", s.authed(s.putTimeboxes))
	mux.Handle("GET /api/boards/{board}/days/{date}/timeboxes", s.authed(s.getTimeboxes))

	mux.Handle("GET /api/boards/{board}/tokens", s.sessionOnly(s.listTokens))
	mux.Handle("POST /api/boards/{board}/tokens", s.sessionOnly(s.createToken))
	mux.Handle("DELETE /api/tokens/{id}", s.sessionOnly(s.deleteToken))
	mux.Handle("GET /api/connections", s.sessionOnly(s.listConnections))
	mux.Handle("DELETE /api/connections/{id}", s.sessionOnly(s.deleteConnection))
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, p *principal) {
	out := map[string]any{"kind": p.kind, "name": p.label}
	if p.user != nil {
		out = map[string]any{"kind": p.kind, "id": p.user.ID, "email": p.user.Email, "name": p.user.Name, "admin": p.admin}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listBoards(w http.ResponseWriter, r *http.Request, p *principal) {
	boards, err := s.boards(r.Context(), p)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, boards)
}

// pathBoard resolves {board} to a board the principal may use, writing 404 otherwise.
func (s *Server) pathBoard(w http.ResponseWriter, r *http.Request, p *principal) (*db.Board, bool) {
	id, ok := pathID(w, r, "board")
	if !ok {
		return nil, false
	}
	b, err := s.board(r.Context(), p, id)
	if err != nil {
		writeStoreErr(w, err)
		return nil, false
	}
	return b, true
}

func (s *Server) listLists(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	lists, err := s.store.Lists(r.Context(), b.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lists)
}

func (s *Server) createList(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	l, created, err := s.store.CreateList(r.Context(), b.ID, in.Name, in.Kind)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, l)
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "list")
	if !ok {
		return
	}
	_, b, err := s.list(r.Context(), p, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	openOnly := r.URL.Query().Get("open") == "true"
	items, err := s.store.Items(r.Context(), id, openOnly, db.StartOfDay(time.Now(), b.Timezone))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// addItems accepts one item object or an array of them; items with source_id upsert.
func (s *Server) addItems(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "list")
	if !ok {
		return
	}
	if _, _, err := s.list(r.Context(), p, id); err != nil {
		writeStoreErr(w, err)
		return
	}
	in, ok := decodeMany[db.NewItem](w, r, 200)
	if !ok {
		return
	}
	items, err := s.store.AddItems(r.Context(), id, in)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, items)
}

func (s *Server) patchItem(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var patch db.ItemPatch
	if !decodeBody(w, r, &patch) {
		return
	}
	it, err := s.updateItem(r, p, id, patch)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// updateItem checks access to the item's list and, when moving, to the target list.
func (s *Server) updateItem(r *http.Request, p *principal, id int64, patch db.ItemPatch) (*db.Item, error) {
	ctx := r.Context()
	it, err := s.store.Item(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.list(ctx, p, it.ListID); err != nil {
		return nil, err
	}
	if patch.ListID != nil {
		if _, _, err := s.list(ctx, p, *patch.ListID); err != nil {
			return nil, err
		}
	}
	return s.store.UpdateItem(ctx, id, patch)
}

func (s *Server) postBriefing(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	var in db.NewBriefing
	if !decodeBody(w, r, &in) {
		return
	}
	br, err := s.store.PostBriefing(r.Context(), b.ID, in)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, br)
}

func (s *Server) latestBriefing(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	br, err := s.store.LatestBriefing(r.Context(), b.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, br)
}

func (s *Server) pushInbox(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	in, ok := decodeMany[db.NewInboxItem](w, r, 200)
	if !ok {
		return
	}
	items, err := s.store.PushInbox(r.Context(), b.ID, in)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, items)
}

func (s *Server) listInbox(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	items, err := s.store.Inbox(r.Context(), b.ID, r.URL.Query().Get("status"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// putWidget stores the request body as the widget's JSON payload.
func (s *Server) putWidget(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	wd, err := s.store.SetWidget(r.Context(), b.ID, r.PathValue("name"), json.RawMessage(body))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wd)
}

func (s *Server) listWidgets(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	ws, err := s.store.Widgets(r.Context(), b.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

// putTimeboxes replaces the day's proposed boxes. Body: an array, or {"boxes": [...]}.
func (s *Server) putTimeboxes(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var boxes []db.NewTimebox
	if err := json.Unmarshal(body, &boxes); err != nil {
		var wrapped struct {
			Boxes []db.NewTimebox `json:"boxes"`
		}
		if err := json.Unmarshal(body, &wrapped); err != nil {
			writeErr(w, http.StatusBadRequest, "body must be an array of timeboxes")
			return
		}
		boxes = wrapped.Boxes
	}
	out, err := s.store.SetTimeboxes(r.Context(), b.ID, r.PathValue("date"), boxes)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTimeboxes(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	out, err := s.store.Timeboxes(r.Context(), b.ID, r.PathValue("date"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Tokens and connected apps (session only).

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	ts, err := s.store.APITokens(r.Context(), b.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

// createToken returns the secret exactly once.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request, p *principal) {
	b, ok := s.pathBoard(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	t, secret, err := s.store.CreateAPIToken(r.Context(), b.ID, p.user.ID, in.Name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	t, err := s.store.APIToken(r.Context(), id)
	if err == nil {
		_, err = s.board(r.Context(), p, t.BoardID)
	}
	if err == nil {
		err = s.store.DeleteAPIToken(r.Context(), id)
	}
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request, p *principal) {
	gs, err := s.store.Grants(r.Context(), p.user.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, gs)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.store.RevokeGrant(r.Context(), id, p.user.ID); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := readJSONBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func decodeMany[T any](w http.ResponseWriter, r *http.Request, max int) ([]T, bool) {
	body, err := readJSONBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	out, err := decodeOneOrMany[T](body, max)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return out, true
}
