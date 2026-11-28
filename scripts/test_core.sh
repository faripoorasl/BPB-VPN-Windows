#!/usr/bin/env bash
# Builds the core for this platform and runs everything: the Go packages' tests, then the
# Flutter suite with BPBCORE_BIN pointing at the built host (the integration test spawns it
# and drives a real Xray through the service protocol). The tests that need the real internet
# are gated: BPB_E2E=1 adds the ECH-gateway registrations (core/networking), and
# BPB_TOR_E2E=1 runs the setup-Tor bootstrap against the Tor network (it needs a tor bundle
# staged at core/tor/: tor and lyrebird from a tor-expert-bundle-linux-x86_64).
set -euo pipefail
cd "$(dirname "$0")/../core"
go vet ./cmd/bpbcore
go test ./... -count=1 -timeout 420s
go build -o bpbcore ./cmd/bpbcore
cd ..
export BPBCORE_BIN="$(pwd)/core/bpbcore"
flutter test
echo "All core and app tests passed."
