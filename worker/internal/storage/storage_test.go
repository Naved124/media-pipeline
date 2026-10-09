package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeS3 serves body for any GetObject. When advertisedLen is set it is sent
// as Content-Length; otherwise the body is streamed without one.
func fakeS3(t *testing.T, body string, advertisedLen int) *s3.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if advertisedLen > 0 {
			w.Header().Set("Content-Length", strconv.Itoa(advertisedLen))
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return s3.New(s3.Options{
		Region:       "ap-south-1",
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		Credentials:  aws.AnonymousCredentials{},
	})
}

func TestDownloadStaysInsideJobDir(t *testing.T) {
	for _, key := range []string{"clip.mp4", "../../etc/passwd", "/abs/path.mp4", "..", ".", "a/b/c/../../../../x"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			c := NewClient(fakeS3(t, "video bytes", 0), "in", "out", 1024)

			path, err := c.Download(context.Background(), key, dir)
			if err != nil {
				t.Fatal(err)
			}
			if path != filepath.Join(dir, sourceName) {
				t.Fatalf("downloaded to %s, want %s", path, filepath.Join(dir, sourceName))
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode := info.Mode().Perm(); mode != 0o600 {
				t.Errorf("file mode %o, want 600", mode)
			}
		})
	}
}

func TestDownloadRefusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	// a symlink planted where the download goes must not be followed
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, sourceName)); err != nil {
		t.Fatal(err)
	}
	c := NewClient(fakeS3(t, "video bytes", 0), "in", "out", 1024)
	if _, err := c.Download(context.Background(), "clip.mp4", dir); err == nil {
		t.Fatal("expected O_EXCL to refuse an existing path")
	}
}

func TestDownloadEnforcesSizeLimit(t *testing.T) {
	big := strings.Repeat("x", 2048)

	t.Run("advertised length over limit", func(t *testing.T) {
		c := NewClient(fakeS3(t, big, len(big)), "in", "out", 1024)
		if _, err := c.Download(context.Background(), "big.mp4", t.TempDir()); err == nil {
			t.Fatal("expected a size error")
		}
	})

	t.Run("streamed body over limit", func(t *testing.T) {
		c := NewClient(fakeS3(t, big, 0), "in", "out", 1024)
		if _, err := c.Download(context.Background(), "big.mp4", t.TempDir()); err == nil {
			t.Fatal("expected a size error")
		}
	})

	t.Run("exactly at limit", func(t *testing.T) {
		c := NewClient(fakeS3(t, big[:1024], 1024), "in", "out", 1024)
		if _, err := c.Download(context.Background(), "ok.mp4", t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
}
