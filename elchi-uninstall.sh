#!/usr/bin/env bash
#
# elchi-uninstall.sh
# --------------------------------------------------
# Safely removes all elchi components from the system
# WARNING: This will remove all elchi configurations and services
#

set -euo pipefail

###############################################################################
# GLOBAL VARIABLES
###############################################################################

# System users
ELCHI_USER="elchi"
ENVOY_USER="envoyuser"

# Directory paths
ELCHI_DIR="/etc/elchi"
ELCHI_VAR_LIB="/var/lib/elchi"

# Configuration files
SYSCTL_FILE="/etc/sysctl.d/elchi.conf"
LIMITS_FILE="/etc/security/limits.d/elchi.conf"
MODPROBE_FILE="/etc/modprobe.d/elchi.conf"
SUDO_FILE="/etc/sudoers.d/99-${ELCHI_USER}"
SERVICE_FILE="/etc/systemd/system/elchi-client.service"
ROUTING_TABLE_LINK="/etc/iproute2/rt_tables.d/elchi.conf"

# ANSI colors
C_RST='\033[0m'
C_INF='\033[1;34m' # blue
C_OK='\033[1;32m'  # green
C_WRN='\033[1;33m' # yellow
C_ERR='\033[1;31m' # red

###############################################################################
# UTILITY FUNCTIONS
###############################################################################

info() { printf "${C_INF}[INFO] %s${C_RST}\n" "$*"; }
ok()   { printf "${C_OK}[ OK ] %s${C_RST}\n"  "$*"; }
warn() { printf "${C_WRN}[WARN] %s${C_RST}\n" "$*"; }
fail() { printf "${C_ERR}[FAIL] %s${C_RST}\n" "$*"; exit 1; }

confirm() {
    local prompt="$1"
    local response
    
    printf "${C_WRN}%s [y/N]: ${C_RST}" "$prompt"
    read -r response
    
    case "$response" in
        [yY]|[yY][eE][sS]) return 0 ;;
        *) return 1 ;;
    esac
}

###############################################################################
# SAFETY CHECKS
###############################################################################

# Check root privileges
[[ $EUID -eq 0 ]] || fail "This script must be run as root (sudo)"

# Confirmation prompt
echo ""
echo "════════════════════════════════════════════════════════════════"
echo "                    ELCHI UNINSTALL SCRIPT"
echo "════════════════════════════════════════════════════════════════"
echo ""
warn "This script will remove:"
echo "  • elchi-client service and binary"
echo "  • All elchi configurations"
echo "  • System users: $ELCHI_USER, $ENVOY_USER"
echo "  • Directories: $ELCHI_DIR, $ELCHI_VAR_LIB"
echo "  • System configurations (sysctl, limits, sudoers)"
echo "  • All running elchi services"
echo ""
warn "This action is IRREVERSIBLE!"
echo ""

if ! confirm "Are you sure you want to continue?"; then
    info "Uninstall cancelled"
    exit 0
fi

echo ""
if ! confirm "Please confirm once more - remove all elchi components?"; then
    info "Uninstall cancelled"
    exit 0
fi

echo ""
info "Starting uninstall process..."

###############################################################################
# STOP SERVICES
###############################################################################

info "Stopping elchi services..."

# Stop elchi-client service
if systemctl list-units --full -all | grep -Fq "elchi-client.service"; then
    info "Stopping elchi-client service..."
    systemctl stop elchi-client.service 2>/dev/null || true
    systemctl disable elchi-client.service 2>/dev/null || true
    ok "elchi-client service stopped and disabled"
else
    info "elchi-client service not found"
fi

# Stop any elchi-managed services
info "Stopping elchi-managed services..."
for service in /etc/systemd/system/elchi-*.service; do
    if [[ -f "$service" ]]; then
        service_name=$(basename "$service")
        info "Stopping $service_name..."
        systemctl stop "$service_name" 2>/dev/null || true
        systemctl disable "$service_name" 2>/dev/null || true
    fi
done

