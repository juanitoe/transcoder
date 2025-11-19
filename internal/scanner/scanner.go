package scanner

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"transcoder/internal/checksum"
	"transcoder/internal/config"
	"transcoder/internal/database"
	"transcoder/internal/types"
	"github.com/cespare/xxhash/v2"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Scanner handles remote media library scanning
type Scanner struct {
	cfg           *config.Config
	db            *database.DB
	sshClient     *ssh.Client
	sftpClient    *sftp.Client
	progress      ScanProgress
	logFile       *os.File
	checksumAlgo  checksum.Algorithm // Detected remote checksum algorithm
}

// New creates a new Scanner instance
func New(cfg *config.Config, db *database.DB) (*Scanner, error) {
	// Open log file
	logPath := os.ExpandEnv(cfg.Logging.ScannerLog)
	if strings.HasPrefix(logPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		logPath = filepath.Join(home, logPath[2:])
	}

	// Ensure log directory exists
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open log file in append mode
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	return &Scanner{
		cfg:     cfg,
		db:      db,
		logFile: logFile,
	}, nil
}

// Connect establishes SSH/SFTP connection to remote server
func (s *Scanner) Connect(ctx context.Context) error {
	// Try to use SSH agent first (for passphrase-protected keys)
	var authMethods []ssh.AuthMethod

	// Try SSH agent
	if agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers))
		s.logDebug("Using SSH agent for authentication")
	}

	// Try loading key file if agent failed or as fallback
	if len(authMethods) == 0 {
		keyPath := os.ExpandEnv(s.cfg.Remote.SSHKey)
		if strings.HasPrefix(keyPath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			keyPath = filepath.Join(home, keyPath[2:])
		}

		s.logDebug("Loading SSH key from: %s", keyPath)

		// Read private key
		keyBytes, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("failed to read SSH key: %w", err)
		}

		// Parse private key (will fail if passphrase-protected)
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			return fmt.Errorf("failed to parse SSH key: %w (try using ssh-add to add your key to the agent)", err)
		}

		authMethods = append(authMethods, ssh.PublicKeys(signer))
		s.logDebug("Using SSH key file for authentication")
	}

	// Configure SSH client
	sshConfig := &ssh.ClientConfig{
		User: s.cfg.Remote.User,
		Auth: authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Use known_hosts
		Timeout:         30 * time.Second,
	}

	// Connect to SSH server
	addr := fmt.Sprintf("%s:%d", s.cfg.Remote.Host, s.cfg.Remote.Port)
	s.logDebug("Connecting to SSH server: %s@%s", s.cfg.Remote.User, addr)
	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to SSH server: %w", err)
	}
	s.sshClient = sshClient
	s.logDebug("SSH connection established")

	// Create SFTP client
	s.logDebug("Creating SFTP client")
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		s.sshClient.Close()
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	s.sftpClient = sftpClient
	s.logDebug("SFTP client created successfully")

	// Detect available checksum algorithm on remote
	s.checksumAlgo = s.detectRemoteChecksumAlgo()
	s.logDebug("Remote checksum algorithm: %s", s.checksumAlgo)

	return nil
}

// detectRemoteChecksumAlgo checks which checksum tool is available on remote
func (s *Scanner) detectRemoteChecksumAlgo() checksum.Algorithm {
	session, err := s.sshClient.NewSession()
	if err != nil {
		s.logDebug("Failed to create session for checksum detection: %v", err)
		return checksum.AlgoMD5 // Default fallback
	}
	defer session.Close()

	output, err := session.Output(checksum.DetectRemoteCommand())
	if err != nil {
		s.logDebug("Failed to detect checksum tool: %v", err)
		return checksum.AlgoMD5 // Default fallback
	}

	return checksum.ParseDetectionOutput(string(output))
}

// GetChecksumAlgo returns the detected remote checksum algorithm
func (s *Scanner) GetChecksumAlgo() checksum.Algorithm {
	return s.checksumAlgo
}

// Close closes the SSH/SFTP connection
func (s *Scanner) Close() error {
	if s.sftpClient != nil {
		s.sftpClient.Close()
	}
	if s.sshClient != nil {
		s.sshClient.Close()
	}
	if s.logFile != nil {
		s.logFile.Close()
	}
	return nil
}

