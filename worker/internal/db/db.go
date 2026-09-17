// Package db for creating db connection pool and defining the connection between postgresql and go
package db

import (
	"context"
	"fmt"

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

func (c *Client) CreateJob(ctx context.Context, jobID string, key string) error {
	query := `INSERT INTO jobs (job_id, input_key, status, started_at) VALUES($1, $2,'processing', now())`

	_, err := c.Pool.Exec(ctx, query, jobID, key)
	if err != nil {
		return fmt.Errorf("failed to create job %s: %w", jobID, err)
	}
	return nil

}
