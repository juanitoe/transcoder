package checksum

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// Algorithm represents the checksum algorithm used
type Algorithm string

const (
	AlgoXXH64 Algorithm = "xxh64"
	AlgoMD5   Algorithm = "md5"
)

// Result contains the checksum and algorithm used
type Result struct {
	Hash      string
	Algorithm Algorithm
}

// NewXXHash64 creates a new xxHash64 hasher
func NewXXHash64() hash.Hash64 {
	return xxhash.New()
}

// NewMD5 creates a new MD5 hasher
func NewMD5() hash.Hash {
	return md5.New()
}

// CalculateFile calculates the xxHash64 checksum of a local file
func CalculateFile(filePath string) (*Result, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	hasher := xxhash.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return nil, fmt.Errorf("failed to calculate checksum: %w", err)
	}

	return &Result{
		Hash:      hex.EncodeToString(hasher.Sum(nil)),
		Algorithm: AlgoXXH64,
	}, nil
}

// StreamingHasher wraps a reader to calculate checksum while streaming
type StreamingHasher struct {
	reader    io.Reader
	xxhasher  hash.Hash64
	md5hasher hash.Hash
	algorithm Algorithm
}

// NewStreamingHasher creates a hasher that calculates checksum during read
func NewStreamingHasher(reader io.Reader, algo Algorithm) *StreamingHasher {
	sh := &StreamingHasher{
		algorithm: algo,
	}

	if algo == AlgoMD5 {
		sh.md5hasher = md5.New()
		sh.reader = io.TeeReader(reader, sh.md5hasher)
	} else {
		sh.xxhasher = xxhash.New()
		sh.reader = io.TeeReader(reader, sh.xxhasher)
	}

	return sh
}

// Read implements io.Reader
func (sh *StreamingHasher) Read(p []byte) (n int, err error) {
	return sh.reader.Read(p)
}

// Sum returns the final checksum result
func (sh *StreamingHasher) Sum() *Result {
	var hashBytes []byte
	if sh.algorithm == AlgoMD5 {
		hashBytes = sh.md5hasher.Sum(nil)
	} else {
		hashBytes = sh.xxhasher.Sum(nil)
	}

	return &Result{
		Hash:      hex.EncodeToString(hashBytes),
		Algorithm: sh.algorithm,
	}
}

// ParseRemoteOutput parses the output from xxhsum or md5sum commands
func ParseRemoteOutput(output string, algo Algorithm) (string, error) {
	// Both xxhsum and md5sum output: "hash  filename" or "hash filename"
	output = strings.TrimSpace(output)
	parts := strings.Fields(output)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid checksum output: %s", output)
	}
	return parts[0], nil
}

// RemoteCommand returns the shell command to calculate checksum on remote
func RemoteCommand(filePath string, algo Algorithm) string {
	if algo == AlgoMD5 {
		return fmt.Sprintf("md5sum %q | cut -d' ' -f1", filePath)
	}
	// xxhsum outputs in format: "hash  filename"
	return fmt.Sprintf("xxhsum %q | cut -d' ' -f1", filePath)
}

// DetectRemoteCommand returns the command to check which tool is available
func DetectRemoteCommand() string {
	return "which xxhsum >/dev/null 2>&1 && echo xxh64 || echo md5"
}

// ParseDetectionOutput parses the output from DetectRemoteCommand
func ParseDetectionOutput(output string) Algorithm {
	output = strings.TrimSpace(output)
	if output == "xxh64" {
		return AlgoXXH64
	}
	return AlgoMD5
}
