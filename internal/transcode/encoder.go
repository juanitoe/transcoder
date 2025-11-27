package transcode

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"transcoder/internal/config"
)

// Encoder handles video transcoding with FFmpeg
type Encoder struct {
	cfg *config.Config
}

// New creates a new Encoder instance
func New(cfg *config.Config) *Encoder {
	return &Encoder{
		cfg: cfg,
	}
}

// TranscodeProgress represents real-time transcoding progress
type TranscodeProgress struct {
	Frame     int64
	FPS       float64
	Bitrate   string
	TotalSize int64
	Time      string
	Speed     float64
	Progress  float64 // 0-100
}

// ProgressCallback is called periodically during transcoding
type ProgressCallback func(progress TranscodeProgress)

// Transcode transcodes a video file to HEVC
// durationUs is the total duration in microseconds for progress calculation
func (e *Encoder) Transcode(ctx context.Context, inputPath, outputPath string, durationUs int64, progressCb ProgressCallback) error {
	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Build FFmpeg command
	args := e.buildFFmpegArgs(inputPath, outputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	// Create pipe for stderr (where FFmpeg sends progress)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start FFmpeg
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	// Parse progress from stderr (-progress pipe:2 output format)
	// Format is key=value pairs, with each block ending in progress=continue or progress=end
	scanner := bufio.NewScanner(stderrPipe)

	go func() {
		progressData := make(map[string]string)

		for scanner.Scan() {
			line := scanner.Text()

			// Parse key=value pairs
			if idx := strings.Index(line, "="); idx > 0 {
				key := line[:idx]
				value := line[idx+1:]
				progressData[key] = value

				// When we receive progress=continue or progress=end, emit update
				if key == "progress" {
					// Parse accumulated values
					frame, _ := strconv.ParseInt(progressData["frame"], 10, 64)
					fps, _ := strconv.ParseFloat(progressData["fps"], 64)
					totalSize, _ := strconv.ParseInt(progressData["total_size"], 10, 64)
					timeStr := progressData["out_time"]
					bitrateStr := progressData["bitrate"]
					outTimeUs, _ := strconv.ParseInt(progressData["out_time_us"], 10, 64)

					// Parse speed (remove 'x' suffix)
					speedStr := strings.TrimSuffix(progressData["speed"], "x")
					speed, _ := strconv.ParseFloat(speedStr, 64)

					// Calculate progress percentage using time (more reliable than frames)
					var progressPercent float64
					if durationUs > 0 && outTimeUs > 0 {
						progressPercent = (float64(outTimeUs) / float64(durationUs)) * 100
						if progressPercent > 100 {
							progressPercent = 100
						}
					}

					// Report progress
					if progressCb != nil {
						progressCb(TranscodeProgress{
							Frame:     frame,
							FPS:       fps,
							Bitrate:   bitrateStr,
							TotalSize: totalSize,
							Time:      timeStr,
							Speed:     speed,
							Progress:  progressPercent,
						})
					}

					// Reset for next progress block
					progressData = make(map[string]string)
				}
			}
		}
	}()

	// Wait for FFmpeg to complete
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg failed: %w", err)
	}

	return nil
}

// buildFFmpegArgs builds the FFmpeg command arguments
func (e *Encoder) buildFFmpegArgs(inputPath, outputPath string) []string {
	args := []string{
		"-y", // Overwrite output file
		"-i", inputPath,
		"-c:v", e.cfg.Encoder.Codec, // Video codec (hevc_videotoolbox)
		"-progress", "pipe:2", // Send progress to stderr
		"-stats_period", "0.5", // Update stats every 0.5 seconds
	}

	// Quality settings
	// For videotoolbox, use -q:v (0-100, where 100 is best quality)
	if strings.Contains(e.cfg.Encoder.Codec, "videotoolbox") {
		args = append(args, "-q:v", strconv.Itoa(e.cfg.Encoder.Quality))
	} else {
		// For software encoders, use CRF (0-51, where 0 is lossless)
		crf := 51 - int(float64(e.cfg.Encoder.Quality)/100.0*51)
		args = append(args, "-crf", strconv.Itoa(crf))
	}

	// Preset (if applicable)
	if e.cfg.Encoder.Preset != "" && !strings.Contains(e.cfg.Encoder.Codec, "videotoolbox") {
		args = append(args, "-preset", e.cfg.Encoder.Preset)
	}

	// Copy audio streams
	args = append(args, "-c:a", "copy")

	// Copy subtitle streams
	args = append(args, "-c:s", "copy")

	// Map all streams
	args = append(args, "-map", "0")

	// Output file
	args = append(args, outputPath)

	return args
}

// Verify checks if a transcoded file is valid and playable
func (e *Encoder) Verify(ctx context.Context, filePath string) error {
	_, err := e.GetDuration(ctx, filePath)
	return err
}

// GetDuration returns the duration of a video file in seconds
func (e *Encoder) GetDuration(ctx context.Context, filePath string) (float64, error) {
	// Use ffprobe to get the duration
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("verification failed: %w", err)
	}

	// Check if we got a valid duration
	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid video duration: %s", durationStr)
	}

	return duration, nil
}

