package transcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
}

// makeClip renders a short synthetic clip with ffmpeg's lavfi test source.
func makeClip(t *testing.T, path string, args ...string) {
	t.Helper()
	base := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	out, err := exec.Command("ffmpeg", append(append(base, args...), "-y", path)...).CombinedOutput()
	if err != nil {
		t.Fatalf("making test clip: %v: %s", err, out)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTranscodeDownscalesOnly(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	// no extension, exactly as storage.Download names it
	src := filepath.Join(dir, "source")
	makeClip(t, src, "-f", "lavfi", "-i", "testsrc=size=1280x720:rate=24", "-f", "lavfi", "-i", "sine",
		"-t", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", "-f", "mp4")

	videos, err := Transcode(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"720p", "480p", "360p"}
	if len(videos) != len(want) {
		t.Fatalf("got %d renditions, want %v", len(videos), want)
	}
	for i, v := range videos {
		if v.Resolution != want[i] {
			t.Errorf("rendition %d is %s, want %s", i, v.Resolution, want[i])
		}
		if filepath.Dir(v.FilePath) != dir {
			t.Errorf("rendition written outside the job directory: %s", v.FilePath)
		}
		got, err := probe(context.Background(), v.FilePath)
		if err != nil {
			t.Fatalf("probing rendition %s: %v", v.Resolution, err)
		}
		if got.height != Targets[i+1] {
			t.Errorf("rendition %s is %dpx tall", v.Resolution, got.height)
		}
	}
}

func TestTranscodeAcceptsWebM(t *testing.T) {
	requireFFmpeg(t)
	src := filepath.Join(t.TempDir(), "source")
	makeClip(t, src, "-f", "lavfi", "-i", "testsrc=size=640x360:rate=24", "-t", "1", "-c:v", "libvpx-vp9", "-f", "webm")

	videos, err := Transcode(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 1 || videos[0].Resolution != "360p" {
		t.Fatalf("got %+v, want a single 360p rendition", videos)
	}
}

// Inputs that are not plain media containers must be refused before ffmpeg
// decodes them, whatever the installed ffmpeg's own mitigations happen to be.
func TestTranscodeRejectsNonMediaInputs(t *testing.T) {
	requireFFmpeg(t)

	tests := map[string]string{
		"hls playlist reading a local file": "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:10.0,\nfile:///etc/passwd\n#EXT-X-ENDLIST\n",
		"hls playlist fetching metadata":    "#EXTM3U\n#EXTINF:10.0,\nhttp://169.254.169.254/latest/meta-data/\n#EXT-X-ENDLIST\n",
		"concat script naming a sibling":    "ffconcat version 1.0\nfile sibling.mp4\n",
		"plain text":                        "definitely not a video\n",
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			// a valid sibling makes the concat case one ffmpeg itself would accept
			makeClip(t, filepath.Join(dir, "sibling.mp4"), "-f", "lavfi", "-i", "testsrc=size=640x360", "-t", "1", "-pix_fmt", "yuv420p")
			src := filepath.Join(dir, "source")
			writeFile(t, src, body)

			videos, err := Transcode(context.Background(), src)
			if err == nil {
				t.Fatalf("accepted %s: %+v", name, videos)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "source_") {
					t.Errorf("wrote output %s for rejected input", e.Name())
				}
			}
		})
	}
}

func TestTranscodeRejectsAudioOnly(t *testing.T) {
	requireFFmpeg(t)
	src := filepath.Join(t.TempDir(), "source")
	makeClip(t, src, "-f", "lavfi", "-i", "sine", "-t", "1", "-c:a", "aac", "-f", "mp4")

	if _, err := Transcode(context.Background(), src); err == nil || !strings.Contains(err.Error(), "no video stream") {
		t.Fatalf("expected a no-video-stream error, got %v", err)
	}
}

func TestTranscodeRejectsOversizedFrames(t *testing.T) {
	requireFFmpeg(t)
	src := filepath.Join(t.TempDir(), "source")
	makeClip(t, src, "-f", "lavfi", "-i", "color=size=16x8200", "-frames:v", "1", "-c:v", "ffv1", "-f", "matroska")

	if _, err := Transcode(context.Background(), src); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected a dimension error, got %v", err)
	}
}

func TestTranscodeHonoursContext(t *testing.T) {
	requireFFmpeg(t)
	src := filepath.Join(t.TempDir(), "source")
	makeClip(t, src, "-f", "lavfi", "-i", "testsrc=size=1920x1080:rate=30", "-t", "20", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "mp4")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Transcode(ctx, src); err == nil {
		t.Fatal("expected the transcode to be cut off by the context")
	}
	if elapsed := time.Since(start); elapsed > waitDelay+5*time.Second {
		t.Fatalf("transcode ran %s after its context expired", elapsed)
	}
}
