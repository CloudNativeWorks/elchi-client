package network

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

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
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set interface up: %w", err)
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list addresses: %w", err)
	}

	ipExists := false
	for _, addr := range addrs {
		if addr.IP.Equal(ipAddr) {
			ipExists = true
			break
		}
	}

	if !ipExists {
		for _, addr := range addrs {
			if err := netlink.AddrDel(link, &addr); err != nil {
				logger.Warnf("Failed to remove old IP %s: %v", addr.IP.String(), err)
			}
		}

		addr := &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   ipAddr,
				Mask: ipNet.Mask,
			},
		}

		if err := netlink.AddrAdd(link, addr); err != nil {
			return fmt.Errorf("failed to add IP address: %w", err)
		}

		logger.Debugf("Added IP %s to interface %s", downstreamAddress, ifaceName)
	} else {
		logger.Debugf("Interface %s already has IP %s", ifaceName, downstreamAddress)
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
		if err := netlink.LinkSetDown(link); err != nil {
			logger.Warnf("Failed to set interface down: %v", err)
		}

		if err := netlink.LinkDel(link); err != nil {
			return fmt.Errorf("failed to delete interface: %w", err)
		}
		logger.Debugf("Runtime interface %s deleted via netlink", ifaceName)
	}

	// Step 2: Netplan file will be deleted separately by file cleanup process
	// DO NOT apply netplan here - it causes network interruption during undeploy
	// The interface is already removed from runtime via netlink

	return nil
}
