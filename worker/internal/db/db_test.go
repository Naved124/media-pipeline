package db

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testClient connects to TEST_DATABASE_URL and applies the real migration into
// a throwaway schema, so the test exercises the schema the worker runs against.
func testClient(t *testing.T) *Client {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	admin, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	migration, err := os.ReadFile("../../../db/migrations/0001_create_jobs_table.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	return NewClient(pool)
}

type jobRow struct {
	status      string
	outputKeys  []byte
	startedAt   *time.Time
	completedAt *time.Time
	errMessage  *string
}

func readJob(t *testing.T, c *Client, jobID string) jobRow {
	t.Helper()
	var r jobRow
	err := c.Pool.QueryRow(context.Background(),
		`SELECT status, output_keys, started_at, completed_at, error_message FROM jobs WHERE job_id = $1`, jobID).
		Scan(&r.status, &r.outputKeys, &r.startedAt, &r.completedAt, &r.errMessage)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestJobCompletes(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	jobID := uuid.NewString()

	if err := c.CreateJob(ctx, jobID, "uploads/my video.mp4"); err != nil {
		t.Fatal(err)
	}
	if r := readJob(t, c, jobID); r.status != "processing" || r.startedAt == nil || r.completedAt != nil {
		t.Fatalf("after create: %+v", r)
	}

	outputs := []Output{{"720p", jobID + "/720p.mp4"}, {"480p", jobID + "/480p.mp4"}}
	if err := c.CompleteJob(ctx, jobID, outputs); err != nil {
		t.Fatal(err)
	}
	r := readJob(t, c, jobID)
	if r.status != "completed" || r.completedAt == nil {
		t.Fatalf("after complete: %+v", r)
	}
	var got []Output
	if err := json.Unmarshal(r.outputKeys, &got); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(outputs) {
		t.Fatalf("output_keys = %+v, want %+v", got, outputs)
	}

	// a terminal job can't be moved again
	if err := c.FailJob(ctx, jobID, "late failure"); err == nil {
		t.Fatal("FailJob overwrote a completed job")
	}
	if err := c.CompleteJob(ctx, jobID, outputs); err == nil {
		t.Fatal("CompleteJob ran twice")
	}
}

func TestJobFailsWithUntrustedErrorText(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	jobID := uuid.NewString()

	if err := c.CreateJob(ctx, jobID, "clip.mp4"); err != nil {
		t.Fatal(err)
	}
	// NUL bytes, invalid UTF-8 and oversized output, as ffmpeg stderr can contain
	reason := "ffmpeg: \x00bad\xffbytes " + strings.Repeat("é", maxErrorLen)
	if err := c.FailJob(ctx, jobID, reason); err != nil {
		t.Fatal(err)
	}
	r := readJob(t, c, jobID)
	if r.status != "failed" || r.errMessage == nil {
		t.Fatalf("after fail: %+v", r)
	}
	if len(*r.errMessage) > maxErrorLen {
		t.Fatalf("error_message is %d bytes, cap is %d", len(*r.errMessage), maxErrorLen)
	}
	if !strings.HasPrefix(*r.errMessage, "ffmpeg: badbytes") {
		t.Fatalf("error_message = %.40q", *r.errMessage)
	}
}

func TestCompleteUnknownJob(t *testing.T) {
	c := testClient(t)
	if err := c.CompleteJob(context.Background(), uuid.NewString(), nil); err == nil {
		t.Fatal("expected an error for a job that was never created")
	}
}

func TestInputKeyIsParameterised(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	jobID := uuid.NewString()
	key := `x'); DROP TABLE jobs; --`

	if err := c.CreateJob(ctx, jobID, key); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := c.Pool.QueryRow(ctx, `SELECT input_key FROM jobs WHERE job_id = $1`, jobID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != key {
		t.Fatalf("stored %q, want %q", stored, key)
	}
}
