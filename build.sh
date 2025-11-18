#!/bin/bash
set -e

# Build transcoder with version information

BUILD_TIME=$(date -u +%Y%m%d.%H%M%S)
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

LDFLAGS="-X transcoder/internal/version.BuildTime=${BUILD_TIME} -X transcoder/internal/version.GitCommit=${GIT_COMMIT}"

echo "Building transcoder..."
echo "  Build time: ${BUILD_TIME}"
echo "  Git commit: ${GIT_COMMIT}"

go build -ldflags "${LDFLAGS}" -o transcoder cmd/transcoder/main.go

echo "Build complete: ./transcoder"
