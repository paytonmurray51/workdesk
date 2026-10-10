package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/paytonmurray51/workdesk/internal/db"
)

// principal is whoever is making a request: a signed-in person (session cookie), a
// Claude routine with a board API token, or an MCP connector with an OAuth grant.
type principal struct {
	kind  string   // "session", "token" or "oauth"
	user  *db.User // nil for API tokens
	admin bool
	// boards limits access for tokens and OAuth grants; nil means "by membership" (sessions).
	boards map[int64]bool
	label  string // token name or connector name, for logs
}

func (p *principal) canBoard(ctx context.Context, store *db.Store, boardID int64) bool {
	if p.boards != nil {
		return p.boards[boardID]
	}
	if p.admin {
		return true
	}
	_, err := store.Board(ctx, boardID, p.user.ID, false)
	return err == nil
}

// board loads a board the principal may use, else ErrNotFound (never reveals existence).
func (s *Server) board(ctx context.Context, p *principal, id int64) (*db.Board, error) {
	if !p.canBoard(ctx, s.store, id) {
		return nil, db.ErrNotFound
	}
	return s.store.BoardByID(ctx, id)
}

func (s *Server) boards(ctx context.Context, p *principal) ([]db.Board, error) {
	if p.boards == nil {
		return s.store.Boards(ctx, p.user.ID, p.admin)
	}
	all, err := s.store.AllBoards(ctx)
	if err != nil {
		return nil, err
	}
	out := []db.Board{}
	for _, b := range all {
		if p.boards[b.ID] {
			out = append(out, b)
		}
	}
	return out, nil
}

// list loads a list whose board the principal may use.
func (s *Server) list(ctx context.Context, p *principal, id int64) (*db.List, *db.Board, error) {
	l, err := s.store.List(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	b, err := s.board(ctx, p, l.BoardID)
	if err != nil {
		return nil, nil, err
	}
	return l, b, nil
}

var errAuth = errors.New("unauthorized")

// authenticate resolves the request's principal from a bearer token or the session cookie.
// A nil principal with errAuth means "not signed in"; a non-nil error otherwise is a 403.
func (s *Server) authenticate(r *http.Request) (*principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, tok, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			return nil, errAuth
		}
		return s.bearer(r.Context(), strings.TrimSpace(tok))
	}
	id := s.sessions.UserID(r)
	if id == 0 {
		return nil, errAuth
	}
	u, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		return nil, errAuth
	}
	p := &principal{kind: "session", user: u, admin: s.cfg.AdminEmails[u.Email], label: u.Email}
	if !p.admin {
		// Invites arrive in milestone 5; until then only admins may use the app.
		return nil, errNotInvited
	}
	return p, nil
}

var errNotInvited = errors.New("not invited")

func (s *Server) bearer(ctx context.Context, tok string) (*principal, error) {
	switch {
	case strings.HasPrefix(tok, db.APITokenPrefix):
		t, err := s.store.VerifyAPIToken(ctx, tok)
		if err != nil {
			return nil, errAuth
		}
		return &principal{kind: "token", boards: map[int64]bool{t.BoardID: true}, label: t.Name}, nil
	case strings.HasPrefix(tok, db.OAuthAccessPrefix):
		g, err := s.store.VerifyAccess(ctx, tok)
		if err != nil || !s.validResource(g.Resource) {
			return nil, errAuth
		}
		u, err := s.store.UserByID(ctx, g.UserID)
		if err != nil || !s.cfg.AdminEmails[u.Email] {
			// The person who connected the app lost access; so does the app.
			return nil, errAuth
		}
		p := &principal{kind: "oauth", user: u, admin: true, boards: map[int64]bool{}, label: g.ClientName}
		for _, b := range g.BoardIDs {
			p.boards[b] = true
		}
		return p, nil
	}
	return nil, errAuth
}

// validResource accepts tokens issued for this server (RFC 8707 resource indicators).
func (s *Server) validResource(res string) bool {
	return res == "" || res == s.cfg.BaseURL || res == s.cfg.BaseURL+"/" || res == s.mcpURL()
}

func (s *Server) mcpURL() string { return s.cfg.BaseURL + "/mcp" }

func (s *Server) resourceMetadataURL() string {
	return s.cfg.BaseURL + "/.well-known/oauth-protected-resource/mcp"
}
