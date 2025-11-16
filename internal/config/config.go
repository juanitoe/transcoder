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
	Database DatabaseConfig `yaml:"database"`
	Logging  LoggingConfig  `yaml:"logging"`
}

// RemoteConfig contains remote server settings
type RemoteConfig struct {
	Host       string   `yaml:"host"`
	User       string   `yaml:"user"`
	SSHKey     string   `yaml:"ssh_key"`
	MediaPaths []string `yaml:"media_paths"`
	Port       int      `yaml:"port"` // Default 22
}

// EncoderConfig contains video encoding settings
type EncoderConfig struct {
	Codec   string `yaml:"codec"`   // e.g., hevc_videotoolbox
	Quality int    `yaml:"quality"` // 0-100
	Preset  string `yaml:"preset"`  // ultrafast, superfast, veryfast, faster, fast, medium, slow, slower, veryslow
}

// WorkersConfig contains worker pool settings
type WorkersConfig struct {
	MaxWorkers int    `yaml:"max_workers"` // Maximum concurrent workers
	WorkDir    string `yaml:"work_dir"`    // Temporary work directory
}

// DatabaseConfig contains database settings
type DatabaseConfig struct {
	Path string `yaml:"path"` // Path to SQLite database file
}

// LoggingConfig contains logging settings
type LoggingConfig struct {
	Level      string `yaml:"level"`       // debug, info, warn, error
	File       string `yaml:"file"`        // Log file path
	ScannerLog string `yaml:"scanner_log"` // Scanner debug log file path
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
	if c.Remote.Port == 0 {
		c.Remote.Port = 22
	}

	if c.Encoder.Codec == "" {
		c.Encoder.Codec = "hevc_videotoolbox"
	}

	if c.Encoder.Quality == 0 {
		c.Encoder.Quality = 75
	}

	if c.Encoder.Preset == "" {
		c.Encoder.Preset = "medium"
	}

	if c.Workers.MaxWorkers == 0 {
		c.Workers.MaxWorkers = 2
	}

	if c.Workers.WorkDir == "" {
		home, _ := os.UserHomeDir()
		c.Workers.WorkDir = filepath.Join(home, "transcoder_temp")
	}

	if c.Database.Path == "" {
		home, _ := os.UserHomeDir()
		c.Database.Path = filepath.Join(home, "transcoder", "transcoder.db")
	}

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
}

// validate checks that required fields are set
func (c *Config) validate() error {
	if c.Remote.Host == "" {
		return fmt.Errorf("remote.host is required")
	}

	if c.Remote.User == "" {
		return fmt.Errorf("remote.user is required")
	}

	if len(c.Remote.MediaPaths) == 0 {
		return fmt.Errorf("remote.media_paths must have at least one path")
	}

	if c.Encoder.Quality < 0 || c.Encoder.Quality > 100 {
		return fmt.Errorf("encoder.quality must be between 0 and 100")
	}

	if c.Workers.MaxWorkers < 1 {
		return fmt.Errorf("workers.max_workers must be at least 1")
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
