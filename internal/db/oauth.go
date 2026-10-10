package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	OAuthClientPrefix  = "wdc_"
	OAuthCodePrefix    = "wdcode_"
	OAuthAccessPrefix  = "wdo_"
	OAuthRefreshPrefix = "wdr_"

	CodeTTL    = 10 * time.Minute
	AccessTTL  = time.Hour
	RefreshTTL = 30 * 24 * time.Hour

	MaxOAuthClients = 200
)

type OAuthClient struct {
	ClientID     string
	SecretHash   *string
	Name         string
	RedirectURIs []string
}

// RegisterClient stores a dynamically registered client. Confidential clients get a secret,
// returned once.
func (s *Store) RegisterClient(ctx context.Context, name string, redirectURIs []string, confidential bool) (*OAuthClient, string, error) {
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM oauth_clients`).Scan(&n); err != nil {
		return nil, "", err
	}
	if n >= MaxOAuthClients {
		// Drop clients nobody ever finished connecting with before refusing.
		s.DB.ExecContext(ctx, `DELETE FROM oauth_clients WHERE client_id NOT IN
			(SELECT client_id FROM oauth_grants) AND created_at < ?`,
			now().Add(-24*time.Hour).Format(timeFmt))
		if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM oauth_clients`).Scan(&n); err != nil {
			return nil, "", err
		}
		if n >= MaxOAuthClients {
			return nil, "", ErrConflict
		}
	}
	c := &OAuthClient{ClientID: NewSecret(OAuthClientPrefix)[:len(OAuthClientPrefix)+22], Name: name, RedirectURIs: redirectURIs}
	var secret string
	if confidential {
		secret = NewSecret("wds_")
		h := HashToken(secret)
		c.SecretHash = &h
	}
	uris, _ := json.Marshal(redirectURIs)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO oauth_clients (client_id, secret_hash, name, redirect_uris, created_at)
		VALUES (?, ?, ?, ?, ?)`, c.ClientID, c.SecretHash, name, string(uris), now().Format(timeFmt))
	if err != nil {
		return nil, "", err
	}
	return c, secret, nil
}

func (s *Store) OAuthClient(ctx context.Context, clientID string) (*OAuthClient, error) {
	c := &OAuthClient{ClientID: clientID}
	var uris string
	err := s.DB.QueryRowContext(ctx, `SELECT secret_hash, name, redirect_uris FROM oauth_clients WHERE client_id = ?`,
		clientID).Scan(&c.SecretHash, &c.Name, &uris)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(uris), &c.RedirectURIs)
	return c, nil
}

// Grant is one user's consent for a client, limited to some boards.
type Grant struct {
	ID         int64   `json:"id"`
	ClientID   string  `json:"client_id"`
	ClientName string  `json:"client_name"`
	UserID     int64   `json:"user_id"`
	BoardIDs   []int64 `json:"board_ids"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	Resource   string  `json:"-"` // set when loaded through a token
}

