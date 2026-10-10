package auth

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSignerRoundTripAndTamper(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	v, err := s.Seal(session{UserID: 42}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var got session
	if err := s.Open(v, &got); err != nil || got.UserID != 42 {
		t.Fatalf("open = %+v, %v", got, err)
	}

	tampered := []byte(v)
	tampered[3] ^= 1
	if err := s.Open(string(tampered), &got); err == nil {
		t.Fatal("tampered cookie accepted")
	}

	other := NewSigner([]byte("another-secret-another-secret-xx"))
	if err := other.Open(v, &got); err == nil {
		t.Fatal("cookie from a different secret accepted")
	}

	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if err := s.Open(v, &got); err == nil {
		t.Fatal("expired cookie accepted")
	}
}

func TestSessionsCookie(t *testing.T) {
	ss := &Sessions{Signer: NewSigner([]byte("0123456789abcdef0123456789abcdef")), Secure: true}
	rec := httptest.NewRecorder()
	if err := ss.Set(rec, 7); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	if !c.HttpOnly || !c.Secure || c.Name != SessionCookie {
		t.Fatalf("cookie flags: %+v", c)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	if id := ss.UserID(req); id != 7 {
		t.Fatalf("UserID = %d", id)
	}
	if id := ss.UserID(httptest.NewRequest("GET", "/", nil)); id != 0 {
		t.Fatalf("no cookie UserID = %d", id)
	}
}
