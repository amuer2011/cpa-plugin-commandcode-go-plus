#!/usr/bin/env bash
# Build natively on Linux with a glibc C compiler (not Alpine/musl).
set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_NAME="commandcode-go"
PLUGIN_VERSION="${PLUGIN_VERSION:-$(cat "${SRC_DIR}/VERSION")}"
PLUGIN_VERSION="${PLUGIN_VERSION#v}"
OUTPUT_DIR="${OUTPUT_DIR:-${SRC_DIR}/dist}"

if [[ ! "$PLUGIN_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  printf 'Invalid PLUGIN_VERSION: %s (expected 1.2.5 or 1.2.5-rc.1)\n' "$PLUGIN_VERSION" >&2
  exit 1
fi

cd "$SRC_DIR"
export CGO_ENABLED=1
# Release builds must not silently rewrite go.mod/go.sum.
export GOFLAGS="${GOFLAGS:-} -mod=readonly"
GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"
if [[ "$GOOS" != linux || ! "$GOARCH" =~ ^(amd64|arm64)$ ]]; then
  printf 'Unsupported target: %s/%s; use Linux amd64 or arm64 with glibc.\n' "$GOOS" "$GOARCH" >&2
  exit 1
fi
if [[ "$GOOS" != "$(go env GOHOSTOS)" || "$GOARCH" != "$(go env GOHOSTARCH)" ]]; then
  printf 'build.sh runs tests natively; use a matching Linux runner/container for %s.\n' "$GOARCH" >&2
  exit 1
fi

if ! getconf GNU_LIBC_VERSION >/dev/null 2>&1; then
  printf 'A glibc build environment is required; use golang:1.26-bookworm, not Alpine.\n' >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"
ARTIFACT="${OUTPUT_DIR}/${PLUGIN_NAME}-${PLUGIN_VERSION}-${GOARCH}.so"
go mod download
go mod verify
go vet ./...
go test ./...
go build -trimpath -buildvcs=false -buildmode=c-shared \
  -ldflags "-X main.pluginVersion=${PLUGIN_VERSION}" \
  -o "$ARTIFACT" ./cmd/commandcodego
rm -f "${ARTIFACT%.so}.h"
printf '[build] ok: %s\n' "$ARTIFACT"