// ScanProgress represents scan progress information
type ScanProgress struct {
	CurrentPath   string
	FilesScanned  int
	FilesAdded    int
	FilesUpdated  int
	BytesScanned  int64
	ErrorCount    int
	LastError     error
}

// ProgressCallback is called periodically during scanning
type ProgressCallback func(progress ScanProgress)

// Scan scans all configured media paths and updates the database
func (s *Scanner) Scan(ctx context.Context, progressCb ProgressCallback) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	var totalProgress ScanProgress

	s.logDebug("Starting scan of %d media paths", len(s.cfg.Remote.MediaPaths))
	for _, mediaPath := range s.cfg.Remote.MediaPaths {
		s.logDebug("Scanning path: %s", mediaPath)
		err := s.scanPath(ctx, mediaPath, &totalProgress, progressCb)
		if err != nil {
			return fmt.Errorf("failed to scan %s: %w", mediaPath, err)
		}
	}

	s.logDebug("Scan complete - %d files scanned, %d added, %d updated",
		totalProgress.FilesScanned, totalProgress.FilesAdded, totalProgress.FilesUpdated)
	return nil
}

// scanPath recursively scans a single media path
func (s *Scanner) scanPath(ctx context.Context, path string, progress *ScanProgress, progressCb ProgressCallback) error {
	// Check for cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Update progress
	progress.CurrentPath = path
	s.progress = *progress // Store in scanner for polling
	if progressCb != nil {
		progressCb(*progress)
	}

	// List directory contents
	entries, err := s.sftpClient.ReadDir(path)
	if err != nil {
		progress.ErrorCount++
		progress.LastError = fmt.Errorf("failed to read directory %s: %w", path, err)
		if progressCb != nil {
			progressCb(*progress)
		}
		return nil // Continue scanning other directories
	}

	for _, entry := range entries {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fullPath := filepath.Join(path, entry.Name())

		if entry.IsDir() {
			// Recursively scan subdirectory
			if err := s.scanPath(ctx, fullPath, progress, progressCb); err != nil {
				return err
			}
		} else if s.isVideoFile(entry.Name()) {
			// Process video file
			progress.FilesScanned++
			progress.BytesScanned += entry.Size()
			s.logDebug("Found video file: %s (%d bytes)", fullPath, entry.Size())

			// Extract metadata
			s.logDebug("Extracting metadata from: %s", fullPath)
			metadata, err := s.extractMetadata(ctx, fullPath)
			if err != nil {
				progress.ErrorCount++
				progress.LastError = fmt.Errorf("failed to extract metadata from %s: %w", fullPath, err)
				s.logDebug("Metadata extraction failed: %v", err)
				if progressCb != nil {
					progressCb(*progress)
				}
				continue
			}
			s.logDebug("Metadata extracted successfully: codec=%s, resolution=%dx%d",
				metadata.Codec, metadata.ResolutionWidth, metadata.ResolutionHeight)

			// Check if file exists in database
			existing, err := s.db.GetMediaFileByPath(fullPath)
			if err != nil {
				progress.ErrorCount++
				progress.LastError = fmt.Errorf("database error for %s: %w", fullPath, err)
				if progressCb != nil {
					progressCb(*progress)
				}
				continue
			}

			if existing == nil {
				// New file - calculate checksum and add to database
				s.logDebug("Calculating checksum for new file: %s", fullPath)
				remoteChecksum, err := s.CalculateRemoteChecksum(fullPath)
				if err != nil {
					s.logDebug("Warning: failed to calculate checksum for %s: %v", fullPath, err)
					// Continue without checksum - not fatal
				} else {
					metadata.SourceChecksum = remoteChecksum
					metadata.SourceChecksumAlgo = string(s.checksumAlgo)
					now := time.Now()
					metadata.SourceChecksumAt = &now
				}

				_, err = s.db.AddMediaFile(metadata)
				if err != nil {
					progress.ErrorCount++
					progress.LastError = fmt.Errorf("failed to add %s to database: %w", fullPath, err)
					if progressCb != nil {
						progressCb(*progress)
					}
					continue
				}
				progress.FilesAdded++
			} else {
				// Existing file - check if changed by size (fast check)
				if existing.FileSizeBytes != metadata.FileSizeBytes {
					// Size changed - calculate new checksum and update
					s.logDebug("File size changed for %s: %d -> %d", fullPath, existing.FileSizeBytes, metadata.FileSizeBytes)

					remoteChecksum, err := s.CalculateRemoteChecksum(fullPath)
					if err != nil {
						s.logDebug("Warning: failed to calculate checksum for %s: %v", fullPath, err)
					} else {
						metadata.SourceChecksum = remoteChecksum
						metadata.SourceChecksumAlgo = string(s.checksumAlgo)
						now := time.Now()
						metadata.SourceChecksumAt = &now
					}

					metadata.ID = existing.ID
					if err := s.db.UpdateMediaFile(existing.ID, metadata); err != nil {
						progress.ErrorCount++
						progress.LastError = fmt.Errorf("failed to update %s in database: %w", fullPath, err)
						if progressCb != nil {
							progressCb(*progress)
						}
						continue
					}
					progress.FilesUpdated++
				} else if existing.SourceChecksum == "" {
					// File unchanged but missing checksum - backfill it
					s.logDebug("Backfilling checksum for %s", fullPath)
					remoteChecksum, err := s.CalculateRemoteChecksum(fullPath)
					if err != nil {
						s.logDebug("Warning: failed to calculate checksum for %s: %v", fullPath, err)
					} else {
						s.db.UpdateMediaFileChecksum(existing.ID, remoteChecksum, string(s.checksumAlgo))
					}
				}
				// else: size matches and we have checksum - file unchanged, skip
			}

			// Update progress
			if progressCb != nil {
				progressCb(*progress)
			}
		}
	}

	return nil
}

