package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	Remote   RemoteConfig   `yaml:"remote"`
	Encoder  EncoderConfig  `yaml:"encoder"`
	Workers  WorkersConfig  `yaml:"workers"`
	Files    FileConfig     `yaml:"files"`
	Database DatabaseConfig `yaml:"database"`
	Logging  LoggingConfig  `yaml:"logging"`
}

// RemoteConfig contains remote server settings
type RemoteConfig struct {
	Host           string   `yaml:"host"`
	User           string   `yaml:"user"`
	SSHKey         string   `yaml:"ssh_key"`
	MediaPaths     []string `yaml:"media_paths"`
	Port           int      `yaml:"port"`              // Default 22
	SSHPoolSize    int      `yaml:"ssh_pool_size"`     // SSH connection pool size per worker (default 4)
	SSHTimeout     int      `yaml:"ssh_timeout"`       // SSH connection timeout in seconds (default 30)
	SSHKeepalive   int      `yaml:"ssh_keepalive"`     // SSH keepalive interval in seconds (default 0 = disabled)
	SFTPBufferSize int      `yaml:"sftp_buffer_size"`  // SFTP buffer size in bytes (default 32768, 0 = use default)
}

// EncoderConfig contains video encoding settings
type EncoderConfig struct {
	Codec   string `yaml:"codec"`   // e.g., hevc_videotoolbox
	Quality int    `yaml:"quality"` // 0-100
	Preset  string `yaml:"preset"`  // ultrafast, superfast, veryfast, faster, fast, medium, slow, slower, veryslow
}

// WorkersConfig contains worker pool settings
type WorkersConfig struct {
	MaxWorkers               int  `yaml:"max_workers"`                 // Maximum concurrent workers
	WorkDir                  string `yaml:"work_dir"`                  // Temporary work directory
	SkipChecksumVerification bool `yaml:"skip_checksum_verification"` // Skip checksum verification (faster but less safe, default false)
}

// FileConfig contains file handling settings
type FileConfig struct {
	Extensions      []string `yaml:"extensions"`        // File extensions to process (default: mkv, mp4, avi, m4v, ts)
	MinSizeBytes    int64    `yaml:"min_size_bytes"`    // Minimum file size to process in bytes (default 0 = no minimum)
	ExcludePatterns []string `yaml:"exclude_patterns"`  // Glob patterns to exclude from scanning
	FollowSymlinks  bool     `yaml:"follow_symlinks"`   // Follow symbolic links during scanning (default false)
	KeepOriginal    bool     `yaml:"keep_original"`     // Keep original file after transcoding (default false)
}

// DatabaseConfig contains database settings
type DatabaseConfig struct {
	Path string `yaml:"path"` // Path to SQLite database file
}

// LoggingConfig contains logging settings
type LoggingConfig struct {
	Level           string `yaml:"level"`             // debug, info, warn, error (default info)
	File            string `yaml:"file"`              // Log file path
	ScannerLog      string `yaml:"scanner_log"`       // Scanner debug log file path
	MaxLogSizeBytes int64  `yaml:"max_log_size_bytes"` // Maximum log file size before rotation in bytes (default 104857600 = 100MB)
}

