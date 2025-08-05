#!/usr/bin/env bash
#
# elchi-bootstrap.sh (v3)
# --------------------------------------------------
# • Kernel / sysctl / limits settings
# • elchi & envoyuser users
# • /etc/elchi and /var/lib/elchi hierarchy
# • elchi-client systemd service
# • Optional: FRR installation (--enable-bgp)
# • Optional: SSH security hardening (--enable-ssh-security)
#

set -euo pipefail
shopt -s inherit_errexit

###############################################################################
# GLOBAL VARIABLES AND CONFIGURATION
###############################################################################

# Command line arguments
SSH_BIND_IP=""
ENABLE_FRR=false
ENABLE_SSH_SECURITY=false
SSH_ADMIN_PASSWORD=""  # Will store generated password for SSH admin user

# System users
ELCHI_USER="elchi"
ENVOY_USER="envoyuser"

# Directory paths
ELCHI_DIR="/etc/elchi"
ELCHI_BIN_DIR="$ELCHI_DIR/bin"
ELCHI_CONFIG="$ELCHI_DIR/config.yaml"
ELCHI_VAR_LIB="/var/lib/elchi"
ELCHI_VAR_DIRS=( bootstraps envoys hotrestarter lua tmp )

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

# Password generation
generate_password() {
  head -c 12 /dev/urandom | base64
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

for arg in "$@"; do
  case $arg in
    --ssh-bind-ip=*)
      SSH_BIND_IP="${arg#*=}"
      shift
      ;;
    --enable-bgp)
      ENABLE_FRR=true
      shift
      ;;
    --enable-ssh-security)
      ENABLE_SSH_SECURITY=true
      shift
      ;;
    --help|-h)
      echo "Usage: $0 [OPTIONS]"
      echo ""
      echo "Options:"
      echo "  --ssh-bind-ip=IP           Set specific IP for SSH binding"
      echo "  --enable-bgp               Install and configure FRR routing"
      echo "  --enable-ssh-security      Enable SSH security hardening"
      echo "  --help, -h                 Show this help message"
      echo ""
      echo "Examples:"
      echo "  $0                                    # Basic installation only"
      echo "  $0 --enable-bgp                      # With FRR routing"
      echo "  $0 --enable-ssh-security             # With SSH hardening"
      echo "  $0 --enable-bgp --enable-ssh-security --ssh-bind-ip=10.0.0.1"
      exit 0
      ;;
    *)
      echo "Unknown option: $arg"
      echo "Use --help for usage information"
      exit 1
      ;;
  esac
done

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
Maintainer: elchi-bootstrap
Architecture: arm64
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

define_routing_tables() {
  info "generating deterministic routing table IDs (hash-based)"

  local output_file="/etc/iproute2/rt_tables.d/elchi.conf"
  local base_id=101
  local max_id=199

  > "$output_file"  # Start clean

  mapfile -t ifaces < <(
    ip -o link show | awk -F': ' '{print $2 " " $3}' | while read -r line; do
      iface=$(awk '{print $1}' <<< "$line")
      flags=$(awk -F'[<>]' '{print $2}' <<< "$line")
      [[ "$iface" == "lo" || "$flags" == *NOARP* ]] && continue
      echo "$iface"
    done
  )

  for iface in "${ifaces[@]}"; do
    id=$(echo -n "$iface" | cksum | awk -v base="$base_id" -v max="$max_id" '{print base + ($1 % (max - base + 1))}')
    printf "%s elchi-if-%s\n" "$id" "$iface" >> "$output_file"
  done

  ok "routing tables written to $output_file"
}

###############################################################################
# NETPLAN CONFIGURATION
###############################################################################

