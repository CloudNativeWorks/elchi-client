#!/usr/bin/env bash
#
# elchi-install.sh (v3)
# --------------------------------------------------
# • Kernel / sysctl / limits settings
# • elchi & envoyuser users
# • /etc/elchi and /var/lib/elchi hierarchy
# • elchi-client systemd service
# • Logging infrastructure (logrotate, filebeat)
# • Optional: FRR installation (--enable-bgp)
#

set -euo pipefail
shopt -s inherit_errexit

###############################################################################
# GLOBAL VARIABLES AND CONFIGURATION
###############################################################################

# Command line arguments
ENABLE_FRR=false

# Configuration parameters
CLIENT_NAME=""
SERVER_HOST=""
SERVER_PORT=""
SERVER_TLS=""
SERVER_TOKEN=""
CLIENT_CLOUD=""

# Public mirror base for binaries. elchi-archive is the only PUBLIC repo; every
# component's source repo is private. Install-time downloads are unauthenticated,
# so they must hit the public mirror (an elchi-archive release), never a private
# source repo. The elchi-archive build-elchi-client.yml substitutes this
# placeholder with the release's asset base URL at publish time; both the client
# and the bundled shield binary live in that release. A literal, unreplaced
# placeholder (i.e. a direct run from this repo) falls back to the source repos.
MIRROR_BASE_URL="__MIRROR_BASE_URL__"
[[ "$MIRROR_BASE_URL" == *MIRROR_BASE_URL* ]] && MIRROR_BASE_URL=""

# When this script is run directly (e.g. `curl raw .../main/elchi-install.sh | bash`)
# the placeholder above is NOT substituted, so resolve the mirror at runtime from
# elchi-archive's PUBLIC releases API. This keeps install-time downloads on the
# public mirror — important because the shield source repo is private and an
# unauthenticated fetch from it would fail. Idempotent: only resolves when empty.
resolve_mirror_base_url() {
  [[ -n "$MIRROR_BASE_URL" ]] && return 0
  local archive_repo="CloudNativeWorks/elchi-archive" mtag
  mtag="$(curl -fsSL "https://api.github.com/repos/${archive_repo}/releases?per_page=100" 2>/dev/null \
            | grep '"tag_name":' \
            | sed -E 's/.*"(elchi-client-[^"]+)".*/\1/' \
            | grep '^elchi-client-' \
            | head -n1 || true)"
  if [[ -n "${mtag:-}" ]]; then
    MIRROR_BASE_URL="https://github.com/${archive_repo}/releases/download/${mtag}"
    info "📦 resolved public mirror: ${MIRROR_BASE_URL}"
  else
    warn "⚠️  could not resolve the elchi-archive mirror — falling back to source repos"
  fi
}

# elchi-shield (the Envoy ext_proc API-security sidecar) is installed ALONGSIDE
# the client on the same edge host, in the same install — never separately.
# Use --no-shield to skip it. SHIELD_VERSION is pinned at release time by the
# elchi-archive workflow (the __SHIELD_VERSION__ placeholder) for reporting; the
# binary itself is fetched from MIRROR_BASE_URL above. A literal, unreplaced
# placeholder (i.e. a direct run from this repo) means "latest from source".
INSTALL_SHIELD=true
SHIELD_VERSION="__SHIELD_VERSION__"
[[ "$SHIELD_VERSION" == *SHIELD_VERSION* ]] && SHIELD_VERSION=""
# Optional shield telemetry sinks (off by default; env fallbacks for CI/automation).
SHIELD_AUDIT_DSN="${ELCHI_SHIELD_AUDIT_CLICKHOUSE_DSN:-}"
SHIELD_METRICS_OTLP="${ELCHI_SHIELD_METRICS_OTLP_ENDPOINT:-}"
SHIELD_METRICS_INSECURE="${ELCHI_SHIELD_METRICS_OTLP_INSECURE:-}"

# System users
ELCHI_USER="elchi"
ENVOY_USER="envoyuser"

# Logging configuration
ELCHI_LOG_DIR="/var/log/elchi"
LOGROTATE_CONFIG="/etc/logrotate.d/elchi"
LOGROTATE_CRON="/etc/cron.d/logrotate-5min"
LOGROTATE_SCRIPT="/usr/local/bin/logrotate-5min.sh"
FILEBEAT_CONFIG="/etc/filebeat/filebeat.yml"

# Directory paths
ELCHI_DIR="/etc/elchi"
ELCHI_BIN_DIR="$ELCHI_DIR/bin"
ELCHI_CONFIG="$ELCHI_DIR/config.yaml"
ELCHI_VAR_LIB="/var/lib/elchi"
ELCHI_VAR_DIRS=( bootstraps envoys waf hotrestarter lua tmp )

# Configuration files
SYSCTL_FILE="/etc/sysctl.d/elchi.conf"
LIMITS_FILE="/etc/security/limits.d/elchi.conf"
MODPROBE_FILE="/etc/modprobe.d/elchi.conf"
SUDO_FILE="/etc/sudoers.d/99-${ELCHI_USER}"
SERVICE_FILE="/etc/systemd/system/elchi-client.service"

# ANSI colors for output
C_RST='\033[0m'    # reset
C_INF='\033[1;34m' # bright blue   – INFO
C_OK='\033[1;32m'  # bright green  – SUCCESS
C_WRN='\033[1;33m' # bright yellow – WARNING
C_ERR='\033[1;31m' # bright red    – ERROR
C_DBG='\033[1;30m' # bright black  – DEBUG

###############################################################################
# UTILITY FUNCTIONS
###############################################################################

# Logging functions
info() { printf "${C_INF}[INFO] %s${C_RST}\n" "$*"; }
ok()   { printf "${C_OK}[ OK ] %s${C_RST}\n"  "$*"; }
warn() { printf "${C_WRN}[WARN] %s${C_RST}\n" "$*"; }
fail() { printf "${C_ERR}[FAIL] %s${C_RST}\n" "$*"; exit 1; }
debug() { printf "${C_DBG}[DBG ] %s${C_RST}\n" "$*"; }

# Command execution with output capture and colorized result
run() {
  info "\$ $*"
  if out="$("$@" 2>&1)"; then
    [[ -n $out ]] && ok "$out" || ok "done"
  else
    fail "$* → $out"
  fi
}

# Command execution with pipe support
run_with_pipe() {
  info "\$ $1"
  if out=$(bash -c "$1" 2>&1); then
    ok "${out:-done}"
  else
    fail "$1 → $out"
  fi
}


# Cleanup function for interrupted processes
cleanup_on_exit() {
  echo ""
  echo "🛑 Script interrupted - cleaning up..."
  pkill -f "apt-get.*install.*frr" 2>/dev/null || true
  pkill -f "man-db" 2>/dev/null || true
  rm -f /var/lib/apt/lists/lock /var/cache/apt/archives/lock /var/lib/dpkg/lock-frontend 2>/dev/null || true
  echo "🧹 Cleanup completed"
  exit 130
}

###############################################################################
# COMMAND LINE ARGUMENT PARSING
###############################################################################

while [[ $# -gt 0 ]]; do
  case $1 in
    --enable-bgp)
      ENABLE_FRR=true
      shift
      ;;
    --name=*)
      CLIENT_NAME="${1#*=}"
      shift
      ;;
    --host=*)
      SERVER_HOST="${1#*=}"
      shift
      ;;
    --port=*)
      SERVER_PORT="${1#*=}"
      shift
      ;;
    --tls=*)
      SERVER_TLS="${1#*=}"
      shift
      ;;
    --token=*)
      SERVER_TOKEN="${1#*=}"
      shift
      ;;
    --cloud=*)
      CLIENT_CLOUD="${1#*=}"
      shift
      ;;
    --no-shield)
      INSTALL_SHIELD=false
      shift
      ;;
    --shield-version=*)
      SHIELD_VERSION="${1#*=}"
      shift
      ;;
    --shield-audit-dsn=*)
      SHIELD_AUDIT_DSN="${1#*=}"
      shift
      ;;
    --shield-metrics-otlp=*)
      SHIELD_METRICS_OTLP="${1#*=}"
      shift
      ;;
    --shield-metrics-insecure)
      SHIELD_METRICS_INSECURE=1
      shift
      ;;
    --help|-h)
      echo "Usage: $0 --name=NAME --host=HOST --port=PORT --tls=true|false --token=TOKEN [OPTIONS]"
      echo ""
      echo "Required Parameters:"
      echo "  --name=NAME                Client name"
      echo "  --host=HOST                Server host address"  
      echo "  --port=PORT                Server port (1-65535)"
      echo "  --tls=true|false           Enable TLS"
      echo "  --token=TOKEN              Authentication token (min 8 chars)"
      echo ""
      echo "Optional Parameters:"
      echo "  --cloud=CLOUD              Cloud name (defaults to 'other')"
      echo "  --enable-bgp               Install and configure FRR routing"
      echo "  --no-shield                Do NOT install the elchi-shield sidecar (installed by default)"
      echo "  --shield-version=vX.Y.Z    Pin the elchi-shield release (default: latest)"
      echo "  --shield-audit-dsn=DSN     Send shield audit events to central ClickHouse (else off)"
      echo "  --shield-metrics-otlp=H:P  Push shield metrics to an OTel Collector (OTLP/gRPC)"
      echo "  --shield-metrics-insecure  Plaintext gRPC to the shield metrics collector"
      echo "  --help, -h                 Show this help message"
      echo ""
      echo "Important Note:"
      echo "  If deploying on OpenStack, specify --cloud=YOUR_CLOUD_NAME (use the cloud name from Controller)"
      echo ""
      echo "Examples:"
      echo "  $0 --name=web-server-01 --host=backend.elchi.io --port=443 --tls=true --token=your-token"
      echo "  $0 --name=dev-client --host=10.0.0.1 --port=50051 --tls=false --token=dev-token --cloud=test-env"
      echo "  $0 --name=openstack-vm --host=controller.elchi.io --port=443 --tls=true --token=prod-token --cloud=my-openstack"
      echo "  $0 --enable-bgp --name=edge-router --host=controller.elchi.io --port=443 --tls=true --token=prod-token --cloud=production"
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      echo "Use --help for usage information"
      exit 1
      ;;
  esac