// isVideoFile checks if a file has a video extension
func (s *Scanner) isVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	videoExts := []string{
		".mp4", ".mkv", ".avi", ".mov", ".m4v",
		".wmv", ".flv", ".webm", ".mpg", ".mpeg",
		".m2ts", ".ts", ".vob", ".ogv", ".3gp",
	}
	for _, videoExt := range videoExts {
		if ext == videoExt {
			return true
		}
	}
	return false
}

// extractMetadata extracts video metadata from a remote file using ffprobe
func (s *Scanner) extractMetadata(ctx context.Context, remotePath string) (*types.MediaFile, error) {
	// Use FFprobe to extract full metadata
	return s.ExtractMetadataWithFFprobe(ctx, remotePath)
}

// DownloadFile downloads a remote file to a local path with progress tracking
func (s *Scanner) DownloadFile(ctx context.Context, remotePath, localPath string, progressCb func(bytesRead int64, totalBytes int64)) error {
	_, err := s.DownloadFileWithChecksum(ctx, remotePath, localPath, progressCb)
	return err
}

// DownloadFileWithChecksum downloads a remote file and returns its checksum
func (s *Scanner) DownloadFileWithChecksum(ctx context.Context, remotePath, localPath string, progressCb func(bytesRead int64, totalBytes int64)) (string, error) {
	if s.sftpClient == nil {
		return "", fmt.Errorf("not connected - call Connect() first")
	}

	// Open remote file
	remoteFile, err := s.sftpClient.Open(remotePath)
	if err != nil {
		return "", fmt.Errorf("failed to open remote file: %w", err)
	}
	defer remoteFile.Close()

	// Get file size
	stat, err := remoteFile.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat remote file: %w", err)
	}
	totalBytes := stat.Size()

	// Create local file
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create local directory: %w", err)
	}

	localFile, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to create local file: %w", err)
	}
	defer localFile.Close()

	// Create hasher for checksum calculation
	hasher := xxhash.New()

	// Copy with progress tracking and checksum calculation
	buf := make([]byte, 32*1024) // 32KB buffer
	var bytesRead int64

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		n, err := remoteFile.Read(buf)
		if n > 0 {
			// Write to local file
			if _, writeErr := localFile.Write(buf[:n]); writeErr != nil {
				return "", fmt.Errorf("failed to write to local file: %w", writeErr)
			}
			// Write to hasher for checksum calculation
			hasher.Write(buf[:n])
			bytesRead += int64(n)

			// Report progress
			if progressCb != nil {
				progressCb(bytesRead, totalBytes)
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to read from remote file: %w", err)
		}
	}

	// Return the checksum
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// UploadFile uploads a local file to a remote path with progress tracking
func (s *Scanner) UploadFile(ctx context.Context, localPath, remotePath string, progressCb func(bytesWritten int64, totalBytes int64)) error {
	_, err := s.UploadFileWithChecksum(ctx, localPath, remotePath, progressCb)
	return err
}