# Kill any remaining elchi processes
info "Killing remaining elchi processes..."
pkill -u "$ELCHI_USER" 2>/dev/null || true
pkill -u "$ENVOY_USER" 2>/dev/null || true
sleep 2

###############################################################################
# REMOVE NETPLAN CONFIGURATIONS
###############################################################################

info "Removing netplan configurations..."

# Remove elchi-managed netplan files
for netplan_file in /etc/netplan/99-elchi-*.yaml /etc/netplan/90-*.yaml; do
    if [[ -f "$netplan_file" ]]; then
        info "Removing $(basename "$netplan_file")"
        rm -f "$netplan_file"
    fi
done

# Restore original netplan if it was renamed
if [[ -f "/etc/netplan/50-cloud-init.yaml.elchi-disabled" ]]; then
    info "Restoring original netplan configuration..."
    mv "/etc/netplan/50-cloud-init.yaml.elchi-disabled" "/etc/netplan/50-cloud-init.yaml"
    ok "Original netplan restored"
fi

# Look for other disabled netplan files
for disabled_file in /etc/netplan/*.elchi-disabled; do
    if [[ -f "$disabled_file" ]]; then
        original_name="${disabled_file%.elchi-disabled}"
        info "Restoring $(basename "$original_name")"
        mv "$disabled_file" "$original_name"
    fi
done

###############################################################################
# REMOVE FRR (if installed by elchi)
###############################################################################

if command -v vtysh >/dev/null 2>&1; then
    if confirm "FRR is installed. Remove FRR?"; then
        info "Removing FRR..."
        systemctl stop frr 2>/dev/null || true
        apt-get remove -y frr frr-pythontools 2>/dev/null || true
        apt-get autoremove -y 2>/dev/null || true
        rm -f /etc/apt/sources.list.d/frr.list
        rm -f /etc/apt/preferences.d/frr*
        rm -f /usr/share/keyrings/frrouting.gpg
        ok "FRR removed"
    else
        info "Keeping FRR installation"
    fi
fi

###############################################################################
# REMOVE SYSTEMD SERVICES
###############################################################################

info "Removing systemd services..."

# Remove main service file
if [[ -f "$SERVICE_FILE" ]]; then
    rm -f "$SERVICE_FILE"
    ok "Removed elchi-client.service"
fi

# Remove all elchi-managed services
for service in /etc/systemd/system/elchi-*.service; do
    if [[ -f "$service" ]]; then
        rm -f "$service"
        ok "Removed $(basename "$service")"
    fi
done

# Remove journal configs
for journal_conf in /etc/systemd/journald@elchi-*.conf; do
    if [[ -f "$journal_conf" ]]; then
        rm -f "$journal_conf"
        ok "Removed $(basename "$journal_conf")"
    fi
done

systemctl daemon-reload

###############################################################################
# REMOVE SYSTEM CONFIGURATIONS
###############################################################################

info "Removing system configurations..."

# Remove sysctl config
if [[ -f "$SYSCTL_FILE" ]]; then
    rm -f "$SYSCTL_FILE"
    ok "Removed sysctl configuration"
    # Note: We don't reset sysctl values as they might be used by other services
fi

# Remove limits config
if [[ -f "$LIMITS_FILE" ]]; then
    rm -f "$LIMITS_FILE"
    ok "Removed limits configuration"
fi

# Remove modprobe config
if [[ -f "$MODPROBE_FILE" ]]; then
    rm -f "$MODPROBE_FILE"
    ok "Removed modprobe configuration"
fi

# Remove sudoers config
if [[ -f "$SUDO_FILE" ]]; then
    rm -f "$SUDO_FILE"
    ok "Removed sudoers configuration"
fi

# Remove routing table symlink
if [[ -L "$ROUTING_TABLE_LINK" ]]; then
    rm -f "$ROUTING_TABLE_LINK"
    ok "Removed routing table symlink"
fi

###############################################################################
# REMOVE DIRECTORIES
###############################################################################

info "Removing elchi directories..."

# Backup warning for /etc/elchi
if [[ -d "$ELCHI_DIR" ]]; then
    if [[ -f "$ELCHI_DIR/config.yaml" ]]; then
        warn "Found configuration file: $ELCHI_DIR/config.yaml"
        if confirm "Backup config.yaml before removal?"; then
            backup_file="$HOME/elchi-config-backup-$(date +%Y%m%d-%H%M%S).yaml"
            cp "$ELCHI_DIR/config.yaml" "$backup_file"
            ok "Config backed up to: $backup_file"
        fi
    fi
    
    rm -rf "$ELCHI_DIR"
    ok "Removed $ELCHI_DIR"
fi

# Remove /var/lib/elchi
if [[ -d "$ELCHI_VAR_LIB" ]]; then
    rm -rf "$ELCHI_VAR_LIB"
    ok "Removed $ELCHI_VAR_LIB"
fi

###############################################################################
# REMOVE USERS
###############################################################################

info "Removing system users..."

# Remove elchi user
if id "$ELCHI_USER" &>/dev/null; then
    userdel "$ELCHI_USER" 2>/dev/null || true
    ok "Removed user: $ELCHI_USER"
else
    info "User $ELCHI_USER not found"
fi

# Remove envoyuser
if id "$ENVOY_USER" &>/dev/null; then
    userdel "$ENVOY_USER" 2>/dev/null || true
    ok "Removed user: $ENVOY_USER"
else
    info "User $ENVOY_USER not found"
fi

# Remove groups if they exist
if getent group "$ELCHI_USER" >/dev/null; then
    groupdel "$ELCHI_USER" 2>/dev/null || true
    ok "Removed group: $ELCHI_USER"
fi

if getent group "$ENVOY_USER" >/dev/null; then
    groupdel "$ENVOY_USER" 2>/dev/null || true
    ok "Removed group: $ENVOY_USER"
fi

###############################################################################
# APT SOURCES CLEANUP
###############################################################################

info "Cleaning up APT sources..."

# Re-enable ubuntu.sources if it was disabled
if [[ -f "/etc/apt/sources.list.d/ubuntu.sources.disabled" ]]; then
    mv "/etc/apt/sources.list.d/ubuntu.sources.disabled" "/etc/apt/sources.list.d/ubuntu.sources"
    ok "Re-enabled ubuntu.sources"
fi

# Restore original sources.list if backup exists
if [[ -f "/etc/apt/sources.list.bak" ]]; then
    if confirm "Restore original APT sources.list?"; then
        mv "/etc/apt/sources.list.bak" "/etc/apt/sources.list"
        ok "Restored original sources.list"
    fi
fi

###############################################################################
# FINAL CLEANUP
###############################################################################

info "Performing final cleanup..."

# Clean APT cache
apt-get clean 2>/dev/null || true

# Remove any leftover temporary files
rm -rf /tmp/elchi-* 2>/dev/null || true
rm -rf /tmp/libyang2-* 2>/dev/null || true

###############################################################################
# COMPLETION SUMMARY
###############################################################################

echo ""
echo ""
printf "${C_OK}╔═══════════════════════════════════════════════════════════════════════════╗${C_RST}\n"
printf "${C_OK}║                    🧹 ELCHI UNINSTALL COMPLETED! 🧹                      ║${C_RST}\n"
printf "${C_OK}╚═══════════════════════════════════════════════════════════════════════════╝${C_RST}\n"
echo ""

info "✅ All elchi components have been removed from the system"
echo ""

# List of things that were NOT removed
warn "The following were NOT removed (for safety):"
echo "  • System network configurations (if modified)"
echo "  • Kernel parameters (sysctl values remain active until reboot)"
echo "  • Any custom routing rules created outside of elchi"
echo ""

# Reboot recommendation
warn "⚠️  Recommendation: Reboot the system to ensure all changes are fully reverted"
echo ""
info "To reinstall elchi, run: ./elchi-install.sh"
echo ""