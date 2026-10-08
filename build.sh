#!/usr/bin/env sh
# Builds dist/BIMonitor.exe (cross-compiles from Linux/macOS as well).
#   ./build.sh            version from git tag, or 0.0.0-dev
#   ./build.sh 1.2.0
set -eu
cd "$(dirname "$0")"
VERSION="${1:-$(git describe --tags --abbrev=0 2>/dev/null || echo 0.0.0-dev)}"
VERSION="${VERSION#v}"
echo "BI Output Monitor $VERSION"
go run ./tools/genres -version "$VERSION"
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-H windowsgui -s -w -X bimonitor/internal/app.Version=$VERSION" \
  -o dist/BIMonitor.exe ./cmd/bimonitor
ls -l dist/BIMonitor.exe
