#!/usr/bin/env bash
# Build cpa-plugin-commandcode-go for CLIProxyAPI.
# Runs INSIDE a golang:bookworm container (glibc toolchain, never alpine/musl);
# needs go >= 1.26. Emits a versioned .so next to this script.
set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_NAME="commandcode-go"
PLUGIN_VERSION="${PLUGIN_VERSION:-1.0.0}"

cd "${SRC_DIR}"
export CGO_ENABLED=1
export GOFLAGS=-mod=mod

go mod tidy
go vet ./...
go test ./...
go build -buildvcs=false -buildmode=c-shared -ldflags "-X main.pluginVersion=${PLUGIN_VERSION}" \
	-o "${SRC_DIR}/${PLUGIN_NAME}-v${PLUGIN_VERSION}.so" ./cmd/commandcodego
rm -f "${SRC_DIR}/${PLUGIN_NAME}-v${PLUGIN_VERSION}.h"

printf '[build] ok: %s/%s-v%s.so\n' "${SRC_DIR}" "${PLUGIN_NAME}" "${PLUGIN_VERSION}"
