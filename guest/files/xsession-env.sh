# ControlSnack session environment. Sourced by /etc/X11/Xsession before the desktop starts.

# Accessibility: AT-SPI must be on for GTK, Qt and Electron applications, or the agent sees no element tree.
export GTK_MODULES="gail:atk-bridge"
export NO_AT_BRIDGE=0
export QT_ACCESSIBILITY=1
export QT_LINUX_ACCESSIBILITY_ALWAYS_ON=1
export GNOME_ACCESSIBILITY=1

# The only way out of the VM is the proxy the agent runs on loopback, when the network is enabled at all.
export http_proxy="http://127.0.0.1:3128"
export https_proxy="http://127.0.0.1:3128"
export HTTP_PROXY="$http_proxy"
export HTTPS_PROXY="$https_proxy"
export no_proxy="localhost,127.0.0.1,::1"
export NO_PROXY="$no_proxy"

# A screen that blanks, locks or powers down is a screen the model cannot see.
if command -v xset >/dev/null 2>&1; then
    xset s off s noblank -dpms 2>/dev/null || true
fi
