package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"transcoder/internal/types"
)

// FFprobeOutput represents the JSON output from ffprobe
type FFprobeOutput struct {
	Format  FFprobeFormat   `json:"format"`
	Streams []FFprobeStream `json:"streams"`
}

type FFprobeFormat struct {
	Filename string            `json:"filename"`
	Size     string            `json:"size"`
	Duration string            `json:"duration"`
	BitRate  string            `json:"bit_rate"`
	Tags     map[string]string `json:"tags"`
}

type FFprobeStream struct {
	Index         int               `json:"index"`
	CodecName     string            `json:"codec_name"`
	CodecLongName string            `json:"codec_long_name"`
	CodecType     string            `json:"codec_type"` // video, audio, subtitle
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	RFrameRate    string            `json:"r_frame_rate"`
	BitRate       string            `json:"bit_rate"`
	Duration      string            `json:"duration"`
	Tags          map[string]string `json:"tags"`
	ChannelLayout string            `json:"channel_layout"`
	Channels      int               `json:"channels"`
}

// ExtractMetadataWithFFprobe extracts full metadata from a remote video file using ffprobe via SSH
func (s *Scanner) ExtractMetadataWithFFprobe(ctx context.Context, filePath string) (*types.MediaFile, error) {
	// Get an SSH client from the pool
	sshClient := s.sshPool.get()
	defer s.sshPool.put(sshClient)

	// Create SSH session for this command
	session, err := sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer func() { _ = session.Close() }()

	// Escape single quotes in the path and wrap in single quotes
	escapedPath := strings.ReplaceAll(filePath, "'", "'\\''")
	remoteCmd := fmt.Sprintf("ffprobe -v quiet -print_format json -show_format -show_streams '%s'", escapedPath)

	s.logDebug("Running ffprobe via pooled SSH: %s", remoteCmd)

	// Execute command
	output, err := session.CombinedOutput(remoteCmd)
	if err != nil {
		s.logDebug("ffprobe command failed: %v", err)
		s.logDebug("ffprobe output: %s", string(output))
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	s.logDebug("ffprobe succeeded, output length: %d bytes", len(output))

	return s.parseFFprobeOutput(output, filePath)
}

// ExtractMetadataWithLocalFFprobe extracts metadata from a local video file using ffprobe
func (s *Scanner) ExtractMetadataWithLocalFFprobe(ctx context.Context, filePath string) (*types.MediaFile, error) {
	s.logDebug("Running local ffprobe for: %s", filePath)

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	)

	output, err := cmd.Output()
	if err != nil {
		s.logDebug("ffprobe command failed: %v", err)
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	s.logDebug("ffprobe succeeded, output length: %d bytes", len(output))

	return s.parseFFprobeOutput(output, filePath)
}

// parseFFprobeOutput parses ffprobe JSON output into a MediaFile
func (s *Scanner) parseFFprobeOutput(output []byte, filePath string) (*types.MediaFile, error) {
	// Parse JSON output
	var probe FFprobeOutput
	if err := json.Unmarshal(output, &probe); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	// Extract metadata
	mediaFile := &types.MediaFile{
		FilePath:     filePath,
		FileName:     filepath.Base(filePath),
		DiscoveredAt: time.Now(),
		UpdatedAt:    time.Now(),
	}

	// Parse file size
	if size, err := strconv.ParseInt(probe.Format.Size, 10, 64); err == nil {
		mediaFile.FileSizeBytes = size
	}

	// Parse duration
	if duration, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil {
		mediaFile.DurationSeconds = duration
	}

	// Parse overall bitrate
	if bitrate, err := strconv.ParseInt(probe.Format.BitRate, 10, 64); err == nil {
		mediaFile.BitrateKbps = int(bitrate / 1000)
	}

	// Process streams
	var audioStreams []types.AudioStream
	var subtitleStreams []types.SubtitleStream

	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			// Use first video stream
			if mediaFile.Codec == "" {
				mediaFile.Codec = stream.CodecName
				mediaFile.ResolutionWidth = stream.Width
				mediaFile.ResolutionHeight = stream.Height

				// Parse FPS - try avg_frame_rate first, fall back to r_frame_rate
				if stream.AvgFrameRate != "" && stream.AvgFrameRate != "0/0" {
					fps, err := parseFPS(stream.AvgFrameRate)
					if err == nil && fps > 0 {
						mediaFile.FPS = fps
					}
				}
				// Fallback to r_frame_rate if avg_frame_rate didn't work
				if mediaFile.FPS == 0 && stream.RFrameRate != "" && stream.RFrameRate != "0/0" {
					fps, err := parseFPS(stream.RFrameRate)
					if err == nil && fps > 0 {
						mediaFile.FPS = fps
					}
				}

				// Parse video bitrate (if available)
				if stream.BitRate != "" {
					if bitrate, err := strconv.ParseInt(stream.BitRate, 10, 64); err == nil {
						// Only override if format-level bitrate wasn't set
						if mediaFile.BitrateKbps == 0 {
							mediaFile.BitrateKbps = int(bitrate / 1000)
						}
					}
				}
			}

		case "audio":
			audioStream := types.AudioStream{
				Index:         stream.Index,
				Codec:         stream.CodecName,
				Language:      stream.Tags["language"],
				Title:         stream.Tags["title"],
				Channels:      stream.Channels,
				ChannelLayout: stream.ChannelLayout,
			}
			audioStreams = append(audioStreams, audioStream)

		case "subtitle":
			subtitleStream := types.SubtitleStream{
				Index:    stream.Index,
				Codec:    stream.CodecName,
				Language: stream.Tags["language"],
				Title:    stream.Tags["title"],
				Forced:   stream.Tags["forced"] == "1",
			}
			subtitleStreams = append(subtitleStreams, subtitleStream)
		}
	}

	// Serialize audio/subtitle streams to JSON
	if len(audioStreams) > 0 {
		if audioJSON, err := json.Marshal(audioStreams); err == nil {
			mediaFile.AudioStreamsJSON = string(audioJSON)
		}
	}

	if len(subtitleStreams) > 0 {
		if subtitleJSON, err := json.Marshal(subtitleStreams); err == nil {
			mediaFile.SubtitleStreamsJSON = string(subtitleJSON)
		}
	}

	// Analyze transcoding priority
	s.analyzeTranscodingPriority(mediaFile)

	return mediaFile, nil
}

