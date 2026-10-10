// Package server wires the HTTP routes: health, Google sign-in, the REST API and the UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/paytonmurray51/workdesk/internal/auth"
	"github.com/paytonmurray51/workdesk/internal/config"
	"github.com/paytonmurray51/workdesk/internal/db"
)

type Server struct {
	cfg      *config.Config
	store    *db.Store
	sessions *auth.Sessions
	google   *auth.Google
	ui       fs.FS
}

func New(cfg *config.Config, store *db.Store, ui fs.FS) *Server {
	signer := auth.NewSigner(cfg.SessionSecret)
	s := &Server{
		cfg:      cfg,
		store:    store,
		sessions: &auth.Sessions{Signer: signer, Secure: cfg.Secure()},
		ui:       ui,
	}
	s.google = &auth.Google{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.BaseURL + "/auth/callback",
		Signer:       signer,
		Secure:       cfg.Secure(),
		OnLogin:      s.onLogin,
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /auth/login", s.google.Login)
	mux.HandleFunc("GET /auth/callback", s.google.Callback)
	mux.HandleFunc("POST /auth/logout", s.logout)

	mux.Handle("GET /api/me", s.authed(s.me))
	mux.Handle("GET /api/boards", s.authed(s.listBoards))
	mux.Handle("GET /api/boards/{board}/lists", s.authed(s.listLists))
	mux.Handle("GET /api/lists/{list}/items", s.authed(s.listItems))
	mux.Handle("POST /api/lists/{list}/items", s.authed(s.addItems))
	mux.Handle("PATCH /api/items/{id}", s.authed(s.patchItem))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})

	mux.HandleFunc("/", s.serveUI)

	// Rejects cross-site POST/PATCH (Sec-Fetch-Site / Origin checks) on top of SameSite=Lax.
	csrf := http.NewCrossOriginProtection()
	return securityHeaders(csrf.Handler(mux))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}

// onLogin admits a verified Google identity. Milestone 1: only WORKDESK_ADMIN_EMAILS.
func (s *Server) onLogin(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	if !s.cfg.AdminEmails[id.Email] {
		slog.Info("sign-in refused: not invited", "email", id.Email)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, notInvitedPage)
		return
	}
	u, err := s.store.UpsertUser(r.Context(), id.Sub, id.Email, id.Name)
	if err != nil {
		slog.Error("upsert user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.sessions.Set(w, u.ID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("signed in", "user", u.ID)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.sessions.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

type viewer struct {
	user  *db.User
	admin bool
}

// authed requires a session for a user who is (still) allowed in.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, viewer)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := s.sessions.UserID(r)
		if id == 0 {
			writeErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		u, err := s.store.UserByID(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		v := viewer{user: u, admin: s.cfg.AdminEmails[u.Email]}
		if !v.admin {
			// Invites arrive in milestone 5; until then only admins may use the app.
			writeErr(w, http.StatusForbidden, "not invited")
			return
		}
		h(w, r, v)
	})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, v viewer) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id": v.user.ID, "email": v.user.Email, "name": v.user.Name, "admin": v.admin,
	})
}

func (s *Server) listBoards(w http.ResponseWriter, r *http.Request, v viewer) {
	boards, err := s.store.Boards(r.Context(), v.user.ID, v.admin)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, boards)
}

func (s *Server) listLists(w http.ResponseWriter, r *http.Request, v viewer) {
	id, ok := pathID(w, r, "board")
	if !ok {
		return
	}
	if _, err := s.store.Board(r.Context(), id, v.user.ID, v.admin); err != nil {
		writeStoreErr(w, err)
		return
	}
	lists, err := s.store.Lists(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lists)
}

// listFor loads a list and checks the viewer can see its board.
func (s *Server) listFor(w http.ResponseWriter, r *http.Request, v viewer, id int64) (*db.List, *db.Board, bool) {
	l, err := s.store.List(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return nil, nil, false
	}
	b, err := s.store.Board(r.Context(), l.BoardID, v.user.ID, v.admin)
	if err != nil {
		writeStoreErr(w, err)
		return nil, nil, false
	}
	return l, b, true
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request, v viewer) {
	id, ok := pathID(w, r, "list")
	if !ok {
		return
	}
	_, b, ok := s.listFor(w, r, v, id)
	if !ok {
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

// addItems accepts one item object or an array of them.
func (s *Server) addItems(w http.ResponseWriter, r *http.Request, v viewer) {
	id, ok := pathID(w, r, "list")
	if !ok {
		return
	}
	if _, _, ok := s.listFor(w, r, v, id); !ok {
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var in []db.NewItem
	if trimmed := strings.TrimSpace(string(body)); strings.HasPrefix(trimmed, "[") {
		err = json.Unmarshal(body, &in)
	} else {
		var one db.NewItem
		err = json.Unmarshal(body, &one)
		in = []db.NewItem{one}
	}
	if err != nil || len(in) == 0 || len(in) > 200 {
		writeErr(w, http.StatusBadRequest, "body must be an item or an array of 1-200 items")
		return
	}
	items, err := s.store.AddItems(r.Context(), id, in)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, items)
}

func (s *Server) patchItem(w http.ResponseWriter, r *http.Request, v viewer) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	it, err := s.store.Item(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if _, _, ok := s.listFor(w, r, v, it.ListID); !ok {
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var p db.ItemPatch
	if err := json.Unmarshal(body, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	updated, err := s.store.UpdateItem(r.Context(), id, p)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// serveUI serves built assets and falls back to index.html for client-side routes.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name != "" && name != "index.html" {
		if f, err := s.ui.Open(name); err == nil {
			st, err := f.Stat()
			f.Close()
			if err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite fingerprints asset names, so they can be cached forever.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.ServeFileFS(w, r, s.ui, name)
				return
			}
		}
	}
	index, err := fs.ReadFile(s.ui, "index.html")
	if err != nil {
		http.Error(w, "UI not built", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(index)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https://*.googleusercontent.com; "+
			"style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.ServeHTTP(w, r)
	})
}

func readJSONBody(r *http.Request) ([]byte, error) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return nil, errors.New("Content-Type must be application/json")
	}
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, db.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid input: title must be 1-500 characters")
	default:
		slog.Error("store", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

const notInvitedPage = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Not invited · Workdesk</title>
<style>body{font:16px/1.5 system-ui,sans-serif;background:#13201a;color:#e8efe6;display:grid;place-items:center;min-height:100vh;margin:0;padding:16px}
main{max-width:28rem}a{color:#9fd3a8}</style></head><body><main>
<h1>Not invited yet</h1><p>This Google account isn't on the Workdesk invite list.
Ask the board owner for an invite, or <a href="/auth/login">sign in with another account</a>.</p>
</main></body></html>`
