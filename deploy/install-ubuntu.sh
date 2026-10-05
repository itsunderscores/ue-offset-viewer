#!/usr/bin/env bash
# Installs the SDK viewer as a systemd service on Ubuntu and opens port 1336.
# Run from the uploaded dist/linux folder:  sudo ./install-ubuntu.sh
#
# If you see "bash\r: No such file" the upload converted line endings. Fix with:
#   sed -i 's/\r$//' install-ubuntu.sh sdkviewer.service

# Re-exec under bash if someone ran "sh install-ubuntu.sh" (dash lacks pipefail etc.).
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -euo pipefail

PORT=1336
DIR=/opt/sdkviewer
HERE="$(cd "$(dirname "$0")" && pwd)"

if [[ $EUID -ne 0 ]]; then
  echo "run as root: sudo $0" >&2
  exit 1
fi

[[ -f "$HERE/sdkviewer" ]] || { echo "sdkviewer binary not found next to this script" >&2; exit 1; }

# --- user + files
id -u sdkviewer &>/dev/null || useradd --system --no-create-home --shell /usr/sbin/nologin sdkviewer
mkdir -p "$DIR"
install -m 755 "$HERE/sdkviewer" "$DIR/sdkviewer"
if [[ -f "$HERE/sdk.txt" ]]; then
  install -m 644 "$HERE/sdk.txt" "$DIR/sdk.txt"
elif [[ ! -f "$DIR/sdk.txt" ]]; then
  echo "WARNING: no sdk.txt found. Copy one to $DIR/sdk.txt, then: sudo systemctl restart sdkviewer" >&2
fi
if [[ -f "$HERE/offsets.json" ]]; then
  install -m 644 "$HERE/offsets.json" "$DIR/offsets.json"
elif [[ ! -f "$DIR/offsets.json" ]]; then
  echo "NOTE: no offsets.json found; the Important Offsets view will be empty until you create $DIR/offsets.json" >&2
fi
chown -R sdkviewer:sdkviewer "$DIR"

# --- service (strip any CRLF the upload may have introduced; systemd chokes on "\r")
sed 's/\r$//' "$HERE/sdkviewer.service" > /etc/systemd/system/sdkviewer.service
chmod 644 /etc/systemd/system/sdkviewer.service
systemctl daemon-reload
systemctl enable --now sdkviewer
systemctl restart sdkviewer

# --- firewall
if command -v ufw &>/dev/null; then
  ufw allow "$PORT/tcp" comment "SDK viewer" >/dev/null
  if ufw status | grep -q "Status: active"; then
    echo "ufw: allowed $PORT/tcp"
  else
    echo "ufw is installed but inactive; rule added for when it is enabled (ufw enable)."
  fi
elif command -v firewall-cmd &>/dev/null; then
  firewall-cmd --permanent --add-port="$PORT/tcp" >/dev/null && firewall-cmd --reload >/dev/null
  echo "firewalld: allowed $PORT/tcp"
else
  echo "no ufw/firewalld found; if you use iptables:  iptables -I INPUT -p tcp --dport $PORT -j ACCEPT"
fi

sleep 2
echo
if systemctl is-active --quiet sdkviewer; then
  systemctl --no-pager --lines=3 status sdkviewer || true
else
  echo "!! sdkviewer is NOT running. Last log lines:"
  journalctl -u sdkviewer -n 15 --no-pager || true
  echo
  echo "Common causes: missing /opt/sdkviewer/sdk.txt, or the port is already in use."
fi
echo
IP=$(hostname -I 2>/dev/null | awk '{print $1}')
echo "SDK viewer should be reachable at: http://${IP:-<server-ip>}:$PORT"
echo "If you are on a cloud VM (AWS/Azure/GCP/Oracle/Hetzner...) also open TCP $PORT in the provider's security group / firewall."
echo
echo "Useful: sudo journalctl -u sdkviewer -f      # logs"
echo "        sudo nano $DIR/offsets.json          # edit Important Offsets (reloads automatically, no restart needed)"
echo "        sudo systemctl restart sdkviewer     # after replacing $DIR/sdk.txt (it also hot-reloads on change)"
