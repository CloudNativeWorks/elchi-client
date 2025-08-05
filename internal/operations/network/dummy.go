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
	netplanPath, err := files.WriteDummyNetplanFile(ifaceName, downstreamAddress, port)
	if err != nil {
		return "", ifaceName, fmt.Errorf("failed to create netplan file: %w", err)
	}

	if err := SetupDummyInterfaceWithNetlink(ifaceName, downstreamAddress, logger); err != nil {
		os.Remove(netplanPath)
		return "", ifaceName, fmt.Errorf("failed to setup interface with netlink: %w", err)
	}

	logger.Debugf("Interface %s created and configured", ifaceName)
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

func DeleteDummyInterface(ifaceName string, logger *logger.Logger) error {
	netlinkLock.Lock()
	defer netlinkLock.Unlock()

	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			logger.Infof("Interface %s does not exist, nothing to delete", ifaceName)
			return nil
		}
		return fmt.Errorf("error checking interface: %w", err)
	}

	if err := netlink.LinkSetDown(link); err != nil {
		logger.Warnf("Failed to set interface down: %v", err)
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("failed to delete interface: %w", err)
	}

	logger.Debugf("Dummy interface %s deleted", ifaceName)
	return nil
}
