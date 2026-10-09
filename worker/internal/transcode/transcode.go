// Package transcode will actuall downscale the video files
package transcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Video struct {
	FilePath   string
	Resolution string
}

var Targets = []int{1080, 720, 480, 360}

// maxDimension rejects frames far larger than any real upload; decoding one
// is a cheap way to exhaust a worker's memory.
const maxDimension = 8192

// waitDelay bounds how long a killed ffmpeg/ffprobe may hold its pipes open
// after the job context is cancelled.
const waitDelay = 5 * time.Second

// allowedFormats maps the format_name ffprobe reports to the demuxer ffmpeg is
// then pinned to. Uploads are untrusted, and some formats ffmpeg auto-detects
// are not media but instructions: HLS and concat playlists make ffmpeg open
// other files or URLs named inside the upload (local file read, SSRF). Only
// plain containers are accepted, and ffmpeg never gets to re-detect.
var allowedFormats = map[string]string{
	"mov,mp4,m4a,3gp,3g2,mj2": "mov",
	"matroska,webm":           "matroska",
}

type source struct {
	demuxer string
	width   int
	height  int
}

type probeOutput struct {
	Streams []struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
	} `json:"format"`
}

func probe(ctx context.Context, localPath string) (source, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-protocol_whitelist", "file",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height:format=format_name",
		"-of", "json",
		localPath) // #nosec G204 -- argv, no shell; localPath is the worker's own temp file
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if err != nil {
		// Output keeps stderr on the ExitError; without it the stored reason
		// is just "exit status 1"
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return source{}, fmt.Errorf("ffprobe results: %w, %s", err, bytes.TrimSpace(exitErr.Stderr))
		}
		return source{}, fmt.Errorf("ffprobe results: %w", err)
	}

	var p probeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return source{}, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	demuxer, ok := allowedFormats[p.Format.FormatName]
	if !ok {
		return source{}, fmt.Errorf("unsupported input format %q", p.Format.FormatName)
	}
	if len(p.Streams) == 0 {
		return source{}, fmt.Errorf("no video stream")
	}
	w, h := p.Streams[0].Width, p.Streams[0].Height
	if w <= 0 || h <= 0 || w > maxDimension || h > maxDimension {
		return source{}, fmt.Errorf("video dimensions %dx%d outside 1..%d", w, h, maxDimension)
	}

	return source{demuxer: demuxer, width: w, height: h}, nil
}

func Transcode(ctx context.Context, localPath string) ([]Video, error) {

	results := []Video{}

	src, err := probe(ctx, localPath)
	if err != nil {
		return nil, fmt.Errorf("probing a source height: %w", err)
	}

	for _, height := range Targets {
		if height > src.height {
			continue
		}

		stem := strings.TrimSuffix(localPath, filepath.Ext(localPath))
		outputPath := fmt.Sprintf("%s_%dp.mp4", stem, height)

		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-nostdin", "-hide_banner", "-loglevel", "error",
			"-protocol_whitelist", "file",
			"-f", src.demuxer,
			"-i", localPath,
			"-vf", "scale=-2:"+strconv.Itoa(height),
			"-c:a", "copy",
			"-y", outputPath) // #nosec G204 -- argv, no shell; every value is built by the worker
		cmd.WaitDelay = waitDelay

		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("ffmpeg results: %w, %s", err, output)
		}
		results = append(results, Video{
			FilePath:   outputPath,
			Resolution: fmt.Sprintf("%dp", height),
		})
	}
	return results, nil

}
