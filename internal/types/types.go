package types

import (
	"time"
)

// JobStatus represents the current state of a transcode job
type JobStatus string

const (
	StatusQueued      JobStatus = "queued"
	StatusDownloading JobStatus = "downloading"
	StatusTranscoding JobStatus = "transcoding"
	StatusUploading   JobStatus = "uploading"
	StatusCompleted   JobStatus = "completed"
	StatusFailed      JobStatus = "failed"
	StatusPaused      JobStatus = "paused"
	StatusCanceled    JobStatus = "canceled"
	StatusSkipped     JobStatus = "skipped" // Transcoded file was larger than original
)

// ProcessingStage represents what stage of processing a job is in
type ProcessingStage string

const (
	StageDownload  ProcessingStage = "download"
	StageTranscode ProcessingStage = "transcode"
	StageValidate  ProcessingStage = "validate"
	StageUpload    ProcessingStage = "upload"
)

// MediaFile represents a discovered media file from the remote library
type MediaFile struct {
	ID                            int64      `json:"id"`
	FilePath                      string     `json:"file_path"`
	FileName                      string     `json:"file_name"`
	FileSizeBytes                 int64      `json:"file_size_bytes"`
	Codec                         string     `json:"codec"`
	ResolutionWidth               int        `json:"resolution_width"`
	ResolutionHeight              int        `json:"resolution_height"`
	DurationSeconds               float64    `json:"duration_seconds"`
	BitrateKbps                   int        `json:"bitrate_kbps"`
	FPS                           float64    `json:"fps"`
	AudioStreamsJSON              string     `json:"audio_streams_json"`    // JSON array
	SubtitleStreamsJSON           string     `json:"subtitle_streams_json"` // JSON array
	ShouldTranscode               bool       `json:"should_transcode"`
	TranscodingPriority           int        `json:"transcoding_priority"`
	EstimatedSizeReductionPercent int        `json:"estimated_size_reduction_percent"`
	SourceChecksum                string     `json:"source_checksum"`
	SourceChecksumAlgo            string     `json:"source_checksum_algo"`
	SourceChecksumAt              *time.Time `json:"source_checksum_at"`
	DiscoveredAt                  time.Time  `json:"discovered_at"`
	UpdatedAt                     time.Time  `json:"updated_at"`
}

// TranscodeJob represents an active or completed transcode job
type TranscodeJob struct {
	ID                      int64           `json:"id"`
	MediaFileID             int64           `json:"media_file_id"`
	FilePath                string          `json:"file_path"`
	FileName                string          `json:"file_name"`
	FileSizeBytes           int64           `json:"file_size_bytes"`
	Status                  JobStatus       `json:"status"`
	Stage                   ProcessingStage `json:"stage"`
	Progress                float64         `json:"progress"` // 0-100
	WorkerID                string          `json:"worker_id"`
	TranscodeStartedAt      *time.Time      `json:"transcode_started_at"`
	TranscodeCompletedAt    *time.Time      `json:"transcode_completed_at"`
	TranscodedFileSizeBytes int64           `json:"transcoded_file_size_bytes"`
	EncodingTimeSeconds     int             `json:"encoding_time_seconds"`
	EncodingFPS             float64         `json:"encoding_fps"`
	VerificationPassed      bool            `json:"verification_passed"`
	LocalInputChecksum      string          `json:"local_input_checksum"`
	LocalOutputChecksum     string          `json:"local_output_checksum"`
	UploadedChecksum        string          `json:"uploaded_checksum"`
	ChecksumVerified        bool            `json:"checksum_verified"`
	ErrorMessage            string          `json:"error_message"`
	RetryCount              int             `json:"retry_count"`
	LastRetryAt             *time.Time      `json:"last_retry_at"`
	Priority                int             `json:"priority"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

// ProgressUpdate represents real-time progress from a worker
type ProgressUpdate struct {
	JobID            int64           `json:"job_id"`
	WorkerID         string          `json:"worker_id"`
	Stage            ProcessingStage `json:"stage"`
	Progress         float64         `json:"progress"` // 0-100
	CurrentFrame     int64           `json:"current_frame"`
	TotalFrames      int64           `json:"total_frames"`
	FPS              float64         `json:"fps"`
	Speed            float64         `json:"speed"` // e.g., 3.2x
	BytesTransferred int64           `json:"bytes_transferred"`
	TotalBytes       int64           `json:"total_bytes"`
	ElapsedSeconds   int             `json:"elapsed_seconds"`
	ETASeconds       int             `json:"eta_seconds"`
	Message          string          `json:"message"`
	Timestamp        time.Time       `json:"timestamp"`
}

// Statistics represents aggregate statistics
type Statistics struct {
	TotalFiles             int        `json:"total_files"`
	ToTranscode            int        `json:"to_transcode"`
	Queued                 int        `json:"queued"`
	InProgress             int        `json:"in_progress"`
	Completed              int        `json:"completed"`
	Failed                 int        `json:"failed"`
	TotalSize              int64      `json:"total_size"`
	TranscodedSize         int64      `json:"transcoded_size"`
	SpaceSaved             int64      `json:"space_saved"`
	SpaceSavedPercent      float64    `json:"space_saved_percent"`
	AvgSizeReduction       float64    `json:"avg_size_reduction"`
	AvgEncodingFPS         float64    `json:"avg_encoding_fps"`
	AvgEncodingTimeMinutes float64    `json:"avg_encoding_time_minutes"`
	LastScanTime           *time.Time `json:"last_scan_time"`
}

// EventType represents different types of system events
type EventType string

const (
	EventScanStarted   EventType = "scan_started"
	EventScanProgress  EventType = "scan_progress"
	EventScanCompleted EventType = "scan_completed"
	EventScanFailed    EventType = "scan_failed"
	EventJobStarted    EventType = "job_started"
	EventJobProgress   EventType = "job_progress"
	EventJobCompleted  EventType = "job_completed"
	EventJobFailed     EventType = "job_failed"
	EventJobPaused     EventType = "job_paused"
	EventJobResumed    EventType = "job_resumed"
	EventJobCanceled   EventType = "job_canceled"
	EventWorkerAdded   EventType = "worker_added"
	EventWorkerRemoved EventType = "worker_removed"
	EventError         EventType = "error"
)

// Event represents a system event
type Event struct {
	Type      EventType   `json:"type"`
	Message   string      `json:"message"`
	Data      interface{} `json:"data"`
	Timestamp time.Time   `json:"timestamp"`
}

// AudioStream represents an audio stream
type AudioStream struct {
	Index         int    `json:"index"`
	Codec         string `json:"codec"`
	Language      string `json:"language"`
	Channels      int    `json:"channels"`
	ChannelLayout string `json:"channel_layout"`
	Title         string `json:"title"`
}

// SubtitleStream represents a subtitle stream
type SubtitleStream struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
	Forced   bool   `json:"forced"`
}

// VideoMetadata represents complete video metadata
type VideoMetadata struct {
	Codec            string           `json:"codec"`
	ResolutionWidth  int              `json:"resolution_width"`
	ResolutionHeight int              `json:"resolution_height"`
	DurationSeconds  float64          `json:"duration_seconds"`
	BitrateKbps      int              `json:"bitrate_kbps"`
	FPS              float64          `json:"fps"`
	AudioStreams     []AudioStream    `json:"audio_streams"`
	SubtitleStreams  []SubtitleStream `json:"subtitle_streams"`
	FileSizeBytes    int64            `json:"file_size_bytes"`
}