// Load reads configuration from a YAML file
func Load(path string) (*Config, error) {
	// Expand home directory
	if path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}

	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse YAML
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Apply defaults
	cfg.applyDefaults()

	// Validate
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// applyDefaults sets default values for optional fields
func (c *Config) applyDefaults() {
	// Remote defaults
	if c.Remote.Port == 0 {
		c.Remote.Port = 22
	}

	if c.Remote.SSHPoolSize == 0 {
		c.Remote.SSHPoolSize = 4
	}

	if c.Remote.SSHTimeout == 0 {
		c.Remote.SSHTimeout = 30
	}

	// SSHKeepalive defaults to 0 (disabled) - no change needed

	if c.Remote.SFTPBufferSize == 0 {
		c.Remote.SFTPBufferSize = 32768 // 32KB default
	}

	// Encoder defaults
	if c.Encoder.Codec == "" {
		c.Encoder.Codec = "hevc_videotoolbox"
	}

	if c.Encoder.Quality == 0 {
		c.Encoder.Quality = 75
	}

	if c.Encoder.Preset == "" {
		c.Encoder.Preset = "medium"
	}

	// Workers defaults
	if c.Workers.MaxWorkers == 0 {
		c.Workers.MaxWorkers = 2
	}

	if c.Workers.WorkDir == "" {
		home, _ := os.UserHomeDir()
		c.Workers.WorkDir = filepath.Join(home, "transcoder_temp")
	}

	// SkipChecksumVerification defaults to false - no change needed

	// File handling defaults
	if len(c.Files.Extensions) == 0 {
		c.Files.Extensions = []string{"mkv", "mp4", "avi", "m4v", "ts", "m2ts"}
	}

	// MinSizeBytes defaults to 0 (no minimum) - no change needed
	// ExcludePatterns defaults to empty - no change needed
	// FollowSymlinks defaults to false - no change needed
	// KeepOriginal defaults to false - no change needed

	// Database defaults
	if c.Database.Path == "" {
		home, _ := os.UserHomeDir()
		c.Database.Path = filepath.Join(home, "transcoder", "transcoder.db")
	}

	// Logging defaults
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}

	if c.Logging.File == "" {
		home, _ := os.UserHomeDir()
		c.Logging.File = filepath.Join(home, "transcoder", "transcoder.log")
	}

	if c.Logging.ScannerLog == "" {
		home, _ := os.UserHomeDir()
		c.Logging.ScannerLog = filepath.Join(home, "transcoder", "scanner.log")
	}

	if c.Logging.MaxLogSizeBytes == 0 {
		c.Logging.MaxLogSizeBytes = 104857600 // 100MB default
	}
}

// validate checks that required fields are set
func (c *Config) validate() error {
	// Remote validation
	if c.Remote.Host == "" {
		return fmt.Errorf("remote.host is required")
	}

	if c.Remote.User == "" {
		return fmt.Errorf("remote.user is required")
	}

	if len(c.Remote.MediaPaths) == 0 {
		return fmt.Errorf("remote.media_paths must have at least one path")
	}

	if c.Remote.SSHPoolSize < 1 || c.Remote.SSHPoolSize > 16 {
		return fmt.Errorf("remote.ssh_pool_size must be between 1 and 16")
	}

	if c.Remote.SSHTimeout < 1 || c.Remote.SSHTimeout > 300 {
		return fmt.Errorf("remote.ssh_timeout must be between 1 and 300 seconds")
	}

	if c.Remote.SSHKeepalive < 0 || c.Remote.SSHKeepalive > 3600 {
		return fmt.Errorf("remote.ssh_keepalive must be between 0 and 3600 seconds")
	}

	if c.Remote.SFTPBufferSize < 0 || c.Remote.SFTPBufferSize > 1048576 {
		return fmt.Errorf("remote.sftp_buffer_size must be between 0 and 1048576 bytes (1MB)")
	}

	// Encoder validation
	if c.Encoder.Quality < 0 || c.Encoder.Quality > 100 {
		return fmt.Errorf("encoder.quality must be between 0 and 100")
	}

	// Workers validation
	if c.Workers.MaxWorkers < 0 {
		return fmt.Errorf("workers.max_workers cannot be negative")
	}

	// File validation
	if c.Files.MinSizeBytes < 0 {
		return fmt.Errorf("files.min_size_bytes cannot be negative")
	}

	// Logging validation
	validLevels := []string{"debug", "info", "warn", "error"}
	levelValid := false
	for _, level := range validLevels {
		if c.Logging.Level == level {
			levelValid = true
			break
		}
	}
	if !levelValid {
		return fmt.Errorf("logging.level must be one of: debug, info, warn, error")
	}

	if c.Logging.MaxLogSizeBytes < 0 {
		return fmt.Errorf("logging.max_log_size_bytes cannot be negative")
	}

	return nil
}

// Default returns a configuration with default values
func Default() *Config {
	cfg := &Config{}
	cfg.applyDefaults()
	return cfg
}

// Save writes the configuration to a YAML file
func (c *Config) Save(path string) error {
	// Expand home directory
	if path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal to YAML
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write file
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
