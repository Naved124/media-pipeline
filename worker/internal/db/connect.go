package db

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect builds the pool once, refuses connection settings that could expose
// traffic to the network, and checks the database answers before returning.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}
	if err := requireVerifiedTLS(&cfg.ConnConfig.Config); err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reaching database: %w", err)
	}
	return pool, nil
}

// requireVerifiedTLS rejects any target off this host that isn't
// sslmode=verify-full. pgx defaults to sslmode=prefer, which never checks the
// server certificate and silently falls back to plaintext, so credentials and
// job data could be read or altered on the path to RDS.
func requireVerifiedTLS(cfg *pgconn.Config) error {
	type target struct {
		host string
		tls  *tls.Config
	}
	targets := []target{{cfg.Host, cfg.TLSConfig}}
	for _, fb := range cfg.Fallbacks {
		targets = append(targets, target{fb.Host, fb.TLSConfig})
	}

	for _, t := range targets {
		if isLocal(t.host) {
			continue
		}
		// verify-full is the only mode that sets a TLS config without
		// InsecureSkipVerify; require, verify-ca and prefer all skip
		// hostname verification, and prefer/allow add plaintext fallbacks
		if t.tls == nil || t.tls.InsecureSkipVerify {
			return fmt.Errorf("database host %q is not local: DATABASE_URL must use sslmode=verify-full", t.host)
		}
	}
	return nil
}

// isLocal reports whether host is a unix socket directory or a loopback address.
func isLocal(host string) bool {
	if strings.HasPrefix(host, "/") || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
