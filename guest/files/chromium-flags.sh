# ControlSnack: read by /usr/bin/chromium (Debian's wrapper sources /etc/chromium.d/*).
# Renderer accessibility must be on from the start for the element tree of web pages.
export CHROMIUM_FLAGS="$CHROMIUM_FLAGS --force-renderer-accessibility --no-first-run --no-default-browser-check --password-store=basic"
