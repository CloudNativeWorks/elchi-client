#!/bin/bash
set -euo pipefail

# -------------------------------
# 📁 Paths & Variables
# -------------------------------
APP_NAME="elchi-client"
SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="/etc/elchi"
BIN_DIR="$INSTALL_DIR/bin"
BUILD_OUTPUT="$BIN_DIR/$APP_NAME"
CONFIG_FILE_SOURCE="$SOURCE_DIR/config.yaml"
CONFIG_FILE_TARGET="$INSTALL_DIR/config.yaml"
SYSTEMD_SERVICE_NAME="elchi-client"
# -------------------------------
# 📂 Ensure target directories exist
# -------------------------------
echo "[+] Ensuring $BIN_DIR exists..."
sudo mkdir -p "$BIN_DIR"

# -------------------------------
# 🔨 Build Go binary directly into /etc/elchi/bin/
# -------------------------------
echo "[+] Building Go binary into $BUILD_OUTPUT..."

# Check for CGO dependencies
if ! command -v gcc >/dev/null 2>&1; then
    echo "[!] gcc not found, installing build dependencies..."
    sudo apt-get update -qq
    sudo apt-get install -y build-essential
fi

# Check for pkg-config
if ! command -v pkg-config >/dev/null 2>&1; then
    echo "[!] pkg-config not found, installing..."
    sudo apt-get update -qq
    sudo apt-get install -y pkg-config
fi

# Check for systemd development headers
if ! pkg-config --exists libsystemd 2>/dev/null; then
    echo "[!] systemd development headers not found, installing..."
    sudo apt-get update -qq
    sudo apt-get install -y libsystemd-dev
fi

# Build environment variables
BUILD_ENV=(
    "PATH=$PATH"
    "CGO_ENABLED=1"
)

# Add Go-specific variables if they exist
[[ -n "${GOPATH:-}" ]] && BUILD_ENV+=("GOPATH=$GOPATH")
[[ -n "${GOROOT:-}" ]] && BUILD_ENV+=("GOROOT=$GOROOT") 
[[ -n "${GOCACHE:-}" ]] && BUILD_ENV+=("GOCACHE=$GOCACHE")
[[ -n "${GOMODCACHE:-}" ]] && BUILD_ENV+=("GOMODCACHE=$GOMODCACHE")

# Read version from VERSION file
VERSION=$(cat "$SOURCE_DIR/VERSION" 2>/dev/null || echo "dev")

# Preserve all Go-related environment variables and enable CGO
sudo env "${BUILD_ENV[@]}" \
  go build -tags systemd -ldflags="-w -s -X main.version=$VERSION" -o "$BUILD_OUTPUT" "$SOURCE_DIR/main.go"

# -------------------------------
# 🔐 Permissions for binary
# -------------------------------
sudo chown elchi:elchi "$BUILD_OUTPUT"
sudo chmod 755 "$BUILD_OUTPUT"

# -------------------------------
# ⚙️  Copy config.yaml to /etc/elchi/
# -------------------------------
if [[ -f "$CONFIG_FILE_SOURCE" ]]; then
  echo "[+] Copying config.yaml to $INSTALL_DIR"
  sudo cp "$CONFIG_FILE_SOURCE" "$CONFIG_FILE_TARGET"
  sudo chown elchi:elchi "$CONFIG_FILE_TARGET"
  sudo chmod 644 "$CONFIG_FILE_TARGET"
else
  echo "[!] config.yaml not found in $SOURCE_DIR — skipping config copy"
fi

# -------------------------------
# 🔁 Reload & Restart systemd service
# -------------------------------
echo "[+] Reloading systemd and restarting $SYSTEMD_SERVICE_NAME..."
sudo systemctl daemon-reexec
sudo systemctl restart "$SYSTEMD_SERVICE_NAME"

# -------------------------------
# ✅ Done
# -------------------------------
echo "[✓] Build + Deploy + Restart complete."
echo "    Binary : $BUILD_OUTPUT"
echo "    Config : $CONFIG_FILE_TARGET"

# -------------------------------
# 📜 Tail logs
# -------------------------------
echo "[+] Tailing logs for $SYSTEMD_SERVICE_NAME..."
sudo journalctl -u "$SYSTEMD_SERVICE_NAME" -f --no-pager