// VerifyDuration checks if a transcoded file has the expected duration (within tolerance)
func (e *Encoder) VerifyDuration(ctx context.Context, filePath string, expectedDuration float64) error {
	actualDuration, err := e.GetDuration(ctx, filePath)
	if err != nil {
		return err
	}

	// Allow 1 second tolerance or 1% difference, whichever is greater
	tolerance := expectedDuration * 0.01
	if tolerance < 1.0 {
		tolerance = 1.0
	}

	diff := expectedDuration - actualDuration
	if diff < 0 {
		diff = -diff
	}

	if diff > tolerance {
		return fmt.Errorf("duration mismatch: expected %.1fs, got %.1fs (diff: %.1fs)", expectedDuration, actualDuration, diff)
	}

	return nil
}

// GetFileInfo returns basic file information
func (e *Encoder) GetFileInfo(filePath string) (*FileInfo, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	return &FileInfo{
		Path:         filePath,
		Size:         stat.Size(),
		ModifiedTime: stat.ModTime(),
	}, nil
}

// FileInfo contains basic file information
type FileInfo struct {
	Path         string
	Size         int64
	ModifiedTime time.Time
}

// CalculateSavings calculates the space saved by transcoding
func CalculateSavings(originalSize, transcodedSize int64) (bytesSaved int64, percentSaved float64) {
	bytesSaved = originalSize - transcodedSize
	if originalSize > 0 {
		percentSaved = (float64(bytesSaved) / float64(originalSize)) * 100
	}
	return bytesSaved, percentSaved
}

// EstimateTranscodingSavings estimates whether transcoding is worthwhile
// Returns: estimated savings percentage, whether to proceed, and a reason
func EstimateTranscodingSavings(codec string, bitrateKbps int, resolutionWidth, resolutionHeight int, quality int) (estimatedSavings int, shouldProceed bool, reason string) {
	// If we don't have bitrate info, proceed with transcoding
	if bitrateKbps <= 0 {
		return 30, true, "unknown bitrate, assuming worthwhile"
	}

	// Use width-based thresholds for resolution detection
	// This handles widescreen aspect ratios (2.35:1) where 1920-wide videos
	// have fewer pixels than 1920x1080 but should still be treated as 1080p
	var minHEVCBitrate int
	switch {
	case resolutionWidth >= 3840: // 4K
		minHEVCBitrate = 8000 // 8 Mbps minimum for good 4K HEVC
	case resolutionWidth >= 1920: // 1080p (includes widescreen like 1920x800)
		minHEVCBitrate = 3000 // 3 Mbps minimum for good 1080p HEVC
	case resolutionWidth >= 1280: // 720p
		minHEVCBitrate = 1500 // 1.5 Mbps minimum for good 720p HEVC
	default: // SD
		minHEVCBitrate = 800 // 800 kbps minimum for SD HEVC
	}

	// Adjust minimum based on quality setting (0-100)
	// Higher quality = higher minimum bitrate
	qualityFactor := float64(quality) / 75.0 // Normalize around default quality 75
	minHEVCBitrate = int(float64(minHEVCBitrate) * qualityFactor)

	// Estimate expected HEVC bitrate based on source codec
	var expectedHEVCBitrate int

	codec = strings.ToLower(codec)
	switch codec {
	case "hevc", "h265", "hev1":
		// Already HEVC - no point transcoding
		return 0, false, "already HEVC"

	case "h264", "avc", "avc1":
		// H.264 -> HEVC: typically 30-40% savings
		expectedHEVCBitrate = int(float64(bitrateKbps) * 0.65) // ~35% smaller

	case "mpeg2", "mpeg2video":
		// MPEG2 -> HEVC: typically 50-60% savings
		expectedHEVCBitrate = int(float64(bitrateKbps) * 0.45) // ~55% smaller

	case "mpeg4", "msmpeg4", "divx", "xvid":
		// MPEG4 -> HEVC: typically 40-50% savings
		expectedHEVCBitrate = int(float64(bitrateKbps) * 0.55) // ~45% smaller

	case "vp9":
		// VP9 -> HEVC: VP9 is similar to HEVC
		expectedHEVCBitrate = int(float64(bitrateKbps) * 0.95) // ~5% smaller at best

	case "vp8":
		// VP8 -> HEVC: VP8 is like H.264
		expectedHEVCBitrate = int(float64(bitrateKbps) * 0.65)

	default:
		// Unknown codec - assume moderate savings
		expectedHEVCBitrate = int(float64(bitrateKbps) * 0.60)
	}

	// If expected HEVC bitrate is below minimum for quality, use minimum
	if expectedHEVCBitrate < minHEVCBitrate {
		expectedHEVCBitrate = minHEVCBitrate
	}

	// Calculate actual expected savings
	if expectedHEVCBitrate >= bitrateKbps {
		// Transcoded file would be same size or larger
		return 0, false, fmt.Sprintf("source bitrate (%d kbps) already efficient for %s", bitrateKbps, codec)
	}

	actualSavingsPercent := int((1.0 - float64(expectedHEVCBitrate)/float64(bitrateKbps)) * 100)

	return actualSavingsPercent, true, fmt.Sprintf("estimated %d%% savings (%s %d kbps -> HEVC ~%d kbps)",
		actualSavingsPercent, codec, bitrateKbps, expectedHEVCBitrate)
}