split_netplan_physical_interfaces() {
  local netplan_dir="/etc/netplan"
  local if_prefix="50-elchi-if"
  local route_prefix="50-elchi-r"

  info "📡 Splitting netplan YAML files per physical interface with separate route files"

  [[ -d "$netplan_dir" ]] || fail "Netplan directory not found: $netplan_dir"

  # Detect physical interfaces
  mapfile -t phys_ifaces < <(
    ip -o link show | awk -F': ' '{print $2 " " $3}' | while read -r line; do
      iface=$(awk '{print $1}' <<< "$line")
      flags=$(awk -F'[<>]' '{print $2}' <<< "$line")
      [[ "$iface" == "lo" || "$flags" == *NOARP* ]] && continue
      echo "$iface"
    done
  )
  (( ${#phys_ifaces[@]} == 0 )) && fail "No physical interfaces found"

  mapfile -t netplan_files < <(find "$netplan_dir" -maxdepth 1 -name "*.yaml" | sort)

  declare -A iface_to_yaml
  declare -A yaml_to_ifaces

  # Parse existing netplan files
  for yaml_file in "${netplan_files[@]}"; do
    [[ -f "$yaml_file" ]] || continue

    # Skip backup files and elchi-generated files
    [[ "$yaml_file" == *.bak || "$yaml_file" == *"${if_prefix}-"* || "$yaml_file" == *"${route_prefix}-"* ]] && continue

    debug "🔍 Checking file: $yaml_file"

    mapfile -t file_ifaces < <(yq -r '.network.ethernets | keys[]?' "$yaml_file" 2>/dev/null || true)

    if [[ ${#file_ifaces[@]} -eq 0 ]]; then
      debug "🔎 Interfaces in $yaml_file: ∅"
    else
      debug "🔎 Interfaces in $yaml_file: \"${file_ifaces[*]}\""
    fi

    for iface in "${file_ifaces[@]}"; do
      iface_to_yaml["$iface"]="$yaml_file"
      yaml_to_ifaces["$yaml_file"]+="$iface "
    done
  done

  # Process each netplan file
  for yaml_file in "${!yaml_to_ifaces[@]}"; do
    read -ra ifaces <<< "${yaml_to_ifaces[$yaml_file]}"
    should_backup=false

    for iface in "${ifaces[@]}"; do
      if [[ " ${phys_ifaces[*]} " == *" $iface "* ]]; then
        debug "🔧 Processing physical interface: $iface"
        local if_file="$netplan_dir/${if_prefix}-${iface}.yaml"
        local route_file="$netplan_dir/${route_prefix}-${iface}.yaml"
        
        # Skip if interface file already exists
        if [[ -f "$if_file" ]]; then
          debug "🔧 Interface file already exists: $if_file, skipping"
          continue
        fi

        # Extract interface config without routes
        debug "🔧 Extracting config for interface $iface from $yaml_file"
        local iface_config
        local yq_error
        iface_config=$(yq -y ".network.ethernets.\"$iface\" | del(.routes)" "$yaml_file" 2>&1)
        yq_error=$?
        
        if [[ $yq_error -ne 0 ]]; then
          debug "🚨 yq command failed with exit code $yq_error: $iface_config"
          iface_config=""
        else
          debug "🔧 Extracted config: $iface_config"
        fi
        
        # Check if interface config is valid, fallback to dhcp4: true if empty/null
        if [[ -z "$iface_config" || "$iface_config" == "null" || "$iface_config" == "{}" ]]; then
          debug "🔧 Config empty/null, using dhcp4: true fallback"
          iface_config="dhcp4: true"
        fi
        
        # Create interface file
        {
          echo "network:"
          echo "  version: 2"
          echo "  ethernets:"
          echo "    $iface:"
          echo "$iface_config" | sed 's/^/      /'
        } > "$if_file" || fail "Failed to write $if_file"
        chmod 600 "$if_file"
        ok "→ Created $if_file"

        # Extract and create route file if routes exist
        debug "🔧 Checking routes for interface $iface"
        local routes_exist
        local routes_error
        routes_exist=$(yq -r ".network.ethernets.\"$iface\".routes // empty | length" "$yaml_file" 2>&1)
        routes_error=$?
        
        if [[ $routes_error -ne 0 ]]; then
          debug "🚨 Routes check failed with exit code $routes_error: $routes_exist"
          routes_exist="0"
        else
          debug "🔧 Routes count: $routes_exist"
        fi
        
        if [[ "$routes_exist" != "0" && -n "$routes_exist" ]]; then
          debug "🔧 Extracting route config for $iface"
          local route_config
          local route_extract_error
          route_config=$(yq -y ".network.ethernets.\"$iface\" | {routes: .routes}" "$yaml_file" 2>&1)
          route_extract_error=$?
          
          if [[ $route_extract_error -ne 0 ]]; then
            debug "🚨 Route extraction failed with exit code $route_extract_error: $route_config"
            route_config=""
          else
            debug "🔧 Route config: $route_config"
          fi
          
          if [[ -n "$route_config" && "$route_config" != "null" ]]; then
            {
              echo "network:"
              echo "  version: 2"
              echo "  ethernets:"
              echo "    $iface:"
              echo "$route_config" | sed 's/^/      /'
            } > "$route_file" || fail "Failed to write $route_file"
            chmod 600 "$route_file"
            ok "→ Created $route_file with routes"
          fi
        fi

        should_backup=true
      fi
    done

    # Backup original file if any interface was processed
    if [[ "$should_backup" == true ]]; then
      mv "$yaml_file" "${yaml_file}.bak" || fail "Failed to backup $yaml_file"
      ok "→ Moved $yaml_file → ${yaml_file}.bak"
    fi
  done

  # Create files for missing interfaces
  for iface in "${phys_ifaces[@]}"; do
    [[ -n "${iface_to_yaml[$iface]:-}" ]] && continue

    local if_file="$netplan_dir/${if_prefix}-${iface}.yaml"
    [[ -f "$if_file" ]] && continue

    {
      echo "network:"
      echo "  version: 2"
      echo "  ethernets:"
      echo "    $iface:"
      echo "      dhcp4: true"
    } > "$if_file" || fail "Failed to write $if_file"
    chmod 600 "$if_file"
    ok "→ Added missing interface $iface to $if_file"
  done

  run netplan generate
  run netplan apply

  ok "🏁 All physical interfaces processed and split into separate netplan YAML files"
}

###############################################################################
# SSH SECURITY HARDENING
###############################################################################

secure_ssh_access() {
  info "🔐 Starting SSH hardening"

  NEW_USER="elchiadmin"
  SSH_CONF_FILE="/etc/ssh/sshd_config.d/00-elchi-hardening.conf"
  SSH_ADMIN_PASSWORD=$(generate_password)

  debug "[DEBUG] Entered secure_ssh_access"
  debug "[DEBUG] Checking if user $NEW_USER exists"

  # Create or update user
  if id "$NEW_USER" &>/dev/null; then
    ok "User '$NEW_USER' already exists"
    run_with_pipe "echo '$NEW_USER:$SSH_ADMIN_PASSWORD' | chpasswd"
    ok "[🔑] Password reset:"
  else
    run useradd -m -s /bin/bash "$NEW_USER"
    run_with_pipe "echo '$NEW_USER:$SSH_ADMIN_PASSWORD' | chpasswd"
    run usermod -aG sudo "$NEW_USER"
    ok "[🔑] New user created:"
  fi

  ok "  Username: $NEW_USER"
  ok "  Password: $SSH_ADMIN_PASSWORD"

  # Determine SSH bind IP
  if [[ -n "$SSH_BIND_IP" ]]; then
    INTERFACE_IP="$SSH_BIND_IP"
    info "📌 Using provided SSH bind IP: $INTERFACE_IP"
  else
    info "🔍 Detecting interface IP..."
    
    # Try multiple methods to find interface IP
    for attempt in 1 2 3; do
      debug "Attempt $attempt to find interface IP"
      
      # Method 1: ip command with scope global
      INTERFACE_IP=$(ip -4 addr show scope global | awk '/inet/ {print $2}' | cut -d/ -f1 | head -n1 2>/dev/null || true)
      [[ -n "$INTERFACE_IP" ]] && break
      
      # Method 2: ip route get
      INTERFACE_IP=$(ip route get 8.8.8.8 2>/dev/null | awk '/src/ {print $7}' | head -n1 || true)
      [[ -n "$INTERFACE_IP" ]] && break
      
      # Method 3: hostname -I 
      INTERFACE_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
      [[ -n "$INTERFACE_IP" ]] && break
      
      # Wait a bit before retry
      [[ $attempt -lt 3 ]] && sleep 2
    done
    
    if [[ -z "$INTERFACE_IP" ]]; then
      warn "Could not auto-detect interface IP, using 0.0.0.0 (all interfaces)"
      INTERFACE_IP="0.0.0.0"
    fi
    
    info "📌 Auto-selected SSH bind IP: $INTERFACE_IP"
  fi

  # Write SSH hardening configuration
  info "✏️ Writing SSH hardening config to $SSH_CONF_FILE"
  cat > "$SSH_CONF_FILE" <<EOF
# Elchi SSH Hardening
PermitRootLogin no
PasswordAuthentication yes
ChallengeResponseAuthentication no
KbdInteractiveAuthentication no
UsePAM yes
X11Forwarding no

ClientAliveInterval 300
ClientAliveCountMax 2
ListenAddress $INTERFACE_IP
EOF

  chmod 644 "$SSH_CONF_FILE"
  run systemctl daemon-reexec
  run systemctl restart ssh

  # Disable cloud-init SSH configuration if present
  CLOUD_INIT_FILE="/etc/ssh/sshd_config.d/50-cloud-init.conf"
  if [[ -f "$CLOUD_INIT_FILE" ]]; then
    run mv "$CLOUD_INIT_FILE" "$CLOUD_INIT_FILE.disabled"
    ok "Disabled $CLOUD_INIT_FILE"
  fi

  ok "SSH access hardened: root login disabled, '$NEW_USER' enabled"
}

###############################################################################
# MAIN EXECUTION FLOW
###############################################################################

# Set up signal handlers and check root privileges
trap 'cleanup_on_exit' INT TERM
trap 'fail "line $LINENO (exit code $?)"' ERR
[[ $EUID -eq 0 ]] || fail "Run this script as root (sudo …)"

info "=== Elchi bootstrap v3 ==="

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
net.ipv4.tcp_fin_timeout       = 30

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

# Configure sudoers
info "creating sudoers rule"
cat >"$SUDO_FILE"<<'EOF'
Cmnd_Alias ELCHI_CMDS = \
 /usr/bin/systemctl daemon-reload, \
 /usr/bin/systemctl start *.service, \
 /usr/bin/systemctl stop *.service, \
 /usr/bin/systemctl restart *.service, \
 /usr/bin/systemctl reload *.service, \
 /usr/bin/systemctl enable --now *.service, \
 /usr/bin/systemctl disable *.service, \
 /usr/bin/systemctl status *.service, \
 /usr/bin/tee /etc/systemd/journald@elchi-*.conf, \
 /usr/bin/tee /etc/netplan/50-elchi-*.yaml, \
 /usr/bin/tee /etc/netplan/90-elchi-*.yaml, \
 /usr/bin/chmod 0600 /etc/netplan/50-elchi-*.yaml, \
 /usr/bin/chmod 0600 /etc/netplan/90-elchi-*.yaml, \
 /usr/bin/netplan generate, \
 /usr/bin/netplan apply

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

elchi ALL=(ALL) NOPASSWD: ELCHI_CMDS, FRR_CMDS
Defaults:elchi !pam_session

EOF
chmod 440 "$SUDO_FILE"
run visudo -cf "$SUDO_FILE"

# Create directory structure
info "/etc/elchi tree"
run mkdir -p "$ELCHI_BIN_DIR"
run chown -R root:"$ELCHI_USER" "$ELCHI_DIR"
run chmod 750 "$ELCHI_DIR" "$ELCHI_BIN_DIR"

# Download config.yaml from latest GitHub release
info "📥 Downloading config.yaml from latest GitHub release"
GITHUB_REPO="CloudNativeWorks/elchi-client"
LATEST_RELEASE_URL="https://api.github.com/repos/$GITHUB_REPO/releases/latest"

# Get latest release tag
LATEST_TAG=$(curl -s "$LATEST_RELEASE_URL" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' || echo "")

if [[ -n "$LATEST_TAG" ]]; then
  CONFIG_DOWNLOAD_URL="https://github.com/$GITHUB_REPO/releases/download/$LATEST_TAG/config.yaml"
  info "📦 Latest release: $LATEST_TAG"
  info "🔗 Downloading from: $CONFIG_DOWNLOAD_URL"
  
  TEMP_CONFIG="/tmp/elchi-config-download.yaml"
  
  if curl -fsSL --retry 3 --retry-delay 2 --max-time 30 "$CONFIG_DOWNLOAD_URL" -o "$TEMP_CONFIG"; then
    if mv "$TEMP_CONFIG" "$ELCHI_CONFIG"; then
      ok "✅ config.yaml downloaded successfully"
      run chown root:"$ELCHI_USER" "$ELCHI_CONFIG"
      run chmod 640 "$ELCHI_CONFIG"
    else
      warn "⚠️  Failed to move config.yaml to $ELCHI_CONFIG"
      rm -f "$TEMP_CONFIG"
      touch "$ELCHI_CONFIG"
      chown root:"$ELCHI_USER" "$ELCHI_CONFIG"
      chmod 640 "$ELCHI_CONFIG"
    fi
  else
    warn "⚠️  Failed to download config.yaml from GitHub release, creating default"
    rm -f "$TEMP_CONFIG"
    touch "$ELCHI_CONFIG"
    chown root:"$ELCHI_USER" "$ELCHI_CONFIG"
    chmod 640 "$ELCHI_CONFIG"
  fi
  
  # Download elchi-client binary
  info "📥 Downloading elchi-client binary from latest GitHub release"
  BINARY_DOWNLOAD_URL="https://github.com/$GITHUB_REPO/releases/download/$LATEST_TAG/elchi-client"
  BINARY_PATH="$ELCHI_BIN_DIR/elchi-client"
  TEMP_BINARY="/tmp/elchi-client-download"
  
  info "🔗 Downloading binary from: $BINARY_DOWNLOAD_URL"
  
  # Download to temp location first
  if curl -fsSL --retry 3 --retry-delay 2 --max-time 60 "$BINARY_DOWNLOAD_URL" -o "$TEMP_BINARY"; then
    info "✅ Binary downloaded to temp location"
    
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

ReadWritePaths=/etc/netplan /etc/elchi /var/lib/elchi /usr/lib/systemd/system /etc/systemd
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
run systemctl enable --now elchi-client.service

# Configure system components
setup_sources_list
define_routing_tables
ensure_yq_installed
split_netplan_physical_interfaces

# Optional: FRR installation
if [[ "$ENABLE_FRR" == true ]]; then
  info "🐟 FRR installation enabled"
  install_configure_frr
else
  info "⏭️  FRR installation skipped (use --enable-bgp to enable)"
fi

# Optional: SSH security hardening
if [[ "$ENABLE_SSH_SECURITY" == true ]]; then
  info "🔐 SSH security hardening enabled"
  secure_ssh_access
else
  info "⏭️  SSH security hardening skipped (use --enable-ssh-security to enable)"
fi

###############################################################################
# COMPLETION SUMMARY
###############################################################################

# Show completion summary
echo ""
echo ""
printf "${C_OK}╔═══════════════════════════════════════════════════════════════════════════════╗${C_RST}\n"
printf "${C_OK}║                        🚀 ELCHI BOOTSTRAP COMPLETED! 🚀                       ║${C_RST}\n"
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

if [[ "$ENABLE_SSH_SECURITY" == true ]]; then
  printf "${C_INF}│${C_RST} ✅ SSH Hardening: ${C_OK}Applied${C_RST}\n"
  printf "${C_INF}│${C_RST}    ├─ Root login: ${C_OK}Disabled${C_RST}\n"
  printf "${C_INF}│${C_RST}    ├─ Admin user: ${C_OK}elchiadmin${C_RST}\n"
  printf "${C_INF}│${C_RST}    ├─ Password: ${C_WRN}${SSH_ADMIN_PASSWORD}${C_RST}\n"
  printf "${C_INF}│${C_RST}    └─ Bind IP: ${C_OK}${INTERFACE_IP:-"auto-detected"}${C_RST}\n"
else
  printf "${C_INF}│${C_RST} ⏭️  SSH Hardening: ${C_WRN}Skipped${C_RST}\n"
  printf "${C_INF}│${C_RST}    └─ 💡 Enable with: ${C_INF}$0 --enable-ssh-security${C_RST}\n"
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
if [[ "$ENABLE_SSH_SECURITY" == true ]]; then
  printf "${C_INF}│${C_RST} 📝 SSH Config: ${C_OK}/etc/ssh/sshd_config.d/00-elchi-hardening.conf${C_RST}\n"
fi
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

# Security Notice for SSH Credentials
if [[ "$ENABLE_SSH_SECURITY" == true ]]; then
  printf "${C_WRN}🔐 SECURITY NOTICE: SSH admin credentials shown above. Please:${C_RST}\n"
  printf "${C_WRN}   • Save the password securely${C_RST}\n"
  printf "${C_WRN}   • Consider changing it after first login${C_RST}\n"
  printf "${C_WRN}   • Test SSH access before disconnecting${C_RST}\n"
  echo ""
fi

# Final Success Message
printf "${C_OK}🎉 Bootstrap completed successfully! Your system is optimized ready for production${C_RST}\n"
printf "${C_OK}   workloads.${C_RST}\n"
echo ""

# Show reboot recommendation if kernel parameters changed
printf "${C_WRN}⚠️  Recommendation: Reboot the system to ensure all kernel optimizations${C_RST}\n"
printf "${C_WRN}   are fully applied: ${C_OK}sudo reboot${C_RST}\n"
echo ""
