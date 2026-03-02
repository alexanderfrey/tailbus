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
INSTALL_DIR="/usr/local/bin"
NEED_SUDO=""
if [ ! -w "$INSTALL_DIR" ]; then
  if command -v sudo >/dev/null 2>&1; then
    NEED_SUDO="sudo"
    echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  else
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
    echo "Installing to ${INSTALL_DIR}..."
  fi
else
  echo "Installing to ${INSTALL_DIR}..."
fi

for bin in tailbus-coord tailbusd tailbus; do
  if [ -f "${TMPDIR}/${bin}" ]; then
    $NEED_SUDO install -m 755 "${TMPDIR}/${bin}" "${INSTALL_DIR}/${bin}"
  fi
done

echo ""
echo "tailbus ${VERSION} installed successfully!"
echo "  tailbus-coord  - coordination server"
echo "  tailbusd        - node daemon"
echo "  tailbus         - CLI tool"
echo ""

# Check if install dir is in PATH
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *) echo "NOTE: ${INSTALL_DIR} is not in your PATH. Add it with:"
     echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
     echo "" ;;
esac
