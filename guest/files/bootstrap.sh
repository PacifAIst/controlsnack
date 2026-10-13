#!/bin/bash
# ControlSnack guest stub (PLAN.md section 5.1).
#
# It runs at the start of the desktop session, as the desktop user, and waits on the virtio-serial port to the host.
# The host pushes the guest agent over that port: one text line, then the raw bytes of the program.
#
#   host  -> CSBOOT/1 PUSH arch=<x86_64|aarch64> size=<n> sha256=<hex> nonce=<hex>\n<n raw bytes>
#   guest -> CSBOOT/1 OK sha256=<hex>\n          or          CSBOOT/1 ERR <reason>\n
#
# The stub checks the line, the architecture and the size, stores the bytes where only this user can reach them,
# verifies their SHA-256, answers, and runs the agent with the port on file descriptor 3. When the agent ends, the
# stub waits for the next push, so an agent that crashed is replaced without rebooting the VM.
#
# CS_PORT and CS_READ_TIMEOUT exist for the tests of this script; in the VM both have their defaults. CS_PORT=- means
# "file descriptor 3 is already the port", which is how a test hands the script one end of a socket pair.

set -u

PORT="${CS_PORT:-/dev/virtio-ports/org.controlsnack.agent.0}"
READ_TIMEOUT="${CS_READ_TIMEOUT:-300}"
MAX_AGENT=$((64 * 1024 * 1024))

log() { printf 'controlsnack-bootstrap: %s\n' "$*" >&2; }

# reply sends one answer line to the host.
reply() { printf 'CSBOOT/1 %s\n' "$*" >&3; }

# reject answers with an error and throws away what the host is still sending: after a refused push the stream has
# no line boundaries to find, so everything that arrives within two quiet seconds is dropped.
reject() {
    log "refusing the push: $1"
    reply "ERR $1"
    timeout 2 cat <&3 >/dev/null 2>&1 || true
}

umask 077

base="${XDG_RUNTIME_DIR:-}"
if [ -z "$base" ] || [ ! -d "$base" ]; then
    base="/tmp/controlsnack-$(id -u)"
fi
dir="$base/controlsnack"
mkdir -p -m 0700 "$dir" || { log "cannot create $dir"; exit 1; }
chmod 0700 "$dir"

# The device appears when udev has seen the port; the rule gives it to this user.
if [ "$PORT" != "-" ]; then
    until [ -e "$PORT" ]; do sleep 0.2; done
    exec 3<>"$PORT" || { log "cannot open $PORT"; exit 1; }
fi

arch="$(uname -m)"
re='^CSBOOT/1 PUSH arch=(x86_64|aarch64) size=([0-9]{1,9}) sha256=([0-9a-f]{64}) nonce=([0-9a-f]{16,64})$'

while :; do
    line=""
    # read returns at once with a failure while the host is not connected (the port reads as end of file).
    if ! IFS= read -r -n 600 -u 3 line; then
        sleep 0.5
        continue
    fi
    if [ "${#line}" -gt 512 ] || ! [[ $line =~ $re ]]; then
        reject "bad-line"
        continue
    fi
    push_arch="${BASH_REMATCH[1]}"
    size=$((10#${BASH_REMATCH[2]}))
    want="${BASH_REMATCH[3]}"
    nonce="${BASH_REMATCH[4]}"

    if [ "$push_arch" != "$arch" ]; then
        reject "arch-mismatch-$arch"
        continue
    fi
    if [ "$size" -le 0 ] || [ "$size" -gt "$MAX_AGENT" ]; then
        reject "bad-size"
        continue
    fi

    new="$dir/agent.new"
    rm -f "$new"
    timeout "$READ_TIMEOUT" head -c "$size" <&3 >"$new"
    got="$(stat -c %s "$new" 2>/dev/null || echo 0)"
    if [ "$got" -ne "$size" ]; then
        rm -f "$new"
        reply "ERR short-read"
        continue
    fi
    sum="$(sha256sum "$new")"
    sum="${sum%% *}"
    if [ "$sum" != "$want" ]; then
        rm -f "$new"
        reply "ERR sha256-mismatch"
        continue
    fi
    chmod 0700 "$new"
    mv -f "$new" "$dir/agent"
    reply "OK sha256=$sum"

    log "starting the agent ($size bytes, sha256 ${sum:0:12})"
    "$dir/agent" --fd 3 --nonce "$nonce" </dev/null
    log "the agent ended with status $?; waiting for the next push"
done
