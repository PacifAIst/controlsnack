#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
# SPDX-License-Identifier: AGPL-3.0-only
#
# Run a command inside WSL (or any Linux) with a private, checksum-verified Go toolchain, from the repository root.
# Used on Windows machines that have WSL but no Linux Go, to get what a Windows host cannot give: the race detector
# (needs a C compiler) and the Linux-only code paths.
#
#   wsl -e bash /mnt/c/path/to/controlsnack-local/scripts/wsl-go.sh env CGO_ENABLED=1 go test -race ./internal/...
#
# From Git Bash on Windows prefix the call with MSYS_NO_PATHCONV=1 so the /mnt/c path is not rewritten.
# The toolchain is installed once under ~/.cache/controlsnack-dev; nothing outside that directory is touched and
# nothing needs sudo. Override the version with GO_VERSION.
set -euo pipefail

GO_VERSION="${GO_VERSION:-1.26.9}"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/controlsnack-dev"
GOROOT_DIR="$CACHE/go-$GO_VERSION/go"

case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

if [ ! -x "$GOROOT_DIR/bin/go" ]; then
  dir="$CACHE/go-$GO_VERSION"
  mkdir -p "$dir"
  file="go$GO_VERSION.linux-$arch.tar.gz"
  echo "installing Go $GO_VERSION into $dir" >&2
  curl -fsSL "https://go.dev/dl/?mode=json&include=all" -o "$dir/releases.json"
  sum=$(tr "," "\n" < "$dir/releases.json" | grep -A12 "\"filename\": \"$file\"" | grep '"sha256"' | head -1 | sed 's/.*: "\(.*\)"/\1/')
  [ -n "$sum" ] || { echo "no published checksum for $file" >&2; exit 1; }
  curl -fsSL -o "$dir/go.tgz" "https://go.dev/dl/$file"
  echo "$sum  $dir/go.tgz" | sha256sum -c - >&2
  tar -C "$dir" -xzf "$dir/go.tgz"
  rm -f "$dir/go.tgz"
fi

export PATH="$GOROOT_DIR/bin:$PATH"
export GOPATH="$CACHE/gopath" GOCACHE="$CACHE/gocache" GOTOOLCHAIN=local GOFLAGS=-buildvcs=false
cd "$(dirname "${BASH_SOURCE[0]}")/.."
exec "$@"
