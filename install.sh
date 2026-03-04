#!/bin/sh
set -e

REPO="alexanderfrey/tailbus"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)      echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  *)              echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "Detected platform: ${OS}/${ARCH}"

# Fetch latest release tag
VERSION=$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
if [ -z "$VERSION" ]; then
  echo "Failed to fetch latest release version"
  exit 1
fi

# Strip leading v for archive name
VERSION_NUM="${VERSION#v}"

echo "Latest release: ${VERSION}"

ARCHIVE="tailbus_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading ${URL}..."
curl -sSL -o "${TMPDIR}/${ARCHIVE}" "$URL"

echo "Extracting..."
tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR"

# Determine install directory
INSTALL_DIR="${HOME}/.local/bin"
mkdir -p "$INSTALL_DIR"
echo "Installing to ${INSTALL_DIR}..."

for bin in tailbus-coord tailbusd tailbus; do
  if [ -f "${TMPDIR}/${bin}" ]; then
    install -m 755 "${TMPDIR}/${bin}" "${INSTALL_DIR}/${bin}"
  fi
done

# Create ~/.tailbus directory for credentials
mkdir -p "${HOME}/.tailbus" 2>/dev/null || true

echo ""
echo "tailbus ${VERSION} installed successfully!"
echo ""

# Ensure install dir is in PATH
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    # Detect shell profile
    PROFILE=""
    if [ -n "$ZSH_VERSION" ] || [ "$(basename "$SHELL")" = "zsh" ]; then
      PROFILE="${HOME}/.zshrc"
    elif [ -f "${HOME}/.bashrc" ]; then
      PROFILE="${HOME}/.bashrc"
    elif [ -f "${HOME}/.bash_profile" ]; then
      PROFILE="${HOME}/.bash_profile"
    elif [ -f "${HOME}/.profile" ]; then
      PROFILE="${HOME}/.profile"
    fi

    EXPORT_LINE="export PATH=\"${INSTALL_DIR}:\$PATH\""

    if [ -n "$PROFILE" ]; then
      if ! grep -qF ".local/bin" "$PROFILE" 2>/dev/null; then
        echo "" >> "$PROFILE"
        echo "# Added by tailbus installer" >> "$PROFILE"
        echo "$EXPORT_LINE" >> "$PROFILE"
        echo "Added ${INSTALL_DIR} to PATH in ${PROFILE}"
      fi
    fi

    # Also export for current session
    export PATH="${INSTALL_DIR}:$PATH"
    echo ""
    ;;
esac

# --- Install and enable user service ---
TAILBUSD_BIN="${INSTALL_DIR}/tailbusd"
LOG_FILE="${HOME}/.tailbus/tailbusd.log"

if [ "$OS" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
  UNIT_DIR="${HOME}/.config/systemd/user"
  UNIT_FILE="${UNIT_DIR}/tailbusd.service"
  mkdir -p "$UNIT_DIR"

  cat > "$UNIT_FILE" <<UNIT
[Unit]
Description=Tailbus daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${TAILBUSD_BIN}
Restart=on-failure
RestartSec=5
StandardOutput=append:${LOG_FILE}
StandardError=append:${LOG_FILE}

[Install]
WantedBy=default.target
UNIT

  systemctl --user daemon-reload
  if systemctl --user is-active --quiet tailbusd 2>/dev/null; then
    echo "Restarting tailbusd service..."
    systemctl --user restart tailbusd
  else
    systemctl --user enable --now tailbusd
  fi
  echo "tailbusd service installed and running (systemd user unit)"
  echo ""
  echo "Get started:"
  echo "  tailbus login                          # authenticate with Google"
  echo "  tailbus status                         # check connection status"
  echo ""
  echo "Manage the daemon:"
  echo "  systemctl --user status tailbusd       # service status"
  echo "  systemctl --user restart tailbusd      # restart"
  echo "  systemctl --user stop tailbusd         # stop"
  echo "  tail -f ~/.tailbus/tailbusd.log        # logs"

elif [ "$OS" = "darwin" ]; then
  PLIST_DIR="${HOME}/Library/LaunchAgents"
  PLIST_FILE="${PLIST_DIR}/co.tailbus.daemon.plist"
  PLIST_LABEL="co.tailbus.daemon"
  mkdir -p "$PLIST_DIR"

  cat > "$PLIST_FILE" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${PLIST_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${TAILBUSD_BIN}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>${LOG_FILE}</string>
    <key>StandardErrorPath</key>
    <string>${LOG_FILE}</string>
    <key>ThrottleInterval</key>
    <integer>5</integer>
</dict>
</plist>
PLIST

  GUI_UID=$(id -u)
  # Unload if already loaded (upgrade path)
  if launchctl print "gui/${GUI_UID}/${PLIST_LABEL}" >/dev/null 2>&1; then
    echo "Stopping existing tailbusd service..."
    launchctl bootout "gui/${GUI_UID}/${PLIST_LABEL}" 2>/dev/null || true
  fi
  launchctl bootstrap "gui/${GUI_UID}" "$PLIST_FILE"
  echo "tailbusd service installed and running (launchd)"
  echo ""
  echo "Get started:"
  echo "  tailbus login                          # authenticate with Google"
  echo "  tailbus status                         # check connection status"
  echo ""
  echo "Manage the daemon:"
  echo "  launchctl list | grep tailbus          # service status"
  echo "  launchctl kickstart -k gui/\$(id -u)/co.tailbus.daemon   # restart"
  echo "  launchctl kill TERM gui/\$(id -u)/co.tailbus.daemon      # stop"
  echo "  tail -f ~/.tailbus/tailbusd.log        # logs"

else
  echo "Get started:"
  echo "  tailbusd               # start daemon -> login with Google -> connected"
  echo "  tailbus login          # authenticate without starting daemon"
  echo "  tailbus status         # check connection status"
fi
echo ""
