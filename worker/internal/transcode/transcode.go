// Package transcode will actuall downscale the video files
package transcode

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Video struct {
	FilePath   string
	Resolution string
}

var Targets = []int{1080, 720, 480, 360}

func resolution(localPath string) (int, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=height", "-of", "csv=p=0", localPath)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe results: %w", err)
	}
	height, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parsing ffprobe height: %w", err)
	}

	return height, nil
}

func Transcode(localPath string) ([]Video, error) {

	results := []Video{}

	sourceHeight, err := resolution(localPath)
	if err != nil {
		return nil, fmt.Errorf("probing a source height: %w", err)
	}

	for _, height := range Targets {
		if height > sourceHeight {
			continue
		}

		stem := strings.TrimSuffix(localPath, filepath.Ext(localPath))
		outputPath := fmt.Sprintf("%s_%dp.mp4", stem, height)

		cmd := exec.Command("ffmpeg", "-i", localPath, "-vf", fmt.Sprintf("scale=-2:%d", height), "-c:a", "copy", "-y", outputPath)

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