// UploadFileWithChecksum uploads a local file and returns its checksum
func (s *Scanner) UploadFileWithChecksum(ctx context.Context, localPath, remotePath string, progressCb func(bytesWritten int64, totalBytes int64)) (string, error) {
	if s.sftpClient == nil {
		return "", fmt.Errorf("not connected - call Connect() first")
	}

	// Open local file
	localFile, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to open local file: %w", err)
	}
	defer localFile.Close()

	// Get file size
	stat, err := localFile.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat local file: %w", err)
	}
	totalBytes := stat.Size()

	// Create remote directory if needed
	remoteDir := filepath.Dir(remotePath)
	if err := s.sftpClient.MkdirAll(remoteDir); err != nil {
		return "", fmt.Errorf("failed to create remote directory: %w", err)
	}

	// Create remote file
	remoteFile, err := s.sftpClient.Create(remotePath)
	if err != nil {
		return "", fmt.Errorf("failed to create remote file: %w", err)
	}
	defer remoteFile.Close()

	// Create hasher for checksum calculation
	hasher := xxhash.New()

	// Copy with progress tracking and checksum calculation
	buf := make([]byte, 32*1024) // 32KB buffer
	var bytesWritten int64

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		n, err := localFile.Read(buf)
		if n > 0 {
			// Write to remote file
			if _, writeErr := remoteFile.Write(buf[:n]); writeErr != nil {
				return "", fmt.Errorf("failed to write to remote file: %w", writeErr)
			}
			// Write to hasher for checksum calculation
			hasher.Write(buf[:n])
			bytesWritten += int64(n)

			// Report progress
			if progressCb != nil {
				progressCb(bytesWritten, totalBytes)
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to read from local file: %w", err)
		}
	}

	// Return the checksum
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// DeleteRemoteFile deletes a file on the remote server
func (s *Scanner) DeleteRemoteFile(remotePath string) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	if err := s.sftpClient.Remove(remotePath); err != nil {
		return fmt.Errorf("failed to delete remote file: %w", err)
	}

	return nil
}

// RenameRemoteFile renames/moves a file on the remote server (atomic operation)
func (s *Scanner) RenameRemoteFile(oldPath, newPath string) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	s.logDebug("Renaming remote file: %s -> %s", oldPath, newPath)

	if err := s.sftpClient.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("failed to rename remote file: %w", err)
	}

	s.logDebug("Successfully renamed file")
	return nil
}

// CalculateRemoteChecksum calculates the checksum of a remote file via SSH
func (s *Scanner) CalculateRemoteChecksum(remotePath string) (string, error) {
	if s.sshClient == nil {
		return "", fmt.Errorf("not connected - call Connect() first")
	}

	session, err := s.sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Use detected algorithm
	cmd := checksum.RemoteCommand(remotePath, s.checksumAlgo)
	s.logDebug("Calculating remote checksum: %s", cmd)

	output, err := session.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to calculate remote checksum: %w", err)
	}

	hash := strings.TrimSpace(string(output))
	s.logDebug("Remote checksum: %s", hash)
	return hash, nil
}

// GetProgress returns the current scan progress
func (s *Scanner) GetProgress() ScanProgress {
	return s.progress
}

// logDebug writes a debug message to the log file
func (s *Scanner) logDebug(format string, args ...interface{}) {
	if s.logFile == nil {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	logLine := fmt.Sprintf("[%s] DEBUG: %s\n", timestamp, message)

	s.logFile.WriteString(logLine)
}
