package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paytonmurray51/workdesk/internal/db"
)

// OAuth 2.1 authorization server for MCP connectors (MCP authorization spec):
// protected-resource metadata (RFC 9728), server metadata (RFC 8414), dynamic client
// registration (RFC 7591), authorization code + PKCE (S256 only), refresh rotation.
// People approve a connector on a consent page and choose which boards it may use.

const oauthScope = "workdesk"

func (s *Server) oauthRoutes(mux *http.ServeMux) {
	mux.Handle("GET /.well-known/oauth-protected-resource", cors(http.HandlerFunc(s.protectedResourceMeta)))
	mux.Handle("GET /.well-known/oauth-protected-resource/mcp", cors(http.HandlerFunc(s.protectedResourceMeta)))
	mux.Handle("GET /.well-known/oauth-authorization-server", cors(http.HandlerFunc(s.authServerMeta)))
	mux.Handle("OPTIONS /.well-known/", cors(http.NotFoundHandler()))
	mux.Handle("POST /oauth/register", cors(http.HandlerFunc(s.oauthRegister)))
	mux.Handle("POST /oauth/token", cors(http.HandlerFunc(s.oauthToken)))
	mux.Handle("OPTIONS /oauth/", cors(http.NotFoundHandler()))
	mux.HandleFunc("GET /oauth/authorize", s.oauthAuthorize)
	mux.HandleFunc("POST /oauth/authorize", s.oauthConsent)
	mux.HandleFunc("/.well-known/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
}

// cors allows browser-based MCP clients to discover metadata and call register/token.
// None of these endpoints use cookies.
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) protectedResourceMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.mcpURL(),
		"resource_name":            "Workdesk",
		"authorization_servers":    []string{s.cfg.BaseURL},
		"scopes_supported":         []string{oauthScope},
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Server) authServerMeta(w http.ResponseWriter, r *http.Request) {
	b := s.cfg.BaseURL
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                b,
		"authorization_endpoint":                b + "/oauth/authorize",
		"token_endpoint":                        b + "/oauth/token",
		"registration_endpoint":                 b + "/oauth/register",
		"scopes_supported":                      []string{oauthScope},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"authorization_response_iss_parameter_supported": true,
	})
}

