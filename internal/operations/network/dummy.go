package network

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/files"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/tools"
	"github.com/vishvananda/netlink"
)

var (
	netlinkLock sync.Mutex
)

func SetupDummyInterface(filename, ifaceName, downstreamAddress string, port uint32, logger *logger.Logger) (string, string, error) {
	// Step 1: Write netplan config for persistence (restart durability)
	netplanPath, err := files.WriteDummyNetplanFile(ifaceName, downstreamAddress, port)
	if err != nil {
		return "", ifaceName, fmt.Errorf("failed to create netplan file: %w", err)
	}

	// Step 2: Apply runtime configuration with netlink (immediate activation)
	if err := SetupDummyInterfaceWithNetlink(ifaceName, downstreamAddress, logger); err != nil {
		// Cleanup netplan file on failure
		os.Remove(netplanPath)
		return "", ifaceName, fmt.Errorf("failed to setup interface with netlink: %w", err)
	}

	// Step 3: Netplan config is written for restart durability only
	// DO NOT apply netplan here - it causes network interruption
	// The interface is already active via netlink for immediate use

	logger.Debugf("Interface %s created via netlink (runtime) + netplan config written (restart durability)", ifaceName)
	return netplanPath, ifaceName, nil
}

func SetupDummyInterfaceWithNetlink(ifaceName, downstreamAddress string, logger *logger.Logger) error {
	netlinkLock.Lock()
	defer netlinkLock.Unlock()

	logger.Debugf("Setting up interface %s with IP %s", ifaceName, downstreamAddress)

	ipv4CIDR, err := tools.GetIPv4CIDR(downstreamAddress)
	if err != nil {
		return fmt.Errorf("invalid IP address format: %w", err)
	}

	ipAddr, ipNet, err := net.ParseCIDR(ipv4CIDR)
	if err != nil {
		return fmt.Errorf("invalid IP address format: %w", err)
	}

	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		if !strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("error checking interface: %w", err)
		}
		logger.Debugf("Interface %s not found, creating new one", ifaceName)

		dummy := &netlink.Dummy{
			LinkAttrs: netlink.LinkAttrs{
				Name: ifaceName,
			},
		}

		if err := netlink.LinkAdd(dummy); err != nil {
			return fmt.Errorf("failed to create dummy interface: %w", err)
		}

		link, err = netlink.LinkByName(ifaceName)
		if err != nil {
			return fmt.Errorf("failed to get newly created interface: %w", err)
		}
		logger.Debugf("Successfully created new interface %s", ifaceName)
	} else {
		logger.Debugf("Interface %s already exists, reusing it", ifaceName)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set interface up: %w", err)
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list addresses: %w", err)
	}

	// Log existing IPs on interface
	if len(addrs) > 0 {
		var existingIPs []string
		for _, addr := range addrs {
			existingIPs = append(existingIPs, addr.IP.String())
		}
		logger.Debugf("Interface %s currently has IPs: %v", ifaceName, existingIPs)
	}

	// Always ensure we have the correct IP - remove others and add target IP
	// First, remove all IPs that are NOT the target IP
	for _, addr := range addrs {
		if !addr.IP.Equal(ipAddr) {
			logger.Debugf("Removing unwanted IP %s from interface %s", addr.IP.String(), ifaceName)
			if err := netlink.AddrDel(link, &addr); err != nil {
				logger.Warnf("Failed to remove unwanted IP %s: %v", addr.IP.String(), err)
			} else {
				logger.Debugf("Successfully removed unwanted IP %s", addr.IP.String())
			}
		}
	}

	// Now add the target IP (netlink will ignore if it already exists)
	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   ipAddr,
			Mask: ipNet.Mask,
		},
	}

	if err := netlink.AddrAdd(link, addr); err != nil {
		// Check if error is "file exists" (IP already exists) - this is OK
		if strings.Contains(err.Error(), "file exists") {
			logger.Debugf("IP %s already exists on interface %s", downstreamAddress, ifaceName)
		} else {
			return fmt.Errorf("failed to add IP address: %w", err)
		}
	} else {
		logger.Debugf("Successfully added IP %s to interface %s", downstreamAddress, ifaceName)
	}

	// Verify the final state
	finalAddrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err == nil {
		var finalIPs []string
		for _, fAddr := range finalAddrs {
			finalIPs = append(finalIPs, fAddr.IP.String())
		}
		logger.Debugf("Interface %s final IP state: %v", ifaceName, finalIPs)
	}

	// Force systemd-networkd to reload its configuration without network interruption
	// This makes systemd-networkd aware of the new netplan configuration
	runner := cmdrunner.NewCommandsRunner()
	reloadCtx, reloadCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer reloadCancel()
	if err := runner.RunWithS(reloadCtx, "networkctl", "reload"); err != nil {
		logger.Warnf("Failed to reload networkctl: %v", err)
	} else {
		logger.Debugf("Successfully reloaded networkctl configuration")
	}

	return nil
}

// DeleteDummyInterface removes interface from both runtime (netlink) and persistent config (netplan)
func DeleteDummyInterface(ifaceName string, logger *logger.Logger) error {
	netlinkLock.Lock()
	defer netlinkLock.Unlock()

	// Step 1: Remove runtime interface via netlink (immediate)
	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			logger.Infof("Interface %s does not exist in runtime, nothing to delete", ifaceName)
		} else {
			return fmt.Errorf("error checking interface: %w", err)
		}
	} else {
		// Interface exists, delete it
		logger.Debugf("Interface %s found, deleting it. Current state: UP=%v", ifaceName, link.Attrs().Flags&net.FlagUp != 0)

		// First, remove all IP addresses
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err == nil && len(addrs) > 0 {
			logger.Debugf("Removing %d IP addresses from interface %s", len(addrs), ifaceName)
			for _, addr := range addrs {
				logger.Debugf("Removing IP %s from interface %s", addr.IP.String(), ifaceName)
				if err := netlink.AddrDel(link, &addr); err != nil {
					logger.Warnf("Failed to remove IP %s: %v", addr.IP.String(), err)
				}
			}
		}

		if err := netlink.LinkSetDown(link); err != nil {
			logger.Warnf("Failed to set interface down: %v", err)
		} else {
			logger.Debugf("Interface %s set down", ifaceName)
		}

		if err := netlink.LinkDel(link); err != nil {
			logger.Errorf("Failed to delete interface %s: %v", ifaceName, err)
			return fmt.Errorf("failed to delete interface: %w", err)
		}
		logger.Debugf("Runtime interface %s deleted via netlink", ifaceName)

		// Verify deletion
		_, verifyErr := netlink.LinkByName(ifaceName)
		if verifyErr != nil && strings.Contains(verifyErr.Error(), "not found") {
			logger.Debugf("Interface %s deletion verified - not found in system", ifaceName)
		} else {
			logger.Warnf("Interface %s still exists after deletion attempt!", ifaceName)
		}
	}

	// Step 2: Netplan file will be deleted separately by file cleanup process
	// DO NOT apply netplan here - it causes network interruption during undeploy
	// The interface is already removed from runtime via netlink

	return nil
}
