// Package auth handles Google sign-in and signed session cookies.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

var errBadCookie = errors.New("invalid cookie")

// Signer seals small JSON payloads into tamper-proof cookie values (HMAC-SHA256).
// Payloads are signed, not encrypted: never put secrets in them.
type Signer struct {
	key []byte
	now func() time.Time
}

func NewSigner(secret []byte) *Signer {
	// Derive a purpose-specific key so the raw secret is never used directly.
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("workdesk cookie v1"))
	return &Signer{key: m.Sum(nil), now: time.Now}
}

type envelope struct {
	Exp  int64           `json:"exp"`
	Data json.RawMessage `json:"d"`
}

func (s *Signer) Seal(v any, ttl time.Duration) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(envelope{Exp: s.now().Add(ttl).Unix(), Data: data})
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(body)
	return p + "." + base64.RawURLEncoding.EncodeToString(s.mac(p)), nil
}

func (s *Signer) Open(value string, v any) error {
	p, sig, ok := strings.Cut(value, ".")
	if !ok {
		return errBadCookie
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac(p)) {
		return errBadCookie
	}
	body, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return errBadCookie
	}
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil {
		return errBadCookie
	}
	if s.now().Unix() >= e.Exp {
		return errBadCookie
	}
	return json.Unmarshal(e.Data, v)
}

func (s *Signer) mac(p string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(p))
	return m.Sum(nil)
}

const (
	SessionCookie = "wd_session"
	sessionTTL    = 30 * 24 * time.Hour
)

type session struct {
	UserID int64 `json:"uid"`
}

// Sessions issues and reads the login cookie.
type Sessions struct {
	Signer *Signer
	Secure bool
}

func (s *Sessions) Set(w http.ResponseWriter, userID int64) error {
	v, err := s.Signer.Seal(session{UserID: userID}, sessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: v, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
}

// UserID returns the signed-in user id, or 0.
func (s *Sessions) UserID(r *http.Request) int64 {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return 0
	}
	var sess session
	if err := s.Signer.Open(c.Value, &sess); err != nil {
		return 0
	}
	return sess.UserID
}