// CreateGrantCode records consent and returns a one-time authorization code.
func (s *Store) CreateGrantCode(ctx context.Context, clientID string, userID int64, boardIDs []int64,
	redirectURI, codeChallenge, resource string) (string, error) {
	if len(boardIDs) == 0 {
		return "", ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var grantID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO oauth_grants (client_id, user_id, created_at) VALUES (?, ?, ?) RETURNING id`,
		clientID, userID, now().Format(timeFmt)).Scan(&grantID); err != nil {
		return "", err
	}
	for _, b := range boardIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO oauth_grant_boards (grant_id, board_id) VALUES (?, ?)`, grantID, b); err != nil {
			return "", err
		}
	}
	code := NewSecret(OAuthCodePrefix)
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_tokens (hash, kind, grant_id, expires_at, redirect_uri, code_challenge, resource)
		VALUES (?, 'code', ?, ?, ?, ?, ?)`, HashToken(code), grantID, now().Add(CodeTTL).Format(timeFmt),
		redirectURI, codeChallenge, resource); err != nil {
		return "", err
	}
	return code, tx.Commit()
}

// CodeRecord is what an authorization code was issued for.
type CodeRecord struct {
	GrantID       int64
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Resource      string
}

// ConsumeCode deletes a code and returns what it was for. Codes are single-use even if the
// caller then rejects the exchange.
func (s *Store) ConsumeCode(ctx context.Context, code string) (*CodeRecord, error) {
	r := &CodeRecord{}
	err := s.DB.QueryRowContext(ctx, `
		DELETE FROM oauth_tokens WHERE hash = ? AND kind = 'code' AND expires_at > ?
		RETURNING grant_id, redirect_uri, code_challenge, resource,
			(SELECT client_id FROM oauth_grants g WHERE g.id = grant_id)`,
		HashToken(code), now().Format(timeFmt)).Scan(&r.GrantID, &r.RedirectURI, &r.CodeChallenge, &r.Resource, &r.ClientID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

type TokenPair struct {
	Access    string
	Refresh   string
	ExpiresIn int
}

// IssueTokens creates an access + refresh token pair for a grant.
func (s *Store) IssueTokens(ctx context.Context, grantID int64, resource string) (*TokenPair, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := issueTokens(ctx, tx, grantID, resource)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

func issueTokens(ctx context.Context, tx *sql.Tx, grantID int64, resource string) (*TokenPair, error) {
	t := now()
	// Housekeeping: expired codes/tokens are useless, drop them as we go.
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE expires_at <= ?`, t.Format(timeFmt)); err != nil {
		return nil, err
	}
	p := &TokenPair{Access: NewSecret(OAuthAccessPrefix), Refresh: NewSecret(OAuthRefreshPrefix), ExpiresIn: int(AccessTTL.Seconds())}
	for _, row := range []struct {
		tok, kind string
		ttl       time.Duration
	}{{p.Access, "access", AccessTTL}, {p.Refresh, "refresh", RefreshTTL}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_tokens (hash, kind, grant_id, expires_at, resource) VALUES (?, ?, ?, ?, ?)`,
			HashToken(row.tok), row.kind, grantID, t.Add(row.ttl).Format(timeFmt), resource); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// RotateRefresh swaps a refresh token for a new pair. The old refresh token stops working.
func (s *Store) RotateRefresh(ctx context.Context, refresh, clientID string) (*TokenPair, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var grantID int64
	var resource string
	err = tx.QueryRowContext(ctx, `
		DELETE FROM oauth_tokens WHERE hash = ? AND kind = 'refresh' AND expires_at > ?
			AND grant_id IN (SELECT id FROM oauth_grants WHERE client_id = ?)
		RETURNING grant_id, resource`, HashToken(refresh), now().Format(timeFmt), clientID).Scan(&grantID, &resource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p, err := issueTokens(ctx, tx, grantID, resource)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

// VerifyAccess resolves an OAuth access token to its grant and records use.
func (s *Store) VerifyAccess(ctx context.Context, access string) (*Grant, error) {
	var grantID int64
	var resource string
	err := s.DB.QueryRowContext(ctx, `SELECT grant_id, resource FROM oauth_tokens
		WHERE hash = ? AND kind = 'access' AND expires_at > ?`, HashToken(access), now().Format(timeFmt)).
		Scan(&grantID, &resource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.DB.ExecContext(ctx, `UPDATE oauth_grants SET last_used_at = ? WHERE id = ?`, now().Format(timeFmt), grantID)
	g, err := s.grant(ctx, grantID)
	if err != nil {
		return nil, err
	}
	g.Resource = resource
	return g, nil
}

func (s *Store) grant(ctx context.Context, id int64) (*Grant, error) {
	g := &Grant{ID: id}
	var boards string
	err := s.DB.QueryRowContext(ctx, `
		SELECT g.client_id, c.name, g.user_id, g.created_at, g.last_used_at,
			coalesce((SELECT group_concat(board_id) FROM oauth_grant_boards WHERE grant_id = g.id), '')
		FROM oauth_grants g JOIN oauth_clients c ON c.client_id = g.client_id WHERE g.id = ?`, id).
		Scan(&g.ClientID, &g.ClientName, &g.UserID, &g.CreatedAt, &g.LastUsedAt, &boards)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.BoardIDs = parseIDs(boards)
	return g, nil
}

// Grants lists a user's connected apps that still hold a live token.
func (s *Store) Grants(ctx context.Context, userID int64) ([]Grant, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT g.id FROM oauth_grants g WHERE g.user_id = ? AND EXISTS
			(SELECT 1 FROM oauth_tokens t WHERE t.grant_id = g.id AND t.kind IN ('access','refresh') AND t.expires_at > ?)
		ORDER BY g.id`, userID, now().Format(timeFmt))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []Grant{}
	for _, id := range ids {
		g, err := s.grant(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, nil
}

// RevokeGrant deletes a user's grant and every token issued under it.
func (s *Store) RevokeGrant(ctx context.Context, id, userID int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM oauth_grants WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func parseIDs(s string) []int64 {
	out := []int64{}
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.ParseInt(p, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}
