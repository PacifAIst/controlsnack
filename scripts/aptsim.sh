#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
# SPDX-License-Identifier: AGPL-3.0-only
#
# Simulate "apt-get install --no-install-recommends <packages>" against the Debian 13 (trixie) archive, without root
# and without touching the system: private apt state, an empty dpkg status (so the closure it reports is an upper
# bound), packages from the arguments. Use it after changing guest.Packages to find a wrong package name or a
# conflict in seconds instead of 30 minutes into an image build.
#
#   scripts/aptsim.sh update xfce4 chromium lightdm             # first run: fetches the package index
#   ARCH=arm64 scripts/aptsim.sh xfce4 chromium                 # later runs reuse it
#
# Needs apt-get (Debian, Ubuntu, WSL) and network access for "update".
set -euo pipefail
arch="${ARCH:-amd64}"
root="${XDG_CACHE_HOME:-$HOME/.cache}/controlsnack-dev/aptsim-$arch"
mkdir -p "$root"/{state/lists/partial,cache/archives/partial,etc}
: > "$root/status"
cat > "$root/etc/sources.list" <<LIST
deb [trusted=yes arch=$arch] https://deb.debian.org/debian trixie main
LIST
opts=(-o Dir::Etc::sourcelist="$root/etc/sources.list" -o Dir::Etc::sourceparts=/dev/null
      -o Dir::State="$root/state" -o Dir::State::status="$root/status" -o Dir::Cache="$root/cache"
      -o APT::Architecture="$arch" -o APT::Architectures="$arch" -o Debug::NoLocking=1
      -o Acquire::Languages=none -o APT::Get::List-Cleanup=0 -o Dir::Etc::trusted=/dev/null -o Dir::Etc::trustedparts=/dev/null)
if [ "${1:-}" = "update" ]; then
  apt-get "${opts[@]}" update 2>&1 | tail -5
  shift
fi
apt-get "${opts[@]}" -s install -y --no-install-recommends "$@" 2>&1
