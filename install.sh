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

echo "Get started:"
echo "  tailbusd               # start daemon -> login with Google -> connected"
echo "  tailbus login          # authenticate without starting daemon"
echo "  tailbus status         # check connection status"
echo ""