// analyzeTranscodingPriority determines if a file should be transcoded and its priority
func (s *Scanner) analyzeTranscodingPriority(mediaFile *types.MediaFile) {
	// Codec check: transcode if not already HEVC
	if mediaFile.Codec != "hevc" && mediaFile.Codec != "h265" {
		mediaFile.ShouldTranscode = true
	} else {
		mediaFile.ShouldTranscode = false
		return
	}

	// Priority scoring (0-100)
	priority := 0

	// Resolution-based priority
	pixels := mediaFile.ResolutionWidth * mediaFile.ResolutionHeight
	switch {
	case pixels >= 3840*2160: // 4K
		priority += 50
	case pixels >= 1920*1080: // 1080p
		priority += 40
	case pixels >= 1280*720: // 720p
		priority += 20
	default: // SD
		priority += 10
	}

	// Size-based priority - larger files first
	sizeGB := float64(mediaFile.FileSizeBytes) / (1024 * 1024 * 1024)
	switch {
	case sizeGB >= 20:
		priority += 30
	case sizeGB >= 10:
		priority += 20
	case sizeGB >= 5:
		priority += 10
	default:
		priority += 5
	}

	// Estimate space savings
	// HEVC typically achieves 40-60% reduction from H.264
	if mediaFile.Codec == "h264" || mediaFile.Codec == "mpeg4" {
		mediaFile.EstimatedSizeReductionPercent = 50
	} else {
		// Other codecs (mpeg2, etc.) may save even more
		mediaFile.EstimatedSizeReductionPercent = 60
	}

	mediaFile.TranscodingPriority = priority
}

// parseFPS parses fractional frame rates like "24000/1001" or "30/1"
func parseFPS(fpsStr string) (float64, error) {
	parts := strings.Split(fpsStr, "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid fps format: %s", fpsStr)
	}

	numerator, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}

	denominator, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, err
	}

	if denominator == 0 {
		return 0, fmt.Errorf("fps denominator is zero")
	}

	return numerator / denominator, nil
}
