package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Naved124/media-pipeline-worker/internal/db"
	"github.com/Naved124/media-pipeline-worker/internal/queue"
	"github.com/Naved124/media-pipeline-worker/internal/storage"
)

// fakeAWS stands in for the two HTTP APIs processJob calls: S3 GetObject and
// PutObject (path-style), and SQS DeleteMessage (JSON protocol). It records
// what the worker did so the test can check order and effects.
type fakeAWS struct {
	mu       sync.Mutex
	input    []byte
	uploads  []string
	deleted  []string
	sawPutAt int // len(uploads) when the delete arrived
}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.DeleteMessage") {
		body, _ := io.ReadAll(r.Body)
		f.deleted = append(f.deleted, string(body))
		f.sawPutAt = len(f.uploads)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(`{}`))
		return
	}
	switch r.Method {
	case http.MethodGet:
		_, _ = w.Write(f.input)
	case http.MethodPut:
		_, _ = io.Copy(io.Discard, r.Body)
		f.uploads = append(f.uploads, r.URL.Path)
	default:
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}
}

func testWorker(t *testing.T, input []byte) (*worker, *fakeAWS, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}

	fake := &fakeAWS{input: input}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	s3Client := s3.New(s3.Options{
		Region: "ap-south-1", BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true, Credentials: aws.AnonymousCredentials{},
	})
	sqsClient := sqs.New(sqs.Options{
		Region: "ap-south-1", BaseEndpoint: aws.String(srv.URL),
		Credentials: aws.AnonymousCredentials{},
	})

	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migration, err := os.ReadFile("../../../db/migrations/0001_create_jobs_table.sql")
	if err != nil {
		t.Fatal(err)
	}
	// shared database: create the table only if an earlier run hasn't
	if _, err := pool.Exec(context.Background(), strings.Replace(string(migration), "CREATE TABLE jobs", "CREATE TABLE IF NOT EXISTS jobs", 1)); err != nil {
		t.Fatal(err)
	}

	return &worker{
		QueueClient:   queue.NewClient(sqsClient, srv.URL+"/000000000000/jobs"),
		StorageClient: storage.NewClient(s3Client, "in", "out", 64<<20),
		DBClient:      db.NewClient(pool),
	}, fake, pool
}

func clip(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.mp4")
	out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=854x480:rate=24", "-t", "1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", path).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func jobsFor(t *testing.T, pool *pgxpool.Pool, key string) (status []string, errs []string) {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT status, coalesce(error_message, '') FROM jobs WHERE input_key = $1`, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s, e string
		if err := rows.Scan(&s, &e); err != nil {
			t.Fatal(err)
		}
		status, errs = append(status, s), append(errs, e)
	}
	return status, errs
}

func TestProcessJobCompletesThenDeletes(t *testing.T) {
	w, fake, pool := testWorker(t, clip(t))
	key := uniqueKey(t, " clip.mp4")

	err := w.processJob(context.Background(), queue.Message{Bucket: "in", ObjectKey: key, ReceiptHandle: "rh-ok"})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(fake.uploads)
	if len(fake.uploads) != 2 || !strings.HasSuffix(fake.uploads[0], "/360p.mp4") || !strings.HasSuffix(fake.uploads[1], "/480p.mp4") {
		t.Fatalf("uploads = %v, want 360p and 480p under one job prefix", fake.uploads)
	}
	if len(fake.deleted) != 1 || !strings.Contains(fake.deleted[0], "rh-ok") {
		t.Fatalf("deletes = %v, want exactly the received handle", fake.deleted)
	}
	if fake.sawPutAt != 2 {
		t.Fatalf("message deleted after %d of 2 uploads", fake.sawPutAt)
	}
	if status, _ := jobsFor(t, pool, key); len(status) != 1 || status[0] != "completed" {
		t.Fatalf("job rows = %v, want one completed", status)
	}
	assertNoJobDirsLeft(t)
}

func TestProcessJobFailureKeepsMessage(t *testing.T) {
	playlist := []byte("#EXTM3U\n#EXTINF:10.0,\nfile:///etc/passwd\n#EXT-X-ENDLIST\n")
	w, fake, pool := testWorker(t, playlist)
	key := uniqueKey(t, ".mp4")

	if err := w.processJob(context.Background(), queue.Message{Bucket: "in", ObjectKey: key, ReceiptHandle: "rh-bad"}); err == nil {
		t.Fatal("expected the playlist to be rejected")
	}
	if len(fake.uploads) != 0 || len(fake.deleted) != 0 {
		t.Fatalf("uploads %v, deletes %v: a failed job must do neither", fake.uploads, fake.deleted)
	}
	status, errs := jobsFor(t, pool, key)
	// rejected by ffprobe itself or by the format allowlist, depending on the
	// ffmpeg build; either way the reason must be recorded, not just an exit code
	if len(status) != 1 || status[0] != "failed" || !strings.Contains(errs[0], "probing") || strings.HasSuffix(errs[0], "exit status 1") {
		t.Fatalf("job rows = %v %v, want one failed with the reason", status, errs)
	}
	assertNoJobDirsLeft(t)
}

func TestProcessJobRejectsForeignBucket(t *testing.T) {
	w, fake, pool := testWorker(t, clip(t))
	key := uniqueKey(t, ".mp4")

	if err := w.processJob(context.Background(), queue.Message{Bucket: "someone-elses", ObjectKey: key, ReceiptHandle: "rh"}); err == nil {
		t.Fatal("expected a bucket mismatch error")
	}
	if status, _ := jobsFor(t, pool, key); len(status) != 0 || len(fake.deleted) != 0 {
		t.Fatalf("rows %v, deletes %v: a foreign event must not start a job", status, fake.deleted)
	}
}

// uniqueKey keeps rows from earlier runs in the shared database out of the count.
func uniqueKey(t *testing.T, suffix string) string {
	return "uploads/" + t.Name() + "-" + uuid.NewString() + suffix
}

// assertNoJobDirsLeft checks the per-job work directories were removed.
func assertNoJobDirsLeft(t *testing.T) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "job-*"))
	if len(left) != 0 {
		t.Fatalf("job directories left behind: %v", left)
	}
}
