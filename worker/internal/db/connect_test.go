package db

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequireVerifiedTLS(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		// local development: plaintext is fine on loopback
		{"postgres://u:p@localhost:5432/db?sslmode=disable", false},
		{"postgres://u:p@127.0.0.1:5432/db", false},
		{"postgres://u:p@[::1]:5432/db?sslmode=disable", false},
		{"host=/var/run/postgresql dbname=db", false},

		// remote: only verify-full is accepted
		{"postgres://u:p@db.example.internal:5432/db?sslmode=verify-full", false},
		{"postgres://u:p@db.example.internal:5432/db", true}, // pgx default: prefer
		{"postgres://u:p@db.example.internal:5432/db?sslmode=prefer", true},
		{"postgres://u:p@db.example.internal:5432/db?sslmode=allow", true},
		{"postgres://u:p@db.example.internal:5432/db?sslmode=disable", true},
		{"postgres://u:p@db.example.internal:5432/db?sslmode=require", true},
		{"postgres://u:p@db.example.internal:5432/db?sslmode=verify-ca", true},
		{"postgres://u:p@10.0.16.5:5432/db?sslmode=require", true},

		// every host in a multi-host URL is checked
		{"postgres://u:p@localhost:5432,db.example.internal:5432/db?sslmode=disable", true},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig(tt.url)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = requireVerifiedTLS(&cfg.ConnConfig.Config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr %v, got %v", tt.wantErr, err)
			}
		})
	}
}
