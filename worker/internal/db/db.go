// Package db for creating db connection pool and defining the connection between postgresql and go
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// maxErrorLen caps error_message so a chatty ffmpeg failure can't bloat the row.
const maxErrorLen = 4096

type Client struct {
	Pool *pgxpool.Pool
}

// Output is one rendition as stored in jobs.output_keys.
type Output struct {
	Resolution string `json:"resolution"`
	Key        string `json:"key"`
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

// CompleteJob moves a processing job to completed and records its renditions.
func (c *Client) CompleteJob(ctx context.Context, jobID string, outputs []Output) error {
	outputKeys, err := json.Marshal(outputs)
	if err != nil {
		return fmt.Errorf("failed to encode outputs for job %s: %w", jobID, err)
	}

	query := `UPDATE jobs SET status = 'completed', output_keys = $2, completed_at = now()
	           WHERE job_id = $1 AND status = 'processing'`

	tag, err := c.Pool.Exec(ctx, query, jobID, outputKeys)
	if err != nil {
		return fmt.Errorf("failed to complete job %s: %w", jobID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("failed to complete job %s: no processing row found", jobID)
	}
	return nil
}

// FailJob moves a processing job to failed and records why.
func (c *Client) FailJob(ctx context.Context, jobID string, reason string) error {
	query := `UPDATE jobs SET status = 'failed', error_message = $2
	           WHERE job_id = $1 AND status = 'processing'`

	tag, err := c.Pool.Exec(ctx, query, jobID, cleanError(reason))
	if err != nil {
		return fmt.Errorf("failed to mark job %s failed: %w", jobID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("failed to mark job %s failed: no processing row found", jobID)
	}
	return nil
}

// cleanError makes an error string safe for a TEXT column: Postgres rejects
// NUL bytes and invalid UTF-8, and tool output can be arbitrarily long.
func cleanError(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > maxErrorLen {
		s = s[:maxErrorLen]
	}
	// Truncation can split a multi-byte rune; this also drops the broken tail.
	return strings.ToValidUTF8(s, "")
}
