package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level represents logging level
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time
	Level     string
	Message   string
}

// LogSubscriber is a function that receives log entries
type LogSubscriber func(entry LogEntry)

// Logger handles application-wide logging
type Logger struct {
	file        *os.File
	level       Level
	mu          sync.Mutex
	subscribers []LogSubscriber
	subMu       sync.RWMutex
}

var (
	defaultLogger *Logger
	once          sync.Once
)

// Init initializes the global logger with the given file path and level
func Init(filePath string, levelStr string) error {
	var initErr error
	once.Do(func() {
		// Expand home directory
		if strings.HasPrefix(filePath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				initErr = fmt.Errorf("failed to get home directory: %w", err)
				return
			}
			filePath = filepath.Join(home, filePath[2:])
		}

		// Ensure log directory exists
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			initErr = fmt.Errorf("failed to create log directory: %w", err)
			return
		}

		// Open log file in append mode
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			initErr = fmt.Errorf("failed to open log file: %w", err)
			return
		}

		// Parse level
		level := parseLevel(levelStr)

		defaultLogger = &Logger{
			file:  file,
			level: level,
		}

		// Log startup
		defaultLogger.Info("=== Transcoder started ===")
	})
	return initErr
}

// parseLevel converts string level to Level constant
func parseLevel(levelStr string) Level {
	switch strings.ToLower(levelStr) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Close closes the log file
func Close() {
	if defaultLogger != nil && defaultLogger.file != nil {
		defaultLogger.Info("=== Transcoder stopped ===")
		_ = defaultLogger.file.Close()
	}
}

// Subscribe adds a subscriber to receive log entries
func Subscribe(subscriber LogSubscriber) {
	if defaultLogger != nil {
		defaultLogger.subMu.Lock()
		defaultLogger.subscribers = append(defaultLogger.subscribers, subscriber)
		defaultLogger.subMu.Unlock()
	}
}

// log writes a log message at the given level
func (l *Logger) log(level Level, levelStr string, format string, args ...interface{}) {
	if l == nil || level < l.level {
		return
	}

	now := time.Now()
	message := fmt.Sprintf(format, args...)

	// Write to file
	l.mu.Lock()
	if l.file != nil {
		timestamp := now.Format("2006-01-02 15:04:05")
		logLine := fmt.Sprintf("[%s] %s: %s\n", timestamp, levelStr, message)
		_, _ = l.file.WriteString(logLine)
	}
	l.mu.Unlock()

	// Notify subscribers
	entry := LogEntry{
		Timestamp: now,
		Level:     levelStr,
		Message:   message,
	}
	l.subMu.RLock()
	for _, sub := range l.subscribers {
		sub(entry)
	}
	l.subMu.RUnlock()
}

// Debug logs a debug message
func Debug(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.log(LevelDebug, "DEBUG", format, args...)
	}
}

// Info logs an info message
func Info(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.log(LevelInfo, "INFO", format, args...)
	}
}

// Warn logs a warning message
func Warn(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.log(LevelWarn, "WARN", format, args...)
	}
}

// Error logs an error message
func Error(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.log(LevelError, "ERROR", format, args...)
	}
}

// Logger method versions for struct use
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(LevelDebug, "DEBUG", format, args...)
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.log(LevelInfo, "INFO", format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(LevelWarn, "WARN", format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.log(LevelError, "ERROR", format, args...)
}