func oauthErr(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// validRedirectURI allows https URLs, and http only for loopback (local MCP clients).
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" || len(raw) > 500 {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		GrantTypes   []string `json:"grant_types"`
		Response     []string `json:"response_types"`
	}
	body, err := readJSONBody(r)
	if err == nil {
		err = json.Unmarshal(body, &in)
	}
	if err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "body must be JSON client metadata")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		oauthErr(w, http.StatusBadRequest, "invalid_redirect_uri", "1-10 redirect_uris required")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirectURI(u) {
			oauthErr(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be https (or http on localhost)")
			return
		}
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type "+g)
			return
		}
	}
	for _, rt := range in.Response {
		if rt != "code" {
			oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported response_type "+rt)
			return
		}
	}
	if in.AuthMethod == "" {
		in.AuthMethod = "client_secret_basic" // RFC 7591 default
	}
	if in.AuthMethod != "none" && in.AuthMethod != "client_secret_post" && in.AuthMethod != "client_secret_basic" {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	name := strings.TrimSpace(in.ClientName)
	if len(name) > 100 {
		name = name[:100]
	}
	if name == "" {
		name = "MCP client"
	}
	c, secret, err := s.store.RegisterClient(r.Context(), name, in.RedirectURIs, in.AuthMethod != "none")
	if errors.Is(err, db.ErrConflict) {
		oauthErr(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many registered clients")
		return
	}
	if err != nil {
		slog.Error("oauth register", "err", err)
		oauthErr(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	slog.Info("oauth client registered", "client", c.ClientID, "name", name)
	out := map[string]any{
		"client_id":                  c.ClientID,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                name,
		"redirect_uris":              in.RedirectURIs,
		"token_endpoint_auth_method": in.AuthMethod,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      oauthScope,
	}
	if secret != "" {
		out["client_secret"] = secret
		out["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, out)
}

// authzRequest is the validated /oauth/authorize request, carried through the consent
// form as a signed value so it can't be altered.
type authzRequest struct {
	ClientID    string `json:"c"`
	RedirectURI string `json:"r"`
	State       string `json:"s"`
	Challenge   string `json:"p"`
	Resource    string `json:"res"`
	UserID      int64  `json:"u"`
}

func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := s.store.OAuthClient(r.Context(), q.Get("client_id"))
	if err != nil {
		oauthPage(w, http.StatusBadRequest, "Unknown app", "This connection request names an app Workdesk doesn't know. Try adding the connector again.")
		return
	}
	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" && len(c.RedirectURIs) == 1 {
		redirectURI = c.RedirectURIs[0]
	}
	if !slices.Contains(c.RedirectURIs, redirectURI) {
		oauthPage(w, http.StatusBadRequest, "Bad redirect", "The app asked to return somewhere it didn't register.")
		return
	}
	// From here on, errors go back to the app.
	fail := func(code, desc string) {
		http.Redirect(w, r, withParams(redirectURI, url.Values{
			"error": {code}, "error_description": {desc}, "state": {q.Get("state")}, "iss": {s.cfg.BaseURL},
		}), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	resource := q.Get("resource")
	if resource != "" && !s.validResource(resource) {
		fail("invalid_target", "unknown resource")
		return
	}

	p, err := s.authenticateSession(r)
	if errors.Is(err, errNotInvited) {
		oauthPage(w, http.StatusForbidden, "Not invited", "This Google account can't use Workdesk yet.")
		return
	}
	if err != nil {
		http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	boards, err := s.boards(r.Context(), p)
	if err != nil || len(boards) == 0 {
		oauthPage(w, http.StatusForbidden, "No boards", "You don't have any boards to share with this app.")
		return
	}
	req := authzRequest{ClientID: c.ClientID, RedirectURI: redirectURI, State: q.Get("state"),
		Challenge: q.Get("code_challenge"), Resource: resource, UserID: p.user.ID}
	sealed, err := s.signer.Seal(req, 15*time.Minute)
	if err != nil {
		oauthPage(w, http.StatusInternalServerError, "Error", "Something went wrong. Try again.")
		return
	}
	ru, _ := url.Parse(redirectURI)
	// Chrome applies form-action to the redirect after submit, so allow the app's origin.
	w.Header().Set("Content-Security-Policy", strings.Replace(defaultCSP, "form-action 'self'",
		"form-action 'self' "+ru.Scheme+"://"+ru.Host, 1))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	consentTmpl.Execute(w, map[string]any{
		"Client": c.Name, "RedirectHost": ru.Host, "Email": p.user.Email, "Boards": boards, "Req": sealed,
	})
}

// authenticateSession is like authenticate but ignores Authorization headers: consent
// must come from a person's browser session.
func (s *Server) authenticateSession(r *http.Request) (*principal, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Del("Authorization")
	return s.authenticate(r2)
}

func (s *Server) oauthConsent(w http.ResponseWriter, r *http.Request) {
	if err := http.NewCrossOriginProtection().Check(r); err != nil {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var req authzRequest
	if err := s.signer.Open(r.PostForm.Get("req"), &req); err != nil {
		oauthPage(w, http.StatusBadRequest, "Expired", "This approval page expired. Start the connection again from the app.")
		return
	}
	p, err := s.authenticateSession(r)
	if err != nil || p.user.ID != req.UserID {
		oauthPage(w, http.StatusForbidden, "Signed out", "Sign in again and restart the connection from the app.")
		return
	}
	back := func(v url.Values) {
		v.Set("state", req.State)
		v.Set("iss", s.cfg.BaseURL)
		http.Redirect(w, r, withParams(req.RedirectURI, v), http.StatusFound)
	}
	if r.PostForm.Get("decision") != "allow" {
		back(url.Values{"error": {"access_denied"}, "error_description": {"the user declined"}})
		return
	}
	var ids []int64
	for _, v := range r.PostForm["board"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err == nil && p.canBoard(r.Context(), s.store, id) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		oauthPage(w, http.StatusBadRequest, "Pick a board", "Choose at least one board for this app. Go back and try again.")
		return
	}
	code, err := s.store.CreateGrantCode(r.Context(), req.ClientID, p.user.ID, ids, req.RedirectURI, req.Challenge, req.Resource)
	if err != nil {
		slog.Error("oauth grant", "err", err)
		oauthPage(w, http.StatusInternalServerError, "Error", "Something went wrong. Try again.")
		return
	}
	slog.Info("oauth consent", "user", p.user.ID, "client", req.ClientID, "boards", ids)
	back(url.Values{"code": {code}})
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "form body required")
		return
	}
	f := r.PostForm
	clientID, secret, basic := r.BasicAuth()
	if basic {
		clientID, _ = url.QueryUnescape(clientID)
		secret, _ = url.QueryUnescape(secret)
	} else {
		clientID, secret = f.Get("client_id"), f.Get("client_secret")
	}
	c, err := s.store.OAuthClient(r.Context(), clientID)
	if err != nil {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	if c.SecretHash != nil && subtle.ConstantTimeCompare([]byte(db.HashToken(secret)), []byte(*c.SecretHash)) != 1 {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "bad client credentials")
		return
	}

	var pair *db.TokenPair
	switch f.Get("grant_type") {
	case "authorization_code":
		rec, err := s.store.ConsumeCode(r.Context(), f.Get("code"))
		if err != nil || rec.ClientID != c.ClientID {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "code is invalid, expired or already used")
			return
		}
		if f.Get("redirect_uri") != "" && f.Get("redirect_uri") != rec.RedirectURI {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
			return
		}
		if !pkceOK(f.Get("code_verifier"), rec.CodeChallenge) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
		resource := rec.Resource
		if res := f.Get("resource"); res != "" {
			if !s.validResource(res) || (resource != "" && res != resource) {
				oauthErr(w, http.StatusBadRequest, "invalid_target", "resource mismatch")
				return
			}
			resource = res
		}
		pair, err = s.store.IssueTokens(r.Context(), rec.GrantID, resource)
		if err != nil {
			slog.Error("oauth issue", "err", err)
			oauthErr(w, http.StatusInternalServerError, "server_error", "")
			return
		}
	case "refresh_token":
		pair, err = s.store.RotateRefresh(r.Context(), f.Get("refresh_token"), c.ClientID)
		if err != nil {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid or expired")
			return
		}
	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  pair.Access,
		"token_type":    "Bearer",
		"expires_in":    pair.ExpiresIn,
		"refresh_token": pair.Refresh,
		"scope":         oauthScope,
	})
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func withParams(raw string, v url.Values) string {
	u, _ := url.Parse(raw)
	q := u.Query()
	for k, vs := range v {
		if len(vs) > 0 && vs[0] != "" {
			q.Set(k, vs[0])
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func oauthPage(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	messageTmpl.Execute(w, map[string]string{"Title": title, "Msg": msg})
}

const pageStyle = `<style>
:root{color-scheme:dark;--bg:#0f1a15;--s:#17251e;--l:#2c4236;--t:#e8efe6;--m:#9db3a3;--a:#8fcf9c;--ai:#0f1a15}
@media (prefers-color-scheme: light){:root{color-scheme:light;--bg:#f4efe4;--s:#fffdf8;--l:#ded3bd;--t:#22301f;--m:#66705f;--a:#3f7d4e;--ai:#fff}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--t);font:16px/1.5 system-ui,sans-serif;padding:16px;box-sizing:border-box}
main{background:var(--s);border:1px solid var(--l);border-radius:20px;padding:24px;max-width:420px;width:100%;box-sizing:border-box}
h1{margin:0 0 8px;font-size:24px}p{margin:0 0 12px}.m{color:var(--m)}
fieldset{border:1px solid var(--l);border-radius:12px;margin:16px 0;padding:8px 12px}legend{color:var(--m);padding:0 4px}
label{display:flex;align-items:center;gap:12px;min-height:44px;cursor:pointer}input[type=checkbox]{width:22px;height:22px;accent-color:var(--a)}
.row{display:flex;gap:8px}button{flex:1;min-height:44px;border-radius:12px;border:1px solid var(--l);background:transparent;color:var(--t);font:inherit;font-weight:600;cursor:pointer}
button.p{background:var(--a);border-color:var(--a);color:var(--ai)}
</style>`

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect app · Workdesk</title>` + pageStyle + `</head>
<body><main>
<h1>Connect {{.Client}}?</h1>
<p class="m">Signed in as {{.Email}}. <strong>{{.Client}}</strong> will be able to read and change the boards you pick:
lists, to-dos, briefings, inbox, widgets and timeboxes. You'll return to <strong>{{.RedirectHost}}</strong>.</p>
<form method="post" action="/oauth/authorize">
<input type="hidden" name="req" value="{{.Req}}">
<fieldset><legend>Boards</legend>
{{range .Boards}}<label><input type="checkbox" name="board" value="{{.ID}}" checked> {{.Name}}</label>
{{end}}</fieldset>
<div class="row"><button type="submit" name="decision" value="deny">Cancel</button>
<button type="submit" name="decision" value="allow" class="p">Allow</button></div>
</form>
<p class="m" style="margin-top:16px;font-size:14px">You can disconnect it any time under Settings → Connected apps.</p>
</main></body></html>`))

var messageTmpl = template.Must(template.New("msg").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} · Workdesk</title>` + pageStyle + `</head>
<body><main><h1>{{.Title}}</h1><p class="m">{{.Msg}}</p><p><a href="/" style="color:var(--a)">Back to Workdesk</a></p></main></body></html>`))
