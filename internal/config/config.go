// Package config reads workdesk settings from the environment (.env via docker compose).
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	BaseURL            string
	ListenAddr         string
	DBPath             string
	GoogleClientID     string
	GoogleClientSecret string
	SessionSecret      []byte
	AdminEmails        map[string]bool
	// Feedback button: issues are filed in FeedbackRepo ("owner/repo") with FeedbackToken, a
	// fine-grained GitHub token limited to Issues: read and write on that repo. Optional.
	FeedbackRepo  string
	FeedbackToken string
	GitHubAPI     string
}

func FromEnv() (*Config, error) {
	c := &Config{
		BaseURL:            strings.TrimRight(getenv("WORKDESK_BASE_URL", "http://localhost:8080"), "/"),
		ListenAddr:         getenv("WORKDESK_LISTEN_ADDR", ":8080"),
		DBPath:             getenv("WORKDESK_DB_PATH", "/data/workdesk.db"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		SessionSecret:      []byte(os.Getenv("WORKDESK_SESSION_SECRET")),
		AdminEmails:        ParseEmails(os.Getenv("WORKDESK_ADMIN_EMAILS")),
		FeedbackRepo:       getenv("WORKDESK_FEEDBACK_REPO", "paytonmurray51/workdesk"),
		FeedbackToken:      os.Getenv("WORKDESK_FEEDBACK_GITHUB_TOKEN"),
		GitHubAPI:          "https://api.github.com",
	}
	var errs []error
	if _, err := url.ParseRequestURI(c.BaseURL); err != nil {
		errs = append(errs, fmt.Errorf("WORKDESK_BASE_URL: %w", err))
	}
	if c.GoogleClientID == "" || c.GoogleClientSecret == "" {
		errs = append(errs, errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET are required"))
	}
	if len(c.SessionSecret) < 32 {
		errs = append(errs, errors.New("WORKDESK_SESSION_SECRET must be at least 32 characters"))
	}
	if len(c.AdminEmails) == 0 {
		errs = append(errs, errors.New("WORKDESK_ADMIN_EMAILS must list at least one email"))
	}
	return c, errors.Join(errs...)
}

// Secure reports whether cookies should carry the Secure flag.
func (c *Config) Secure() bool { return strings.HasPrefix(c.BaseURL, "https://") }

func ParseEmails(s string) map[string]bool {
	m := map[string]bool{}
	for _, e := range strings.Split(s, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			m[e] = true
		}
	}
	return m
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
