package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	googleIssuer = "https://accounts.google.com"
	stateCookie  = "wd_oauth"
	stateTTL     = 10 * time.Minute
)

// Identity is what a verified Google ID token tells us.
type Identity struct {
	Sub   string
	Email string
	Name  string
}

// Google runs the OpenID Connect authorization-code flow (with PKCE) against Google.
// The provider is discovered lazily so the server starts even if Google is unreachable.
type Google struct {
	ClientID, ClientSecret, RedirectURL string
	Signer                              *Signer
	Secure                              bool

	// OnLogin decides whether the identity may sign in and starts the session.
	// It writes the response itself.
	OnLogin func(w http.ResponseWriter, r *http.Request, id Identity)

	mu       sync.Mutex
	provider *oidc.Provider
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

func (g *Google) setup(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.provider == nil {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		p, err := oidc.NewProvider(ctx, googleIssuer)
		if err != nil {
			return nil, nil, err
		}
		g.provider = p
	}
	conf := &oauth2.Config{
		ClientID:     g.ClientID,
		ClientSecret: g.ClientSecret,
		RedirectURL:  g.RedirectURL,
		Endpoint:     g.provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
	return conf, g.provider.Verifier(&oidc.Config{ClientID: g.ClientID}), nil
}

// Login redirects to Google's consent screen.
func (g *Google) Login(w http.ResponseWriter, r *http.Request) {
	conf, _, err := g.setup(r.Context())
	if err != nil {
		slog.Error("oidc discovery", "err", err)
		http.Error(w, "Google sign-in is unavailable right now. Try again shortly.", http.StatusBadGateway)
		return
	}
	st := loginState{State: randString(), Nonce: randString(), Verifier: oauth2.GenerateVerifier()}
	v, err := g.Signer.Seal(st, stateTTL)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: v, Path: "/auth/", MaxAge: int(stateTTL.Seconds()),
		HttpOnly: true, Secure: g.Secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, conf.AuthCodeURL(st.State,
		oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier),
		oauth2.SetAuthURLParam("prompt", "select_account")), http.StatusFound)
}

// Callback finishes the flow: checks state, exchanges the code, verifies the ID token.
func (g *Google) Callback(w http.ResponseWriter, r *http.Request) {
	id, err := g.callback(r)
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/auth/", MaxAge: -1,
		HttpOnly: true, Secure: g.Secure, SameSite: http.SameSiteLaxMode})
	if err != nil {
		slog.Warn("sign-in failed", "err", err)
		http.Error(w, "Sign-in failed. Go back and try again.", http.StatusBadRequest)
		return
	}
	g.OnLogin(w, r, id)
}

func (g *Google) callback(r *http.Request) (Identity, error) {
	if e := r.URL.Query().Get("error"); e != "" {
		return Identity{}, fmt.Errorf("google returned error %q", e)
	}
	c, err := r.Cookie(stateCookie)
	if err != nil {
		return Identity{}, errors.New("missing state cookie")
	}
	var st loginState
	if err := g.Signer.Open(c.Value, &st); err != nil {
		return Identity{}, errors.New("bad or expired state cookie")
	}
	if r.URL.Query().Get("state") != st.State {
		return Identity{}, errors.New("state mismatch")
	}
	conf, verifier, err := g.setup(r.Context())
	if err != nil {
		return Identity{}, err
	}
	tok, err := conf.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("code exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return Identity{}, errors.New("no id_token in response")
	}
	idt, err := verifier.Verify(r.Context(), raw)
	if err != nil {
		return Identity{}, fmt.Errorf("verify id_token: %w", err)
	}
	if idt.Nonce != st.Nonce {
		return Identity{}, errors.New("nonce mismatch")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idt.Claims(&claims); err != nil {
		return Identity{}, err
	}
	if claims.Email == "" || !claims.EmailVerified {
		return Identity{}, errors.New("google account email is not verified")
	}
	return Identity{Sub: idt.Subject, Email: strings.ToLower(claims.Email), Name: claims.Name}, nil
}

func randString() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