done

# Validate required parameters
info "🔍 Validating required configuration parameters"
MISSING_PARAMS=()

if [[ -z "$CLIENT_NAME" ]]; then
  MISSING_PARAMS+=("--name")
fi

if [[ -z "$SERVER_HOST" ]]; then
  MISSING_PARAMS+=("--host")
fi

if [[ -z "$SERVER_PORT" ]]; then
  MISSING_PARAMS+=("--port")
fi

if [[ -z "$SERVER_TLS" ]]; then
  MISSING_PARAMS+=("--tls")
fi

if [[ -z "$SERVER_TOKEN" ]]; then
  MISSING_PARAMS+=("--token")
fi


if [[ ${#MISSING_PARAMS[@]} -gt 0 ]]; then
  fail "❌ Missing required parameters: ${MISSING_PARAMS[*]}"
  echo ""
  echo "Example usage:"
  echo "  $0 --name=web-server-01 --host=backend.elchi.io --port=443 --tls=true --token=your-token"
  echo ""
  echo "Use --help for full parameter list"
  exit 1
fi

# Validate parameter values
if [[ "$SERVER_TLS" != "true" && "$SERVER_TLS" != "false" ]]; then
  fail "❌ Invalid --tls value: '$SERVER_TLS'. Must be 'true' or 'false'"
fi

if ! [[ "$SERVER_PORT" =~ ^[0-9]+$ ]] || [[ "$SERVER_PORT" -lt 1 ]] || [[ "$SERVER_PORT" -gt 65535 ]]; then
  fail "❌ Invalid --port value: '$SERVER_PORT'. Must be a number between 1-65535"
fi

if [[ ${#SERVER_TOKEN} -lt 8 ]]; then
  fail "❌ Invalid --token value: Token must be at least 8 characters long"
fi


ok "✅ All required parameters validated successfully"

###############################################################################
# APT SOURCES CONFIGURATION
###############################################################################

setup_sources_list() {
  local arch codename base_url source_file
  
  arch=$(dpkg --print-architecture)
  codename=$(lsb_release -cs)
  source_file="/etc/apt/sources.list"
  
  echo "[INFO] Detected architecture: $arch"
  echo "[INFO] Ubuntu codename: $codename"
  
  # Modern Ubuntu systems disable ubuntu.sources file
  if [[ -f "/etc/apt/sources.list.d/ubuntu.sources" ]]; then
    mv "/etc/apt/sources.list.d/ubuntu.sources" "/etc/apt/sources.list.d/ubuntu.sources.disabled"
    echo "[INFO] ubuntu.sources file disabled"
  fi
  
  # Backup existing sources.list
  cp "$source_file" "${source_file}.bak"
  
  # Select correct URL based on architecture
  if [[ "$arch" == "arm64" || "$arch" == "armhf" ]]; then
    base_url="http://ports.ubuntu.com/ubuntu-ports"
  else
    base_url="http://archive.ubuntu.com/ubuntu"
  fi
  
  # Create new sources.list
  cat > "$source_file" <<EOF
deb $base_url $codename main restricted universe multiverse
deb $base_url $codename-updates main restricted universe multiverse
deb $base_url $codename-security main restricted universe multiverse
deb $base_url $codename-backports main restricted universe multiverse
EOF
  
  echo "[INFO] sources.list updated: $base_url"
  
  # Update APT package cache
  apt-get clean
  apt-get update
}

###############################################################################
# ELASTIC REPOSITORY SETUP
# Add Elastic's official APT repository for Filebeat installation
###############################################################################

setup_elastic_repository() {
  info "📦 Setting up Elastic APT repository"

  # Check if repository is already configured
  if [[ -f /etc/apt/sources.list.d/elastic-8.x.list ]]; then
    ok "✅ Elastic repository already configured"
    return 0
  fi

  # Install prerequisites
  info "🔧 Installing prerequisites for Elastic repository"
  run apt-get install -y curl gnupg apt-transport-https

  # Add Elastic GPG key
  info "🔑 Adding Elastic GPG key"
  curl -fsSL https://artifacts.elastic.co/GPG-KEY-elasticsearch | gpg --dearmor -o /usr/share/keyrings/elastic-keyring.gpg

  # Add Elastic repository
  info "📦 Adding Elastic 8.x repository"
  echo "deb [signed-by=/usr/share/keyrings/elastic-keyring.gpg] https://artifacts.elastic.co/packages/8.x/apt stable main" | tee /etc/apt/sources.list.d/elastic-8.x.list > /dev/null

  # Update package cache
  run apt-get update -qq

  ok "✅ Elastic repository configured"
}

###############################################################################
# FRR INSTALLATION AND CONFIGURATION
# Install FRR 10.4.0 from official repository with zebra + bgpd only
# gRPC enabled, auto-save configuration enabled
###############################################################################

install_configure_frr() {
  echo "📦 Installing FRR 10.4.0 from official repository"

  # Step 1: Clean up any existing FRR installations
  echo "🧹 Cleaning up existing FRR installations..."
  systemctl stop frr 2>/dev/null || true
  apt-get remove -y frr frr-pythontools 2>/dev/null || true
  rm -f /etc/apt/sources.list.d/frr.list
  rm -f /etc/apt/preferences.d/frr*
  rm -f /usr/share/keyrings/frrouting.gpg

  # Step 2: Install prerequisites
  echo "🔧 Installing prerequisites..."
  run apt-get update -qq
  run apt-get install -y curl gnupg lsb-release apt-transport-https

  # Step 3: Add FRR 10.4 repository
  echo "📦 Adding FRR 10.4 repository..."
  FRR_SERIES="frr-10.4"
  OS_CODENAME="$(lsb_release -cs)"
  
  curl -fsSL https://deb.frrouting.org/frr/keys.gpg \
       -o /usr/share/keyrings/frrouting.gpg

  echo "deb [signed-by=/usr/share/keyrings/frrouting.gpg] \
https://deb.frrouting.org/frr ${OS_CODENAME} ${FRR_SERIES}" \
    | tee /etc/apt/sources.list.d/frr.list >/dev/null

  # Step 4: Set repository preferences to prioritize FRR repo
  cat > /etc/apt/preferences.d/frr-priority <<EOF
Package: *
Pin: origin deb.frrouting.org
Pin-Priority: 1000

Package: frr*
Pin: origin deb.frrouting.org
Pin-Priority: 1001

Package: libyang*
Pin: origin deb.frrouting.org
Pin-Priority: 1001
EOF

  # Step 5: Update package cache
  run apt-get update -qq

  # Step 6: Handle libyang2 dependency
  echo "🚀 Installing FRR 10.4.0..."
  
  # Check available libyang2 versions
  echo "🔍 Checking available libyang2 versions..."
  apt-cache policy libyang2 | grep -A5 "Version table"
  
  # Remove existing libyang2 packages
  echo "🧹 Removing existing libyang2 packages..."
  apt-get remove -y libyang2t64 libyang2 2>/dev/null || true
  apt-get autoremove -y 2>/dev/null || true
  
  # Check if libyang2 from FRR repository is available
  echo "📦 Checking libyang2 availability in FRR repository..."
  AVAILABLE_LIBYANG=$(apt-cache policy libyang2 | grep -A1 "deb.frrouting.org" | grep -oP '\d+\.\d+\.\d+' | head -1 || echo "")
  
  if [[ -n "$AVAILABLE_LIBYANG" ]]; then
    echo "🔍 Found libyang2 $AVAILABLE_LIBYANG in FRR repository"
    
    # Install libyang2 from FRR repository
    echo "📦 Installing libyang2 from FRR repository..."
    run apt-get install -y libyang2
    
    LIBYANG_VERSION=$(dpkg -l | grep libyang2 | awk '{print $3}' || echo "Not found")
    echo "✅ libyang2 version: $LIBYANG_VERSION"
  else
    echo "⚠️  No compatible libyang2 found in FRR repository for ARM64"
    echo "🔨 Building libyang2 from source..."
    
    # Install build dependencies for libyang2 compilation
    run apt-get install -y build-essential cmake libpcre2-dev pkg-config
    
    # Create temporary build directory
    BUILD_DIR="/tmp/libyang2-build"
    rm -rf "$BUILD_DIR"
    mkdir -p "$BUILD_DIR"
    cd "$BUILD_DIR"
    
    # Download and compile libyang2 2.1.128
    echo "📥 Downloading libyang2 2.1.128 source..."
    curl -fsSL https://github.com/CESNET/libyang/archive/v2.1.128.tar.gz -o libyang2.tar.gz
    tar -xzf libyang2.tar.gz
    cd libyang-2.1.128
    
    echo "🔨 Compiling libyang2..."
    mkdir build && cd build
    cmake -DCMAKE_INSTALL_PREFIX=/usr \
          -DCMAKE_BUILD_TYPE=Release \
          -DENABLE_TESTS=OFF \
          ..
    make -j$(nproc)
    make install
    ldconfig
    
    # Cleanup build directory
    cd /
    rm -rf "$BUILD_DIR"
    
    echo "✅ libyang2 2.1.128 compiled and installed from source"
    
    # Create a dummy APT package to satisfy FRR dependency
    echo "📦 Creating dummy APT package for libyang2..."
    run apt-get install -y equivs
    
    # Create package control file
    cat > /tmp/libyang2-dummy <<EOF
Section: libs
Priority: optional
Standards-Version: 3.9.2
Package: libyang2
Version: 2.1.128-2~ubuntu24.04u1
Maintainer: elchi-install <elchi@localhost>
Architecture: $(dpkg --print-architecture)
Provides: libyang2
Description: libyang2 compiled from source
 This is a dummy package to satisfy APT dependencies.
 The actual libyang2 2.1.128 is installed from source.
EOF
    
    # Build and install dummy package
    cd /tmp
    equivs-build libyang2-dummy
    
    # Remove any existing libyang2 packages first
    echo "🧹 Removing any existing libyang2 packages..."
    dpkg --remove --force-depends libyang2 libyang2t64 2>/dev/null || true
    
    # Find the generated .deb file and install it
    DEB_FILE=$(find /tmp -name "libyang2_*.deb" -type f | head -1)
    if [[ -n "$DEB_FILE" ]]; then
      run dpkg -i "$DEB_FILE"
      rm -f "$DEB_FILE"
    else
      # Fallback: look in current directory
      DEB_FILE=$(find . -name "libyang2_*.deb" -type f | head -1)
      if [[ -n "$DEB_FILE" ]]; then
        run dpkg -i "$DEB_FILE"
        rm -f "$DEB_FILE"
      else
        fail "Could not find generated libyang2 dummy package"
      fi
    fi
    
    rm -f /tmp/libyang2-dummy
    
    # Update APT cache after dummy package installation  
    echo "🔄 Updating APT cache..."
    apt-get update -qq 2>/dev/null || apt-get update
    
    echo "✅ Dummy APT package created for source-compiled libyang2"
  fi
  
  # Step 7: Install FRR packages with retry logic
  echo "📦 Installing FRR 10.4.0 packages..."
  
  # Try FRR installation with timeout and retry
  for attempt in 1 2 3; do
    echo "🔄 FRR installation attempt $attempt/3..."
    
    # Kill any hanging APT processes
    pkill -f "apt-get.*install.*frr" 2>/dev/null || true
    sleep 2
    
    # Clear APT locks if they exist
    rm -f /var/lib/apt/lists/lock /var/cache/apt/archives/lock /var/lib/dpkg/lock-frontend 2>/dev/null || true
    
    # Try installation with real-time progress monitoring
    echo "⏱️  Starting FRR installation with progress monitoring..."
    
    # Start APT installation with monitoring
    (
      echo "📦 Installing FRR packages (this may take several minutes on ARM64)..."
      timeout 600 apt-get install -y --no-install-recommends \
        -o Dpkg::Options::="--force-overwrite" \
        -o APT::Cache-Limit=100000000 \
        -o APT::Get::AllowUnauthenticated=false \
        frr frr-pythontools &
      
      APT_PID=$!
      echo "🔄 APT process started (PID: $APT_PID)"
      
      # Monitor progress
      COUNTER=0
      while kill -0 $APT_PID 2>/dev/null; do
        COUNTER=$((COUNTER + 1))
        
        if pgrep -f "man-db" >/dev/null 2>&1; then
          echo "📚 Processing manual pages - ARM64 slow but normal (${COUNTER}0s)"
        elif pgrep -f "dpkg.*frr" >/dev/null 2>&1; then
          echo "📦 Installing FRR packages (${COUNTER}0s)"
        else
          echo "🔄 APT installation in progress (${COUNTER}0s)"
        fi
        
        sleep 10
        
        if [ $COUNTER -gt 60 ]; then
          echo "⚠️  Installation taking longer than expected"
          if [ $COUNTER -gt 90 ]; then
            echo "❌ Forcing timeout"
            kill $APT_PID 2>/dev/null
            break
          fi
        fi
      done
      
      wait $APT_PID
    )
    
    # Check if FRR is actually installed regardless of exit code
    FRR_INSTALLED_VERSION=$(dpkg -l | grep "^ii.*frr " | awk '{print $3}' | head -1)
    if [[ -n "$FRR_INSTALLED_VERSION" ]] && command -v vtysh >/dev/null 2>&1; then
      echo "✅ FRR installation successful on attempt $attempt (version: $FRR_INSTALLED_VERSION)"
      
      # Final cleanup of any remaining processes
      echo "🧹 Cleaning up any remaining background processes..."
      pkill -f "man-db" 2>/dev/null || true
      
      echo "⏳ Allowing post-install scripts to finish..."
      sleep 5
      break
    else
      echo "⚠️  FRR installation failed on attempt $attempt (package verification failed)"
      if [[ $attempt -eq 3 ]]; then
        echo "❌ FRR installation failed after 3 attempts"
        echo "🔍 Checking system state..."
        apt-cache policy frr
        dpkg -l | grep -E "(frr|libyang)"
        fail "FRR 10.4.0 installation failed after multiple attempts"
      fi
      sleep 5
    fi
  done

  # Step 8: Verify installation
  FRR_VERSION=$(vtysh -c "show version" | grep -i "frrouting" | head -1 || echo "Unknown")
  INSTALLED_VERSION=$(dpkg -l | grep "^ii.*frr " | awk '{print $3}' | head -1)
  echo "✅ Installed: $FRR_VERSION"
  echo "📦 Package version: $INSTALLED_VERSION"
  
  # Step 8.1: Clean up FRR repository after successful installation
  echo "🧹 Cleaning up FRR repository (no longer needed)..."
  rm -f /etc/apt/sources.list.d/frr.list
  rm -f /etc/apt/preferences.d/frr-priority
  rm -f /usr/share/keyrings/frrouting.gpg

  # Step 9: Configure FRR daemons
  echo "⚙️  Configuring FRR daemons..."
  run sed -i \
      -e 's/^zebra=.*/zebra=yes/' \
      -e 's/^bgpd=.*/bgpd=yes/' \
      /etc/frr/daemons

  # Step 10: Create FRR configuration
  echo "📝 Writing FRR configuration..."
  HOSTNAME=$(hostnamectl --static)
  cat >/etc/frr/frr.conf <<EOF
service integrated-vtysh-config
auto-save running-config

hostname ${HOSTNAME}
log syslog informational

line vty
EOF
  run chmod 640 /etc/frr/frr.conf
  run chown frr:frr /etc/frr/frr.conf

  # Step 11: Start and configure the service
  echo "🚀 Starting FRR service..."
  run systemctl daemon-reload
  run systemctl enable frr
  
  # Stop service if running to ensure clean start
  systemctl stop frr 2>/dev/null || true
  sleep 2
  
  # Start with fresh configuration
  run systemctl start frr
  
  # Step 12: Verify daemons are running
  echo "🔍 Verifying FRR daemons..."
  sleep 3
  
  ZEBRA_STATUS=$(systemctl show frr -p SubState --value)
  if [[ "$ZEBRA_STATUS" == "running" ]]; then
    # Check if zebra and bgpd are actually running
    if pgrep -f "/usr/lib/frr/zebra" >/dev/null && pgrep -f "/usr/lib/frr/bgpd" >/dev/null; then
      echo "✅ FRR daemons (zebra + bgpd) are running"
    else
      echo "⚠️  FRR service running but daemons not detected, restarting..."
      run systemctl restart frr
      sleep 3
      
      if pgrep -f "/usr/lib/frr/zebra" >/dev/null && pgrep -f "/usr/lib/frr/bgpd" >/dev/null; then
        echo "✅ FRR daemons started after restart"
      else
        echo "❌ FRR daemons still not running, manual intervention may be needed"
        systemctl status frr --no-pager || true
      fi
    fi
  else
    echo "❌ FRR service failed to start"
    systemctl status frr --no-pager || true
  fi

  FINAL_VERSION=$(dpkg -l | grep "^ii.*frr " | awk '{print $3}' | head -1)
  ok "✅ FRR $FINAL_VERSION installed and configured — zebra & bgpd enabled."
}

###############################################################################
# SYSTEM UTILITIES
###############################################################################

ensure_yq_installed() {
  if ! command -v yq &>/dev/null; then
    info "🔧 'yq' not found — installing..."
    run apt install -y yq || fail "❌ Failed to install yq"
    ok "✅ 'yq' installed"
  else
    ok "✅ 'yq' already installed"
  fi
}

ensure_required_tools() {
  info "🔧 Checking required system tools"

  # Tools that might be missing in minimal installations
  REQUIRED_PACKAGES=""

  if ! command -v netstat &>/dev/null; then
    REQUIRED_PACKAGES="$REQUIRED_PACKAGES net-tools"
  fi

  if ! command -v networkctl &>/dev/null; then
    REQUIRED_PACKAGES="$REQUIRED_PACKAGES systemd-networkd"
  fi

  if ! command -v netplan &>/dev/null; then
    REQUIRED_PACKAGES="$REQUIRED_PACKAGES netplan.io"
  fi

  if ! command -v ip &>/dev/null; then
    REQUIRED_PACKAGES="$REQUIRED_PACKAGES iproute2"
  fi

  if ! command -v cron &>/dev/null; then
    REQUIRED_PACKAGES="$REQUIRED_PACKAGES cron"
  fi

  if ! command -v logrotate &>/dev/null; then
    REQUIRED_PACKAGES="$REQUIRED_PACKAGES logrotate"
  fi

  if ! command -v filebeat &>/dev/null; then
    REQUIRED_PACKAGES="$REQUIRED_PACKAGES filebeat"
  fi

  if ! command -v rsyslogd &>/dev/null; then
    REQUIRED_PACKAGES="$REQUIRED_PACKAGES rsyslog"
  fi

  if [[ -n "$REQUIRED_PACKAGES" ]]; then
    info "📦 Installing missing tools:$REQUIRED_PACKAGES"
    run apt update -qq
    run apt install -y $REQUIRED_PACKAGES
    ok "✅ Required tools installed"
  else
    ok "✅ All required tools already available"
  fi
}

###############################################################################
# LOGGING CONFIGURATION
###############################################################################

setup_logging_infrastructure() {
  info "📁 Setting up logging infrastructure"

  # Create log directory
  info "creating $ELCHI_LOG_DIR directory"
  run mkdir -p "$ELCHI_LOG_DIR"
  run chown -R root:adm "$ELCHI_LOG_DIR"
  ok "✅ Log directory created"

  # Create logrotate configuration
  info "🛠️  Creating logrotate configuration for elchi"
  cat >"$LOGROTATE_CONFIG" <<'EOF'
/var/log/elchi/*.log {
    size 200M
    rotate 5
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    sharedscripts
    postrotate
        pidof envoy | xargs -r kill -SIGUSR1 2>/dev/null || true
    endscript
}
EOF
  chmod 644 "$LOGROTATE_CONFIG"
  ok "✅ Logrotate config created"

  # Create logrotate script
  info "📜 Creating logrotate-5min.sh script"
  cat >"$LOGROTATE_SCRIPT" <<'EOF'
#!/bin/bash
/usr/sbin/logrotate /etc/logrotate.d/elchi
EOF
  run chmod +x "$LOGROTATE_SCRIPT"
  ok "✅ Logrotate script created"

  # Create cron job for 5-minute logrotate
  info "🕔 Creating 5-minute cron job for logrotate"
  cat >"$LOGROTATE_CRON" <<'EOF'
*/5 * * * * root /usr/local/bin/logrotate-5min.sh
EOF
  chmod 644 "$LOGROTATE_CRON"
  ok "✅ Logrotate cron job configured"

  # Enable and start cron service
  info "🔄 Enabling cron service"
  run systemctl enable --now cron

  # Test logrotate configuration
  info "🧪 Testing logrotate configuration (dry run)"
  if logrotate -dv /etc/logrotate.conf &>/dev/null; then
    ok "✅ Logrotate config is valid"
  else
    warn "⚠️  Logrotate dry run had warnings (non-critical)"
  fi
}

setup_filebeat_configuration() {
  info "📝 Configuring Filebeat"

  # Check if Filebeat config already exists
  if [[ -f "$FILEBEAT_CONFIG" ]]; then
    # Backup existing configuration
    local backup_file="${FILEBEAT_CONFIG}.backup-$(date +%Y%m%d-%H%M%S)"
    info "⚠️  Existing Filebeat config found, creating backup"
    run cp "$FILEBEAT_CONFIG" "$backup_file"
    ok "✅ Backup created: $(basename $backup_file)"
  fi

  # Create new Filebeat configuration
  info "📄 Writing Filebeat configuration"
  cat >"$FILEBEAT_CONFIG" <<'EOF'
filebeat.inputs:
  - type: filestream
    enabled: false
    id: elchi-logs
    paths:
      - /var/log/elchi/*.log

processors:
  - timestamp:
      field: message
      layouts:
        - '[2006-01-02 15:04:05.000]'
      test:
        - '[2025-10-26 11:11:05.343]'
  - drop_fields:
      fields: ["agent", "input", "ecs", "log.offset", "log.file.inode", "log.file.device_id", "tags"]

output.logstash:
  hosts: ["hostorip:5044"]
  loadbalance: false
EOF

  chmod 644 "$FILEBEAT_CONFIG"
  ok "✅ Filebeat configuration created"

  # Restart Filebeat to apply changes
  info "🔃 Restarting Filebeat to apply changes"
  run systemctl daemon-reload
  run systemctl restart filebeat
  ok "✅ Filebeat restarted successfully"
}

###############################################################################
# RSYSLOG CONFIGURATION
###############################################################################

setup_rsyslog_configuration() {
  info "📝 Configuring Rsyslog"

  # Create rsyslog configuration for elchi logs
  info "📄 Creating rsyslog configuration: /etc/rsyslog.d/50-elchi.conf"
  cat > /etc/rsyslog.d/50-elchi.conf <<'EOF'
module(load="imfile")

template(name="WithFilenamePrefix" type="list") {
  property(name="$!metadata!filename" field.extract="basename")
  constant(value=" ")
  property(name="msg")
  constant(value="\n")
}

input(type="imfile"
      File="/var/log/elchi/*_access.log"
      Tag="elchi-access"
      Severity="info"
      Facility="local7"
      addMetadata="on")

input(type="imfile"
      File="/var/log/elchi/*_system.log"
      Tag="elchi-system"
      Severity="info"
      Facility="local7"
      addMetadata="on")

#action(
#  type="omfwd"
#  target="syslog.example.com"
#  port="514"
#  protocol="udp"
#  template="WithFilenamePrefix"
#  action.resumeRetryCount="2"
#  queue.type="linkedList"
#  queue.size="10000"
#)
EOF

  run chmod 644 /etc/rsyslog.d/50-elchi.conf
  ok "✅ Rsyslog configuration created"

  # Enable rsyslog but keep it stopped (will be managed via API)
  info "🔧 Enabling rsyslog service (keeping stopped for API management)"
  run systemctl daemon-reload
  run systemctl enable rsyslog
  run systemctl enable syslog.socket
  run systemctl stop rsyslog 2>/dev/null || true
  run systemctl stop syslog.socket 2>/dev/null || true
  ok "✅ Rsyslog enabled but stopped (managed via API)"
}

###############################################################################
# NETPLAN CONFIGURATION
###############################################################################

rename_default_netplan_to_elchi() {
  info "🔄 Renaming Ubuntu default netplan to elchi-managed format"
  
  local netplan_dir="/etc/netplan"
  local elchi_config="${netplan_dir}/99-elchi-interfaces.yaml"
  
  # Check if elchi config already exists
  if [[ -f "$elchi_config" ]]; then
    info "✅ Elchi netplan config already exists: $elchi_config"
    return 0
  fi
  
  # Find the default Ubuntu netplan file
  local default_config=""
  for config_file in "${netplan_dir}"/*.yaml "${netplan_dir}"/*.yml; do
    # Skip if file doesn't exist (glob didn't match)
    [[ -f "$config_file" ]] || continue
    
    # Skip elchi-managed files
    local basename=$(basename "$config_file")
    if [[ "$basename" =~ ^99-elchi- ]]; then
      continue
    fi
    
    # Found a default config file
    default_config="$config_file"
    info "📄 Found default netplan config: $basename"
    break
  done
  
  # If no default config found, create minimal elchi config
  if [[ -z "$default_config" ]]; then
    info "📝 No default netplan config found, creating minimal elchi config"
    cat > "$elchi_config" <<'EOF'
network:
  version: 2
  renderer: networkd
  ethernets: {}
EOF
    chmod 600 "$elchi_config"
    ok "✅ Created minimal elchi netplan config"
    return 0
  fi
  
  # Rename the default config to elchi format
  info "🔄 Renaming $(basename "$default_config") to 99-elchi-interfaces.yaml"
  if mv "$default_config" "$elchi_config"; then
    # Set proper permissions
    chmod 600 "$elchi_config"
    ok "✅ Successfully renamed to elchi-managed format"
    info "🔧 Network is now managed by elchi-client"
  else
    fail "❌ Failed to rename netplan config"
  fi
}

# split_netplan_physical_interfaces() function removed
# Network management is now handled by the controller using unified YAML approach


###############################################################################
# ELCHI-SHIELD (Envoy ext_proc API-security / WAF sidecar)
###############################################################################
# Installed next to the client on the same host, in the SAME install run. The
# client writes shield's policy files into the watched conf.d; shield inspects
# Envoy traffic over a /run UDS and exposes health/metrics on loopback only.
# Shares the elchi user/group + /etc/elchi/bin already created above (so Envoy,
# already added to the elchi group, can reach the UDS). Audit/metrics are off
# unless a DSN/endpoint is given. Binary download failure is non-fatal.
install_configure_shield() {
  local shield_dir="$ELCHI_DIR/elchi-shield"
  local conf_dir="$shield_dir/conf.d"
  local files_dir="$shield_dir/files"
  local run_dir_name="elchi-shield"
  local socket_path="/run/$run_dir_name/extproc.sock"
  local http_addr="127.0.0.1:9001"
  local shield_service="/etc/systemd/system/elchi-shield.service"
  local shield_binary="$ELCHI_BIN_DIR/elchi-shield"
  local repo="CloudNativeWorks/elchi-shield"

  info "🛡️  Installing elchi-shield (Envoy ext_proc API-security sidecar)"

  # Directory tree (elchi-client populates conf.d; shield only reads it).
  run mkdir -p "$conf_dir" "$files_dir"
  run chown -R "$ELCHI_USER:$ELCHI_USER" "$shield_dir"
  run chmod -R u=rwX,g=rX,o= "$shield_dir"
  if [[ ! -e "$conf_dir/README" ]]; then
    cat >"$conf_dir/README" <<'RD'
# elchi-shield watches this directory for *.yaml / *.json policy files.
# Managed by elchi-client (pushed from the Elchi control plane).
# Empty directory = no policy → the configured default posture applies.
RD
    chown "$ELCHI_USER:$ELCHI_USER" "$conf_dir/README"
  fi

  resolve_mirror_base_url
  local suffix
  case "$(dpkg --print-architecture 2>/dev/null || uname -m)" in
    amd64|x86_64) suffix="linux-amd64" ;;
    arm64|aarch64) suffix="linux-arm64" ;;
    *) warn "⚠️  unsupported arch — defaulting to amd64"; suffix="linux-amd64" ;;
  esac

  # Resolve where to fetch shield from. Prefer the PUBLIC elchi-archive mirror
  # (the shield binary is bundled into the same release as this installer); only
  # fall back to the private source repo on a raw, un-published run from source.
  local url=""
  if [[ -n "$MIRROR_BASE_URL" ]]; then
    url="$MIRROR_BASE_URL/elchi-shield-${suffix}"
    info "📦 elchi-shield from mirror${SHIELD_VERSION:+ ($SHIELD_VERSION)}"
  else
    local tag="$SHIELD_VERSION"
    if [[ -z "$tag" ]]; then
      tag=$(curl -s "https://api.github.com/repos/$repo/releases/latest" \
              | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' || echo "")
    fi
    if [[ -n "$tag" ]]; then
      url="https://github.com/$repo/releases/download/$tag/elchi-shield-${suffix}"
      info "📦 elchi-shield release (source): $tag"
    fi
  fi

  if [[ -z "$url" ]]; then
    warn "⚠️  could not resolve an elchi-shield download — place the binary at $shield_binary manually"
  else
    local tmp="/tmp/elchi-shield-download"
    info "🔗 downloading $url"
    if curl -fsSL --retry 3 --retry-delay 2 --max-time 120 "$url" -o "$tmp"; then
      local sum_url="${url}.sha256" tmpsum="/tmp/elchi-shield.sha256"
      if curl -fsSL --retry 3 --retry-delay 2 --max-time 30 "$sum_url" -o "$tmpsum"; then
        local exp act
        exp=$(awk '{print $1}' "$tmpsum")
        if command -v sha256sum >/dev/null 2>&1; then act=$(sha256sum "$tmp" | awk '{print $1}'); else act=$(shasum -a 256 "$tmp" | awk '{print $1}'); fi
        [[ "$exp" == "$act" ]] || fail "❌ elchi-shield checksum mismatch (expected $exp, got $act)"
        ok "✅ elchi-shield checksum verified"
        rm -f "$tmpsum"
      else
        warn "⚠️  no elchi-shield checksum published — skipping verification"
      fi
      install -m 0755 -o root -g "$ELCHI_USER" "$tmp" "$shield_binary"
      rm -f "$tmp"
      ok "✅ elchi-shield binary installed"
    else
      warn "⚠️  elchi-shield binary download failed — place it at $shield_binary manually"
      rm -f "$tmp"
    fi
  fi

  # Sink config (ClickHouse audit DSN + OTLP metrics endpoint) lives in ONE
  # editable file — shield's --config-file. The operator edits THIS after install
  # (CH address, OTel address) and restarts the service; policies are separate
  # (conf.d/*.yaml). It may carry the ClickHouse password → 0600 (shield warns
  # otherwise). Preserve it on re-run so manual edits are never clobbered.
  local shield_cfg="$shield_dir/config.yaml"
  if [[ -f "$shield_cfg" ]]; then
    # Preserve operator edits, but always re-assert ownership + 0600 — the file
    # may hold the ClickHouse DSN/password and shield warns if it's group-readable.
    chown "$ELCHI_USER:$ELCHI_USER" "$shield_cfg" 2>/dev/null || true
    chmod 0600 "$shield_cfg" 2>/dev/null || true
    ok "✅ shield sink config exists — preserving ($shield_cfg)"
  else
    # Migrate a DSN from the legacy audit.env (installs predating config.yaml) so
    # a re-run without --shield-audit-dsn doesn't silently drop it.
    if [[ -z "$SHIELD_AUDIT_DSN" && -f "$shield_dir/audit.env" ]]; then
      SHIELD_AUDIT_DSN=$(sed -nE 's/^ELCHI_SHIELD_AUDIT_CLICKHOUSE_DSN=(.*)$/\1/p' "$shield_dir/audit.env" | head -n1)
      [[ -n "$SHIELD_AUDIT_DSN" ]] && info "↪ migrating shield audit DSN from legacy audit.env"
    fi
    local _exporter="none"; [[ -n "$SHIELD_AUDIT_DSN" ]] && _exporter="clickhouse"
    local _otlp_insecure="false"; [[ -n "$SHIELD_METRICS_INSECURE" ]] && _otlp_insecure="true"
    info "📝 writing shield sink config $shield_cfg"
    ( umask 077; cat >"$shield_cfg" <<EOF
# elchi-shield SINK config — audit (ClickHouse) + metrics (OTLP).
# Edit, then:  systemctl restart elchi-shield
# Holds the ClickHouse DSN (may contain a password) — keep this file chmod 0600.
# NOTE: this is the PROCESS/sink config. Security POLICIES live in conf.d/*.yaml.
audit:
  # exporter: none | clickhouse | otel  (auto = clickhouse when a dsn is set)
  exporter: ${_exporter}
  # ClickHouse audit sink. e.g. clickhouse://user:pass@CH-HOST:9000/elchi
  clickhouse_dsn: "${SHIELD_AUDIT_DSN}"
  clickhouse_ttl_days: 7
metrics:
  # Push metrics to an OTel Collector (OTLP/gRPC host:port). Empty = /metrics scrape only.
  otlp_endpoint: "${SHIELD_METRICS_OTLP}"
  otlp_insecure: ${_otlp_insecure}
EOF
    )
    chown "$ELCHI_USER:$ELCHI_USER" "$shield_cfg"
    chmod 0600 "$shield_cfg"
  fi
  rm -f "$shield_dir/audit.env"   # legacy: superseded by config.yaml

  # Hardened systemd unit (UDS + loopback only; never exposed off-box).
  info "writing $shield_service"
  cat >"$shield_service" <<EOF
[Unit]
Description=Elchi Shield — Envoy ext_proc API security / WAF sidecar
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$ELCHI_USER
Group=$ELCHI_USER
RuntimeDirectory=$run_dir_name
RuntimeDirectoryMode=2750
ExecStartPre=-$shield_binary validate $conf_dir
ExecStart=$shield_binary \\
  --config-dir $conf_dir \\
  --config-file $shield_cfg \\
  --extproc-network unix \\
  --extproc-addr $socket_path \\
  --http-addr $http_addr \\
  --log-format json \\
  --log-level info

Restart=always
RestartSec=5

NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
ReadWritePaths=$shield_dir $ELCHI_LOG_DIR
UMask=0007
LimitNOFILE=262144

StandardOutput=journal
StandardError=journal
SyslogIdentifier=elchi-shield

[Install]
WantedBy=multi-user.target
EOF
  chmod 644 "$shield_service"
  run systemctl daemon-reload
  if [[ -f "$shield_binary" ]]; then
    run systemctl enable elchi-shield.service
    # restart (not enable --now) so a re-run picks up a freshly downloaded binary.
    run systemctl restart elchi-shield.service
  else
    run systemctl enable elchi-shield.service
    warn "⚠️  shield binary missing — service enabled but not started"
  fi
  ok "✅ elchi-shield installed"
}

###############################################################################
# MAIN EXECUTION FLOW
###############################################################################

# Set up signal handlers and check root privileges
trap 'cleanup_on_exit' INT TERM
trap 'fail "line $LINENO (exit code $?)"' ERR
[[ $EUID -eq 0 ]] || fail "Run this script as root (sudo …)"

info "=== Elchi Client Installer v3 ==="

# Load kernel modules and configure system settings
info "loading nf_conntrack (if needed)"
run modprobe nf_conntrack || true

info "writing tuning files"
cat >"$SYSCTL_FILE"<<'EOF'
# --- Elchi performance tuning for Load Balancer + Envoy Proxies ---

# Connection tracking - increased for multiple Envoy instances
net.netfilter.nf_conntrack_max = 2097152

# TCP optimizations for load balancer
net.ipv4.tcp_window_scaling    = 1
net.ipv4.tcp_syncookies        = 1
net.ipv4.tcp_tw_reuse          = 1
net.ipv4.tcp_fin_timeout        = 30
net.ipv4.conf.all.rp_filter     = 2
net.ipv4.conf.default.rp_filter = 2

# TCP keepalive optimized for proxy workload
net.ipv4.tcp_keepalive_time    = 120
net.ipv4.tcp_keepalive_intvl   = 30
net.ipv4.tcp_keepalive_probes  = 3

# Buffer sizes for high throughput
net.core.rmem_max              = 16777216
net.core.wmem_max              = 16777216
net.ipv4.tcp_rmem              = 4096 87380 16777216
net.ipv4.tcp_wmem              = 4096 65536 16777216

# Port range for multiple Envoy instances
net.ipv4.ip_local_port_range   = 1024 65535

# Queue settings for high connection volume
net.ipv4.tcp_max_syn_backlog   = 32768
net.core.somaxconn             = 32768
net.core.netdev_max_backlog    = 50000

# File descriptor limits (realistic for production)
fs.file-max                    = 4097152
fs.nr_open                     = 2048576

# Inotify settings for config file watching
fs.inotify.max_queued_events   = 16384
fs.inotify.max_user_instances  = 8192
fs.inotify.max_user_watches    = 262144
user.max_inotify_instances     = 8192
user.max_inotify_watches       = 262144

# Enable IP forwarding for routing
net.ipv4.ip_forward            = 1

# Additional TCP optimizations for proxy workload
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_max_tw_buckets    = 1440000
net.ipv4.tcp_timestamps        = 1
net.ipv4.tcp_sack              = 1
net.ipv4.tcp_fack              = 1

# Network interface optimizations
net.core.rmem_default          = 262144
net.core.wmem_default          = 262144
net.core.optmem_max            = 40960

# Connection tracking memory management
net.netfilter.nf_conntrack_tcp_timeout_established = 7200
net.netfilter.nf_conntrack_tcp_timeout_time_wait = 120
EOF

cat >"$LIMITS_FILE"<<'EOF'
# File descriptor limits for Envoy proxies and elchi processes
* soft nofile 655360
* hard nofile 655360

# Process limits for multiple Envoy instances
* soft nproc 32768
* hard nproc 32768

# Memory lock limits for high-performance networking
* soft memlock unlimited
* hard memlock unlimited
EOF

cat >"$MODPROBE_FILE"<<'EOF'
# Connection tracking hash table optimization for load balancer
options nf_conntrack expect_hashsize=524288 hashsize=524288
EOF

chmod 644 "$SYSCTL_FILE" "$LIMITS_FILE" "$MODPROBE_FILE"
run sysctl -p "$SYSCTL_FILE"

# Create system users
info "creating system users"
id "$ELCHI_USER"  &>/dev/null && ok "$ELCHI_USER exists"  || run useradd --system --no-create-home --shell /usr/sbin/nologin "$ELCHI_USER"
id "$ENVOY_USER"  &>/dev/null && ok "$ENVOY_USER exists" || run useradd --system --no-create-home --shell /usr/sbin/nologin "$ENVOY_USER"
run usermod -aG "$ELCHI_USER" "$ENVOY_USER"
run usermod -aG adm "$ENVOY_USER"

# Configure sudoers
info "configuring sudoers rule"

# Always update sudoers file to ensure latest configuration
info "📝 Updating sudoers file with latest configuration"
cat >"$SUDO_FILE"<<'EOF'
Cmnd_Alias ELCHI_CMDS = \
 /usr/bin/systemctl daemon-reload, \
 /usr/bin/systemctl start *.service, \
 /usr/bin/systemctl stop *.service, \
 /usr/bin/systemctl restart *.service, \
 /usr/bin/systemctl reload *.service, \
 /usr/bin/systemctl enable --now *.service, \
 /usr/bin/systemctl enable *.service, \
 /usr/bin/systemctl disable *.service, \
 /usr/bin/systemctl status *.service, \
 /usr/bin/systemctl list-units --all *.service, \
 /usr/bin/systemctl list-units *.service, \
 /usr/bin/systemctl show *.service, \
 /usr/bin/systemctl show -p * *.service, \
 /usr/bin/systemctl is-active *.service, \
 /usr/bin/systemctl is-enabled *.service, \
 /usr/bin/systemctl restart systemd-journald, \
 /usr/bin/tee /etc/systemd/journald@elchi-*.conf, \
 /usr/bin/tee /etc/systemd/system/elchi-*.service, \
 /usr/bin/tee /etc/netplan/99-elchi-*.yaml, \
 /usr/bin/tee /etc/netplan/99-elchi-*.yaml.backup, \
 /usr/bin/tee /etc/netplan/90-*.yaml, \
 /usr/bin/chmod 0600 /etc/netplan/99-elchi-*.yaml, \
 /usr/bin/chmod 0600 /etc/netplan/99-elchi-*.yaml.backup, \
 /usr/bin/chmod 0600 /etc/netplan/90-*.yaml, \
 /usr/bin/netplan generate, \
 /usr/bin/netplan apply, \
 /usr/bin/netplan try, \
 /usr/bin/netplan try --timeout *, \
 /usr/sbin/netplan generate, \
 /usr/sbin/netplan apply, \
 /usr/sbin/netplan try, \
 /usr/sbin/netplan try --timeout *, \
 /usr/bin/networkctl reload

Cmnd_Alias FRR_CMDS = \
 /usr/bin/vtysh, \
 /usr/bin/vtysh -c *, \
 /usr/bin/vtysh -d *, \
 /usr/bin/vtysh -f *, \
 /usr/bin/systemctl start frr, \
 /usr/bin/systemctl stop frr, \
 /usr/bin/systemctl restart frr, \
 /usr/bin/systemctl reload frr, \
 /usr/bin/systemctl status frr, \
 /usr/bin/systemctl enable frr, \
 /usr/bin/systemctl disable frr

Cmnd_Alias FILEBEAT_CMDS = \
 /usr/bin/tee /etc/filebeat/filebeat.yml, \
 /usr/bin/chmod 644 /etc/filebeat/filebeat.yml, \
 /usr/bin/tee /etc/filebeat/.filebeat.yml.elchi-tmp, \
 /usr/bin/chmod 644 /etc/filebeat/.filebeat.yml.elchi-tmp, \
 /usr/bin/filebeat test config -c /etc/filebeat/.filebeat.yml.elchi-tmp, \
 /usr/bin/mv -f /etc/filebeat/.filebeat.yml.elchi-tmp /etc/filebeat/filebeat.yml, \
 /usr/bin/rm -f /etc/filebeat/.filebeat.yml.elchi-tmp, \
 /usr/bin/systemctl start filebeat, \
 /usr/bin/systemctl stop filebeat, \
 /usr/bin/systemctl restart filebeat, \
 /usr/bin/systemctl reload filebeat, \
 /usr/bin/systemctl status filebeat, \
 /usr/bin/systemctl enable filebeat, \
 /usr/bin/systemctl disable filebeat, \
 /usr/bin/systemctl is-active filebeat

Cmnd_Alias RSYSLOG_CMDS = \
 /usr/bin/tee /etc/rsyslog.d/50-elchi.conf, \
 /usr/bin/chmod 644 /etc/rsyslog.d/50-elchi.conf, \
 /usr/bin/tee /etc/rsyslog.d/.50-elchi.conf.elchi-tmp, \
 /usr/bin/chmod 644 /etc/rsyslog.d/.50-elchi.conf.elchi-tmp, \
 /usr/sbin/rsyslogd -N1 -f /etc/rsyslog.d/.50-elchi.conf.elchi-tmp, \
 /usr/bin/mv -f /etc/rsyslog.d/.50-elchi.conf.elchi-tmp /etc/rsyslog.d/50-elchi.conf, \
 /usr/bin/rm -f /etc/rsyslog.d/.50-elchi.conf.elchi-tmp, \
 /usr/bin/systemctl start rsyslog, \
 /usr/bin/systemctl stop rsyslog, \
 /usr/bin/systemctl restart rsyslog, \
 /usr/bin/systemctl reload rsyslog, \
 /usr/bin/systemctl status rsyslog, \
 /usr/bin/systemctl enable rsyslog, \
 /usr/bin/systemctl disable rsyslog, \
 /usr/bin/systemctl is-active rsyslog, \
 /usr/bin/systemctl start syslog.socket, \
 /usr/bin/systemctl stop syslog.socket, \
 /usr/bin/systemctl restart syslog.socket, \
 /usr/bin/systemctl status syslog.socket, \
 /usr/bin/systemctl enable syslog.socket, \
 /usr/bin/systemctl disable syslog.socket, \
 /usr/bin/systemctl is-active syslog.socket

Cmnd_Alias LOGROTATE_CMDS = \
 /usr/bin/tee /etc/logrotate.d/elchi, \
 /usr/bin/chmod 644 /etc/logrotate.d/elchi, \
 /usr/bin/tee /usr/local/bin/logrotate-5min.sh, \
 /usr/bin/chmod 755 /usr/local/bin/logrotate-5min.sh, \
 /usr/bin/tee /etc/cron.d/logrotate-5min, \
 /usr/bin/chmod 644 /etc/cron.d/logrotate-5min

elchi ALL=(ALL) NOPASSWD: ELCHI_CMDS, FRR_CMDS, FILEBEAT_CMDS, RSYSLOG_CMDS, LOGROTATE_CMDS
Defaults:elchi !pam_session

EOF
chmod 440 "$SUDO_FILE"
run visudo -cf "$SUDO_FILE"
info "✅ Sudoers configuration updated"

# Create directory structure
info "/etc/elchi tree"
run mkdir -p "$ELCHI_BIN_DIR"
run chown -R root:"$ELCHI_USER" "$ELCHI_DIR"
run chmod 750 "$ELCHI_DIR" "$ELCHI_BIN_DIR"

# Create config.yaml with user-provided configuration
info "📝 Creating config.yaml with provided configuration"

# Set BGP capability based on --enable-bgp flag
if [[ "$ENABLE_FRR" == true ]]; then
  CLIENT_BGP="true"
else
  CLIENT_BGP="false"
fi

info "🏷️  Client name: $CLIENT_NAME"
info "🌐 Server: $SERVER_HOST:$SERVER_PORT (TLS: $SERVER_TLS)"
info "🔑 Token: ${SERVER_TOKEN:0:8}..."
info "🚏 BGP capability: $CLIENT_BGP (from --enable-bgp flag)"
info "☁️  Cloud: $CLIENT_CLOUD"

# Create config.yaml from template (only if it doesn't exist)
if [[ -f "$ELCHI_CONFIG" ]]; then
  ok "✅ Config file already exists, skipping: $ELCHI_CONFIG"
else
  info "📝 Creating new config file: $ELCHI_CONFIG"
  cat > "$ELCHI_CONFIG" <<EOF
server:
  host: "$SERVER_HOST"
  port: $SERVER_PORT
  tls: $SERVER_TLS
  insecure_skip_verify: true
  token: "$SERVER_TOKEN"
  timeout: "30s"

client:
  name: "$CLIENT_NAME"
  bgp: $CLIENT_BGP
  cloud: "$CLIENT_CLOUD"

logging:
  level: "info"
  format: "json"
EOF

  run chown root:"$ELCHI_USER" "$ELCHI_CONFIG"
  run chmod 640 "$ELCHI_CONFIG"
  ok "✅ Config file created"
fi
ok "✅ config.yaml created successfully"

# Download elchi-client binary from latest GitHub release
info "📥 Downloading elchi-client binary from latest GitHub release"
resolve_mirror_base_url
GITHUB_REPO="CloudNativeWorks/elchi-client"
if [[ -n "$MIRROR_BASE_URL" ]]; then
  # Mirror path: the client binary lives in the same elchi-archive release as
  # this installer, so no private source-repo API call is needed.
  LATEST_TAG="(mirror)"
else
  LATEST_RELEASE_URL="https://api.github.com/repos/$GITHUB_REPO/releases/latest"
  # Get latest release tag
  LATEST_TAG=$(curl -s "$LATEST_RELEASE_URL" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' || echo "")
fi

if [[ -n "$LATEST_TAG" ]]; then
  if [[ -n "$MIRROR_BASE_URL" ]]; then
    info "📦 elchi-client from mirror"
  else
    info "📦 Latest release: $LATEST_TAG"
  fi

  # Download elchi-client binary (architecture-specific)
  
  # Detect system architecture
  SYSTEM_ARCH=$(dpkg --print-architecture 2>/dev/null || uname -m)
  case "$SYSTEM_ARCH" in
    "amd64"|"x86_64")
      BINARY_SUFFIX="linux-amd64"
      info "🏗️  Detected architecture: AMD64"
      ;;
    "arm64"|"aarch64")
      BINARY_SUFFIX="linux-arm64"
      info "🏗️  Detected architecture: ARM64"
      ;;
    *)
      warn "⚠️  Unsupported architecture: $SYSTEM_ARCH, defaulting to AMD64"
      BINARY_SUFFIX="linux-amd64"
      ;;
  esac
  
  if [[ -n "$MIRROR_BASE_URL" ]]; then
    CLIENT_BASE_URL="$MIRROR_BASE_URL"
  else
    CLIENT_BASE_URL="https://github.com/$GITHUB_REPO/releases/download/$LATEST_TAG"
  fi
  BINARY_DOWNLOAD_URL="$CLIENT_BASE_URL/elchi-client-${BINARY_SUFFIX}"
  BINARY_PATH="$ELCHI_BIN_DIR/elchi-client"
  TEMP_BINARY="/tmp/elchi-client-download"
  
  info "🔗 Downloading binary from: $BINARY_DOWNLOAD_URL"
  
  # Download to temp location first
  if curl -fsSL --retry 3 --retry-delay 2 --max-time 60 "$BINARY_DOWNLOAD_URL" -o "$TEMP_BINARY"; then
    info "✅ Binary downloaded to temp location"
    
    # Download and verify checksum
    CHECKSUM_URL="$CLIENT_BASE_URL/elchi-client-${BINARY_SUFFIX}.sha256"
    TEMP_CHECKSUM="/tmp/elchi-client.sha256"
    
    info "🔐 Downloading checksum file for verification"
    if curl -fsSL --retry 3 --retry-delay 2 --max-time 30 "$CHECKSUM_URL" -o "$TEMP_CHECKSUM"; then
      # Extract expected checksum
      EXPECTED_CHECKSUM=$(awk '{print $1}' "$TEMP_CHECKSUM")
      
      # Calculate actual checksum
      if command -v sha256sum >/dev/null 2>&1; then
        ACTUAL_CHECKSUM=$(sha256sum "$TEMP_BINARY" | awk '{print $1}')
      elif command -v shasum >/dev/null 2>&1; then
        ACTUAL_CHECKSUM=$(shasum -a 256 "$TEMP_BINARY" | awk '{print $1}')
      else
        warn "⚠️  No checksum tool available, skipping verification"
        ACTUAL_CHECKSUM="$EXPECTED_CHECKSUM"  # Skip verification
      fi
      
      if [[ "$EXPECTED_CHECKSUM" == "$ACTUAL_CHECKSUM" ]]; then
        ok "✅ Binary checksum verification passed"
      else
        fail "❌ Binary checksum verification failed! Expected: $EXPECTED_CHECKSUM, Got: $ACTUAL_CHECKSUM"
      fi
      
      rm -f "$TEMP_CHECKSUM"
    else
      warn "⚠️  Failed to download checksum file, skipping verification"
    fi
    
    # Move to final location with proper permissions
    if mv "$TEMP_BINARY" "$BINARY_PATH"; then
      run chmod 755 "$BINARY_PATH"
      run chown root:"$ELCHI_USER" "$BINARY_PATH"
      ok "✅ elchi-client binary installed successfully"
    else
      warn "⚠️  Failed to move binary to $BINARY_PATH"
      rm -f "$TEMP_BINARY"
    fi
  else
    warn "⚠️  Failed to download elchi-client binary from GitHub release"
    warn "   You will need to manually place the binary at: $BINARY_PATH"
    rm -f "$TEMP_BINARY"
  fi
else
  warn "⚠️  Could not detect latest release, creating default config.yaml"
  touch "$ELCHI_CONFIG"
  chown root:"$ELCHI_USER" "$ELCHI_CONFIG"
  chmod 640 "$ELCHI_CONFIG"
fi

info "/var/lib/elchi tree"
for d in "${ELCHI_VAR_DIRS[@]}"; do run mkdir -p "$ELCHI_VAR_LIB/$d"; done
run chown -R "$ELCHI_USER:$ELCHI_USER" "$ELCHI_VAR_LIB"
run chmod -R g+rX,o-rwx "$ELCHI_VAR_LIB"
# Ensure kernel can access routing tables directory for symlink
run chmod o+x "$ELCHI_VAR_LIB"

# Setup routing tables symlink
info "configuring routing tables symlink"
ELCHI_RT_TABLES="$ELCHI_VAR_LIB/rt_tables.conf"
KERNEL_RT_TABLES="/etc/iproute2/rt_tables.d/elchi.conf"

# Create empty rt_tables.conf if it doesn't exist
if [[ ! -f "$ELCHI_RT_TABLES" ]]; then
    cat >"$ELCHI_RT_TABLES" <<'EOF'
# Elchi-managed routing tables
# Generated automatically - do not edit manually

EOF
    run chown "$ELCHI_USER:$ELCHI_USER" "$ELCHI_RT_TABLES"
    run chmod 644 "$ELCHI_RT_TABLES"
else
    # Ensure existing file is readable by kernel
    run chmod 644 "$ELCHI_RT_TABLES"
fi

# Remove existing kernel file and create symlink
if [[ -f "$KERNEL_RT_TABLES" ]] && [[ ! -L "$KERNEL_RT_TABLES" ]]; then
    ok "backing up existing $KERNEL_RT_TABLES"
    run mv "$KERNEL_RT_TABLES" "$KERNEL_RT_TABLES.backup"
fi

run rm -f "$KERNEL_RT_TABLES"
run ln -sf "$ELCHI_RT_TABLES" "$KERNEL_RT_TABLES"
ok "routing tables symlink: $KERNEL_RT_TABLES -> $ELCHI_RT_TABLES"

# Create systemd service
info "writing systemd unit"
cat >"$SERVICE_FILE"<<EOF
[Unit]
Description=Elchi Client Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$ELCHI_USER
Group=$ELCHI_USER
ExecStart=$ELCHI_BIN_DIR/elchi-client start --config $ELCHI_CONFIG

Restart=always
RestartSec=15

ReadWritePaths=/etc/netplan /etc/elchi /var/lib/elchi /usr/lib/systemd/system /etc/systemd /etc/filebeat /etc/rsyslog.d
ProtectSystem=full
AmbientCapabilities=CAP_DAC_OVERRIDE CAP_FOWNER CAP_NET_BIND_SERVICE \
                   CAP_NET_ADMIN CAP_SETUID CAP_SETGID CAP_AUDIT_WRITE \
                   CAP_SYS_RESOURCE
CapabilityBoundingSet=CAP_DAC_OVERRIDE CAP_FOWNER CAP_NET_BIND_SERVICE \
                      CAP_NET_ADMIN CAP_SETUID CAP_SETGID CAP_AUDIT_WRITE \
                      CAP_SYS_RESOURCE
NoNewPrivileges=false

StandardOutput=journal
StandardError=journal
SyslogIdentifier=elchi-client

[Install]
WantedBy=multi-user.target
EOF

chmod 644 "$SERVICE_FILE"
run systemctl daemon-reload
run systemctl enable elchi-client.service
# restart (not enable --now) so a re-run picks up a freshly downloaded binary.
run systemctl restart elchi-client.service

# Configure system components
setup_sources_list
setup_elastic_repository
ensure_yq_installed
ensure_required_tools

# Setup logging infrastructure
setup_logging_infrastructure
setup_filebeat_configuration
setup_rsyslog_configuration

# Convert default Ubuntu netplan to elchi-managed format
rename_default_netplan_to_elchi

# NOTE: Network management is now handled by elchi-client controller:
# - Routing tables are dynamically managed via gRPC commands
# - Netplan configurations use unified YAML approach (99-elchi-interfaces.yaml)
# - Per-interface files are no longer used - controller manages everything

# Optional: FRR installation
if [[ "$ENABLE_FRR" == true ]]; then
  info "🐟 FRR installation enabled"
  install_configure_frr
else
  info "⏭️  FRR installation skipped (use --enable-bgp to enable)"
fi

# elchi-shield sidecar (installed in the same run unless --no-shield)
if [[ "$INSTALL_SHIELD" == true ]]; then
  install_configure_shield
else
  info "⏭️  elchi-shield installation skipped (--no-shield)"
fi

###############################################################################
# COMPLETION SUMMARY
###############################################################################

# Show completion summary
echo ""
echo ""
printf "${C_OK}╔═══════════════════════════════════════════════════════════════════════════════╗${C_RST}\n"
printf "${C_OK}║                        🚀 ELCHI CLIENT INSTALLED! 🚀                       ║${C_RST}\n"
printf "${C_OK}╚═══════════════════════════════════════════════════════════════════════════════╝${C_RST}\n"
echo ""

# System Information Header
printf "${C_INF}┌─ 🖥️  SYSTEM INFORMATION ──────────────────────────────────────────────────────┐${C_RST}\n"
printf "${C_INF}│${C_RST} Hostname: ${C_OK}$(hostname)${C_RST}\n"
printf "${C_INF}│${C_RST} Architecture: ${C_OK}$(dpkg --print-architecture)${C_RST}\n"
printf "${C_INF}│${C_RST} Ubuntu Version: ${C_OK}$(lsb_release -ds)${C_RST}\n"
printf "${C_INF}│${C_RST} Kernel Version: ${C_OK}$(uname -r)${C_RST}\n"
printf "${C_INF}└──────────────────────────────────────────────────────────────────────────────┘${C_RST}\n"
echo ""

# Core Components Status
printf "${C_INF}┌─ ⚙️  CORE COMPONENTS STATUS ──────────────────────────────────────────────────┐${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Kernel Performance Tuning: ${C_OK}Applied${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ System Users (elchi/envoyuser): ${C_OK}Created${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Directory Structure: ${C_OK}Configured${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Systemd Service: ${C_OK}Installed & Enabled${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Sudoers Configuration: ${C_OK}Applied${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ APT Sources: ${C_OK}Optimized${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Netplan Configuration: ${C_OK}Split per Interface${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Routing Tables: ${C_OK}Generated${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Logging Infrastructure: ${C_OK}Configured${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Logrotate (5-min cron): ${C_OK}Enabled${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Filebeat: ${C_OK}Configured${C_RST}\n"
printf "${C_INF}│${C_RST} ✅ Rsyslog: ${C_OK}Configured${C_RST}\n"
printf "${C_INF}└──────────────────────────────────────────────────────────────────────────────┘${C_RST}\n"
echo ""

# Performance Optimizations Details
printf "${C_INF}┌─ 🚀 LOAD BALANCER OPTIMIZATIONS ─────────────────────────────────────────────┐${C_RST}\n"
printf "${C_INF}│${C_RST} 🔌 Max Connections: ${C_OK}2M concurrent${C_RST}\n"
printf "${C_INF}│${C_RST} 📁 File Descriptors: ${C_OK}4M system / 655K per process${C_RST}\n"
printf "${C_INF}│${C_RST} ⚡ TCP Optimization: ${C_OK}TIME_WAIT reuse enabled${C_RST}\n"
printf "${C_INF}│${C_RST} 🕐 FIN Timeout: ${C_OK}30s (optimized for proxy)${C_RST}\n"
printf "${C_INF}│${C_RST} 🔄 Keepalive: ${C_OK}120s/30s/3 probes${C_RST}\n"
printf "${C_INF}│${C_RST} 📊 SYN Backlog: ${C_OK}32K connections${C_RST}\n"
printf "${C_INF}│${C_RST} 🎯 somaxconn: ${C_OK}32K queue size${C_RST}\n"
printf "${C_INF}│${C_RST} 🔢 Port Range: ${C_OK}1024-65535${C_RST}\n"
printf "${C_INF}└──────────────────────────────────────────────────────────────────────────────┘${C_RST}\n"
echo ""

# Optional Components Status
printf "${C_INF}┌─ 🐟 OPTIONAL COMPONENTS ─────────────────────────────────────────────────────┐${C_RST}\n"
if [[ "$ENABLE_FRR" == true ]]; then
  FRR_VERSION=$(dpkg -l | grep "^ii.*frr " | awk '{print $3}' | head -1 2>/dev/null || echo "Unknown")
  printf "${C_INF}│${C_RST} ✅ FRR Routing: ${C_OK}Installed (${FRR_VERSION})${C_RST}\n"
  printf "${C_INF}│${C_RST}    ├─ Zebra daemon: ${C_OK}Enabled${C_RST}\n"
  printf "${C_INF}│${C_RST}    ├─ BGP daemon: ${C_OK}Enabled${C_RST}\n"
  printf "${C_INF}│${C_RST}    └─ gRPC/vtysh: ${C_OK}Configured${C_RST}\n"
else
  printf "${C_INF}│${C_RST} ⏭️  FRR Routing: ${C_WRN}Skipped${C_RST}\n"
  printf "${C_INF}│${C_RST}    └─ 💡 Enable with: ${C_INF}$0 --enable-bgp${C_RST}\n"
fi

if [[ "$INSTALL_SHIELD" == true ]]; then
  SHIELD_STATE=$(systemctl is-active elchi-shield.service 2>/dev/null || echo inactive)
  printf "${C_INF}│${C_RST} 🛡️  elchi-shield (ext_proc WAF): ${C_OK}Installed${C_RST} (service: %s)\n" "$SHIELD_STATE"
  printf "${C_INF}│${C_RST}    └─ ext_proc UDS: ${C_OK}/run/elchi-shield/extproc.sock${C_RST}\n"
else
  printf "${C_INF}│${C_RST} ⏭️  elchi-shield: ${C_WRN}Skipped (--no-shield)${C_RST}\n"
fi

printf "${C_INF}└──────────────────────────────────────────────────────────────────────────────┘${C_RST}\n"
echo ""

# Configuration Files Summary
printf "${C_INF}┌─ 📄 CONFIGURATION FILES ─────────────────────────────────────────────────────┐${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Sysctl: ${C_OK}/etc/sysctl.d/elchi.conf${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Limits: ${C_OK}/etc/security/limits.d/elchi.conf${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Modprobe: ${C_OK}/etc/modprobe.d/elchi.conf${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Sudoers: ${C_OK}/etc/sudoers.d/99-elchi${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Service: ${C_OK}/etc/systemd/system/elchi-client.service${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Config: ${C_OK}/etc/elchi/config.yaml${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Routing: ${C_OK}/etc/iproute2/rt_tables.d/elchi.conf${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Logrotate: ${C_OK}/etc/logrotate.d/elchi${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Logrotate Cron: ${C_OK}/etc/cron.d/logrotate-5min${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Filebeat: ${C_OK}/etc/filebeat/filebeat.yml${C_RST}\n"
printf "${C_INF}│${C_RST} 📝 Rsyslog: ${C_OK}/etc/rsyslog.d/50-elchi.conf${C_RST}\n"
printf "${C_INF}│${C_RST} 📁 Log Directory: ${C_OK}/var/log/elchi${C_RST}\n"
printf "${C_INF}└──────────────────────────────────────────────────────────────────────────────┘${C_RST}\n"
echo ""

# Service Status Check
SERVICE_STATUS=$(systemctl is-active elchi-client.service 2>/dev/null || echo "inactive")
BINARY_EXISTS=$(test -f "$ELCHI_BIN_DIR/elchi-client" && echo "yes" || echo "no")

printf "${C_INF}┌─ 🔧 SERVICE STATUS ──────────────────────────────────────────────────────────┐${C_RST}\n"
if [[ "$BINARY_EXISTS" == "yes" ]]; then
  if [[ "$SERVICE_STATUS" == "active" ]]; then
    printf "${C_INF}│${C_RST} 🟢 elchi-client.service: ${C_OK}Running${C_RST}\n"
  else
    printf "${C_INF}│${C_RST} 🟡 elchi-client.service: ${C_WRN}Stopped (binary available)${C_RST}\n"
  fi
  printf "${C_INF}│${C_RST} ✅ elchi-client binary: ${C_OK}Installed${C_RST}\n"
else
  printf "${C_INF}│${C_RST} 🟡 elchi-client.service: ${C_WRN}Stopped (binary missing)${C_RST}\n"
  printf "${C_INF}│${C_RST} ❌ elchi-client binary: ${C_ERR}Not found${C_RST}\n"
fi
printf "${C_INF}│${C_RST} 🔍 Check status: ${C_OK}systemctl status elchi-client${C_RST}\n"
printf "${C_INF}│${C_RST} 📜 View logs: ${C_OK}journalctl -u elchi-client -f${C_RST}\n"
printf "${C_INF}└──────────────────────────────────────────────────────────────────────────────┘${C_RST}\n"
echo ""

# Next Steps
printf "${C_INF}┌─ 🎯 NEXT STEPS ──────────────────────────────────────────────────────────────┐${C_RST}\n"
if [[ "$BINARY_EXISTS" == "yes" ]]; then
  printf "${C_INF}│${C_RST} 1️⃣  Edit config: ${C_OK}/etc/elchi/config.yaml${C_RST}\n"
  printf "${C_INF}│${C_RST} 2️⃣  Restart service: ${C_OK}systemctl restart elchi-client${C_RST}\n"
else
  printf "${C_INF}│${C_RST} 1️⃣  Binary download failed - manually download from:${C_RST}\n"
  printf "${C_INF}│${C_RST}     ${C_WRN}https://github.com/CloudNativeWorks/elchi-client/releases${C_RST}\n"
  printf "${C_INF}│${C_RST} 2️⃣  Copy elchi-client binary to: ${C_OK}/etc/elchi/bin/${C_RST}\n"
  printf "${C_INF}│${C_RST} 3️⃣  Configure: ${C_OK}/etc/elchi/config.yaml${C_RST}\n"
  printf "${C_INF}│${C_RST} 4️⃣  Start service: ${C_OK}systemctl start elchi-client${C_RST}\n"
fi
printf "${C_INF}└──────────────────────────────────────────────────────────────────────────────┘${C_RST}\n"
echo ""

# Final Success Message
printf "${C_OK}🎉 Elchi Client installed successfully! Your system is optimized ready for production${C_RST}\n"
printf "${C_OK}   workloads.${C_RST}\n"
echo ""

# Show reboot recommendation if kernel parameters changed
printf "${C_WRN}⚠️  Recommendation: Reboot the system to ensure all kernel optimizations${C_RST}\n"
printf "${C_WRN}   are fully applied: ${C_OK}sudo reboot${C_RST}\n"
echo ""
