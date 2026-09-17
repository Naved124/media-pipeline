// Package db for creating db connection pool and defining the connection between postgresql and go
package db

import (
	"github.com/jackc/pgx/v5/pgxpool"
)

type Client struct {
	Pool *pgxpool.Pool
}

func NewClient(pool *pgxpool.Pool) *Client {
	return &Client{
		Pool: pool,
	}
}
