// Package server wires the HTTP routes: health, Google sign-in, the REST API, OAuth for
// MCP connectors, the MCP server and the UI.
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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paytonmurray51/workdesk/internal/auth"
	"github.com/paytonmurray51/workdesk/internal/config"
	"github.com/paytonmurray51/workdesk/internal/db"
)

type Server struct {
	cfg      *config.Config
	store    *db.Store
	sessions *auth.Sessions
	signer   *auth.Signer
	google   *auth.Google
	ui       fs.FS
	schemas  *mcp.SchemaCache
}

func New(cfg *config.Config, store *db.Store, ui fs.FS) *Server {
	signer := auth.NewSigner(cfg.SessionSecret)
	s := &Server{
		cfg:      cfg,
		store:    store,
		sessions: &auth.Sessions{Signer: signer, Secure: cfg.Secure()},
		signer:   signer,
		ui:       ui,
		schemas:  mcp.NewSchemaCache(),
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

	s.apiRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})
	mux.HandleFunc("/", s.serveUI)

	// The OAuth authorization server and the MCP endpoint are called server-to-server or
	// by top-level navigation, so they sit outside the cross-origin check; the consent
	// form checks its own origin and carries a signed request.
	open := http.NewServeMux()
	s.oauthRoutes(open)
	open.Handle("/mcp", s.mcpHandler())

	app := http.NewCrossOriginProtection().Handler(mux)
	return securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/mcp" || strings.HasPrefix(p, "/oauth/") || strings.HasPrefix(p, "/.well-known/") {
			open.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	}))
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

// onLogin admits a verified Google identity. Until invites (milestone 5), only
// WORKDESK_ADMIN_EMAILS.
func (s *Server) onLogin(w http.ResponseWriter, r *http.Request, id auth.Identity, next string) {
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
	http.Redirect(w, r, auth.SafeNext(next), http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.sessions.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

type apiHandler func(http.ResponseWriter, *http.Request, *principal)

// authed requires any principal: session, board API token or OAuth access token.
func (s *Server) authed(h apiHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authenticate(r)
		switch {
		case errors.Is(err, errNotInvited):
			writeErr(w, http.StatusForbidden, "not invited")
		case err != nil:
			if r.Header.Get("Authorization") != "" {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			}
			writeErr(w, http.StatusUnauthorized, "sign in required")
		default:
			h(w, r, p)
		}
	})
}

// sessionOnly is for settings a person changes in the UI (tokens, connected apps): a
// token must never be able to mint or list other tokens.
func (s *Server) sessionOnly(h apiHandler) http.Handler {
	return s.authed(func(w http.ResponseWriter, r *http.Request, p *principal) {
		if p.kind != "session" {
			writeErr(w, http.StatusForbidden, "only available when signed in")
			return
		}
		h(w, r, p)
	})
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

const defaultCSP = "default-src 'self'; img-src 'self' data: https://*.googleusercontent.com; " +
	"style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", defaultCSP)
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

// decodeOneOrMany accepts a JSON object or an array of 1..max objects.
func decodeOneOrMany[T any](body []byte, max int) ([]T, error) {
	var out []T
	var err error
	if strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		err = json.Unmarshal(body, &out)
	} else {
		var one T
		err = json.Unmarshal(body, &one)
		out = []T{one}
	}
	if err != nil || len(out) == 0 || len(out) > max {
		return nil, errors.New("body must be an object or an array of 1-" + strconv.Itoa(max) + " objects")
	}
	return out, nil
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
	var ie inputError
	switch {
	case errors.As(err, &ie):
		writeErr(w, http.StatusBadRequest, ie.Error())
	case errors.Is(err, db.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, db.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid input")
	case errors.Is(err, db.ErrConflict):
		writeErr(w, http.StatusConflict, "conflict")
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
