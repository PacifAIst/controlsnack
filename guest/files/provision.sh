#!/bin/bash
# ControlSnack guest provisioning (PLAN.md section 7.5, step 4).
#
# Runs once, as root, from cloud-init on the very first boot of the image build, while the VM has user-mode
# networking. It installs the desktop, configures it, removes what must not be there, trims the disk and powers the
# VM off. Progress goes to the serial console, one "CS-PROVISION:" line per step; the host follows them, and a line
# "CS-PROVISION: done" before the power-off is what tells it that the image is complete.

set -Eeuo pipefail
export DEBIAN_FRONTEND=noninteractive LC_ALL=C

# The serial console is whatever the kernel uses as its last console: ttyS0 on x86_64, ttyAMA0 on aarch64.
console="/dev/$(awk '{print $NF}' /sys/class/tty/console/active 2>/dev/null || echo ttyS0)"
[ -c "$console" ] || console=/dev/null

# say writes straight to the console, once. (Writing to stderr as well would print every line twice: cloud-init sends
# the script's stderr to the same console.) The host follows these lines.
say() {
    printf 'CS-PROVISION: %s\n' "$*" >"$console" 2>/dev/null || printf 'CS-PROVISION: %s\n' "$*" >&2 || true
}

trap 'say "FAILED at line $LINENO: $BASH_COMMAND"; sync; sleep 1; systemctl poweroff --force' ERR

PACKAGES=(@PACKAGES@)

say "start ($(uname -m), $(. /etc/os-release && echo "$PRETTY_NAME"))"

say "installing packages (${#PACKAGES[@]} requested)"
apt-get update
apt-get install -y --no-install-recommends "${PACKAGES[@]}"

say "configuring the desktop"
glib-compile-schemas /usr/share/glib-2.0/schemas
udevadm control --reload || true
echo /usr/sbin/lightdm >/etc/X11/default-display-manager
install -d -o cs -g cs -m 0755 /home/cs/work /home/cs/Downloads
chmod 0755 /usr/local/lib/controlsnack/bootstrap.sh
systemctl set-default graphical.target
# The XFCE settings daemon publishes its own cursor setting over GTK's (gtk3-settings.ini), and its default is a
# blinking cursor; a screen that blinks never goes quiet. Change the packaged default rather than copy the file.
xsettings=/etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xsettings.xml
sed -i 's|\(name="CursorBlink" type="bool" value="\)true|\1false|' "$xsettings"
grep -q 'name="CursorBlink" type="bool" value="false"' "$xsettings" || { say "the cursor blink default could not be changed"; false; }

say "removing what must not be there"
for pkg in unattended-upgrades openssh-server xfce4-screensaver light-locker; do
    if dpkg -s "$pkg" >/dev/null 2>&1; then
        apt-get purge -y "$pkg"
    fi
done
apt-get autoremove -y --purge
systemctl disable apt-daily.timer apt-daily-upgrade.timer 2>/dev/null || true
systemctl mask apt-daily.service apt-daily-upgrade.service 2>/dev/null || true

say "making the boot quick and quiet"
if grep -q '^GRUB_TIMEOUT=' /etc/default/grub; then
    sed -i 's/^GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
else
    echo 'GRUB_TIMEOUT=0' >>/etc/default/grub
fi
update-grub
touch /etc/cloud/cloud-init.disabled
systemctl enable nftables

say "checking the result"
for cmd in lightdm xfce4-session xfce4-terminal chromium tesseract xdotool xset nft; do
    command -v "$cmd" >/dev/null 2>&1 || { say "missing command: $cmd"; false; }
done
id cs >/dev/null
test -x /usr/local/lib/controlsnack/bootstrap.sh
test -f /etc/xdg/autostart/controlsnack-bootstrap.desktop
test -f /etc/udev/rules.d/99-controlsnack.rules
test -d /home/cs/work
test -f /etc/xdg/xfce4/terminal/terminalrc
test -f /etc/gtk-3.0/settings.ini
grep -q ThunarDetailsView /etc/xdg/xfce4/xfconf/xfce-perchannel-xml/thunar.xml
for lang in zh ja ko; do
    fc-list ":lang=$lang" | grep -q . || { say "no font for the language $lang"; false; }
done
if getent passwd debian >/dev/null; then say "the default user 'debian' exists"; false; fi
if id -nG cs | tr ' ' '\n' | grep -qx -e sudo -e adm -e root; then say "cs has too many groups"; false; fi

say "cleaning up"
apt-get clean
rm -rf /var/lib/apt/lists/* /var/cache/apt/* /var/log/apt/* /tmp/* /var/tmp/*
journalctl --rotate >/dev/null 2>&1 || true
journalctl --vacuum-time=1s >/dev/null 2>&1 || true
cloud-init clean --logs >/dev/null 2>&1 || true
touch /etc/cloud/cloud-init.disabled
fstrim -av || true
sync

say "done"
sleep 1
systemctl poweroff --force
