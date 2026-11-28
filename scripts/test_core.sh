#!/usr/bin/env bash
# Builds the core for this platform and runs everything: the Go packages' tests, then the
# Flutter suite with BPBCORE_BIN pointing at the built host (the integration test spawns it
# and drives a real Xray through the service protocol).
set -euo pipefail
cd "$(dirname "$0")/../core"
go vet ./cmd/bpbcore
go test ./... -count=1 -timeout 420s
go build -o bpbcore ./cmd/bpbcore
cd ..
export BPBCORE_BIN="$(pwd)/core/bpbcore"
flutter test
echo "All core and app tests passed."
