package neondb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
}

type Client struct { db *sql.DB }

func Open(ctx context.Context, cfg Config) (*Client, error) {
	dsn, err := validateDSN(cfg.URL)
	if err != nil { return nil, err }
	if cfg.MaxOpenConns == 0 { cfg.MaxOpenConns = 8 }
	if cfg.MaxIdleConns == 0 { cfg.MaxIdleConns = 2 }
	if cfg.MaxOpenConns < 1 || cfg.MaxOpenConns > 32 || cfg.MaxIdleConns < 0 || cfg.MaxIdleConns > cfg.MaxOpenConns {
		return nil, errors.New("Neon pool limits are invalid (max open 1..32; max idle 0..max open)")
	}
	if cfg.ConnMaxLifetime == 0 { cfg.ConnMaxLifetime = 15 * time.Minute }
	if cfg.ConnMaxLifetime < time.Minute || cfg.ConnMaxLifetime > time.Hour {
		return nil, errors.New("Neon connection lifetime must be between 1 minute and 1 hour")
	}
	if cfg.ConnectTimeout == 0 { cfg.ConnectTimeout = 5 * time.Second }
	if cfg.ConnectTimeout < time.Second || cfg.ConnectTimeout > 30*time.Second {
		return nil, errors.New("Neon connect timeout must be between 1 and 30 seconds")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil { return nil, fmt.Errorf("configure Neon PostgreSQL client: %w", err) }
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(2 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to Neon PostgreSQL: %w", err)
	}
	return &Client{db: db}, nil
}

func validateDSN(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" { return "", errors.New("NEON_DATABASE_URL is required") }
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.Path == "" {
		return "", errors.New("NEON_DATABASE_URL must be a PostgreSQL URL with host, database and credentials")
	}
	if u.Fragment != "" { return "", errors.New("NEON_DATABASE_URL must not contain a fragment") }
	query := u.Query()
	sslmode := strings.ToLower(strings.TrimSpace(query.Get("sslmode")))
	if isLocalHost(u.Hostname()) {
		if sslmode == "" { query.Set("sslmode", "disable") }
	} else if sslmode != "require" && sslmode != "verify-ca" && sslmode != "verify-full" {
		return "", errors.New("remote NEON_DATABASE_URL must set sslmode=require or stronger")
	}
	if timeout := query.Get("connect_timeout"); timeout != "" {
		if n, err := strconv.Atoi(timeout); err != nil || n < 1 || n > 30 {
			return "", errors.New("PostgreSQL connect_timeout must be between 1 and 30 seconds")
		}
	} else { query.Set("connect_timeout", "5") }
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func isLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.db == nil { return errors.New("Neon PostgreSQL client is not initialized") }
	return c.db.PingContext(ctx)
}
func (c *Client) Close() error {
	if c == nil || c.db == nil { return nil }
	return c.db.Close()
}
// DB exposes the pool to repositories in the Backend process only.
func (c *Client) DB() *sql.DB {
	if c == nil { return nil }
	return c.db
}
