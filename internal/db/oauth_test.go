package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOAuthCodeAndRefreshFlow(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "sub", "a@example.com", "A")
	t0 := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	setNow(t, t0)

	c, secret, err := s.RegisterClient(ctx, "Claude", []string{"https://claude.ai/api/mcp/auth_callback"}, false)
	if err != nil || secret != "" || c.SecretHash != nil {
		t.Fatalf("public client = %+v %q %v", c, secret, err)
	}
	got, err := s.OAuthClient(ctx, c.ClientID)
	if err != nil || got.Name != "Claude" || got.RedirectURIs[0] != "https://claude.ai/api/mcp/auth_callback" {
		t.Fatalf("lookup = %+v %v", got, err)
	}

	code, err := s.CreateGrantCode(ctx, c.ClientID, u.ID, []int64{1, 2}, "https://claude.ai/api/mcp/auth_callback", "chal", "https://w/mcp")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.ConsumeCode(ctx, code)
	if err != nil || rec.ClientID != c.ClientID || rec.CodeChallenge != "chal" || rec.Resource != "https://w/mcp" {
		t.Fatalf("consume = %+v %v", rec, err)
	}
	if _, err := s.ConsumeCode(ctx, code); !errors.Is(err, ErrNotFound) {
		t.Fatal("code must be single-use")
	}

	pair, err := s.IssueTokens(ctx, rec.GrantID, rec.Resource)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.VerifyAccess(ctx, pair.Access)
	if err != nil || g.UserID != u.ID || len(g.BoardIDs) != 2 || g.ClientName != "Claude" || g.Resource != "https://w/mcp" {
		t.Fatalf("verify = %+v %v", g, err)
	}
	if _, err := s.VerifyAccess(ctx, pair.Refresh); !errors.Is(err, ErrNotFound) {
		t.Fatal("refresh token must not work as an access token")
	}

	// Refresh rotates: the old refresh token is spent.
	if _, err := s.RotateRefresh(ctx, pair.Refresh, "someone-else"); !errors.Is(err, ErrNotFound) {
		t.Fatal("refresh must be bound to its client")
	}
	pair2, err := s.RotateRefresh(ctx, pair.Refresh, c.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateRefresh(ctx, pair.Refresh, c.ClientID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old refresh token reused")
	}

	// Access tokens expire after an hour.
	setNow(t, t0.Add(AccessTTL+time.Minute))
	if _, err := s.VerifyAccess(ctx, pair2.Access); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired access token accepted")
	}

	grants, _ := s.Grants(ctx, u.ID)
	if len(grants) != 1 {
		t.Fatalf("grants = %+v", grants)
	}
	if err := s.RevokeGrant(ctx, grants[0].ID, u.ID+1); !errors.Is(err, ErrNotFound) {
		t.Fatal("another user revoked the grant")
	}
	if err := s.RevokeGrant(ctx, grants[0].ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateRefresh(ctx, pair2.Refresh, c.ClientID); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked grant's refresh token still works")
	}
}

func TestCodeExpires(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "sub", "a@example.com", "A")
	c, secret, _ := s.RegisterClient(ctx, "C", []string{"https://x/cb"}, true)
	if secret == "" || c.SecretHash == nil || *c.SecretHash != HashToken(secret) {
		t.Fatal("confidential client needs a hashed secret")
	}
	t0 := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	setNow(t, t0)
	code, _ := s.CreateGrantCode(ctx, c.ClientID, u.ID, []int64{1}, "https://x/cb", "c", "")
	setNow(t, t0.Add(CodeTTL+time.Second))
	if _, err := s.ConsumeCode(ctx, code); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired code accepted")
	}
	if _, err := s.CreateGrantCode(ctx, c.ClientID, u.ID, nil, "https://x/cb", "c", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("grant with no boards allowed")
	}
}
