package parsers

import (
	"net"
	"os"

	"gopkg.in/yaml.v3"
)

// getInterfaceState returns the up/down state of a network interface
func getInterfaceState(ifname string) string {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return "unknown"
	}
	if iface.Flags&net.FlagUp != 0 {
		return "up"
	}
	return "down"
}

// InterfaceInfo holds parsed interface information
type InterfaceInfo struct {
	Ifname    string
	Addresses []string
	State     string
	Mtu       uint32
	Dhcp4     bool
}

// Parses interface configuration from a map
func parseInterfaceConfig(ifaceMap map[string]any, ifname string) *InterfaceInfo {
	info := &InterfaceInfo{Ifname: ifname}

	if dhcp, ok := ifaceMap["dhcp4"].(bool); ok {
		info.Dhcp4 = dhcp
	}

	if addrs, ok := ifaceMap["addresses"].([]any); ok && len(addrs) > 0 {
		info.Addresses = parseAddresses(addrs)
	}

	if mtu, ok := ifaceMap["mtu"].(int); ok {
		info.Mtu = uint32(mtu)
	} else if mtu, ok := ifaceMap["mtu"].(float64); ok {
		info.Mtu = uint32(mtu)
	}

	info.State = getInterfaceState(ifname)

	return info
}

// Parses addresses from interface configuration
func parseAddresses(addrs []any) []string {
	var addrList []string
	for _, addr := range addrs {
		if s, ok := addr.(string); ok {
			addrList = append(addrList, s)
		}
	}
	return addrList
}

// Parses a netplan interface file and returns interface information
func ParseNetplanInterfaceFile(path string) ([]*InterfaceInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	var result []*InterfaceInfo
	if network, ok := parsed["network"].(map[string]any); ok {
		if ethernets, ok := network["ethernets"].(map[string]any); ok {
			for ifname, ifaceData := range ethernets {
				if ifaceMap, ok := ifaceData.(map[string]any); ok {
					info := parseInterfaceConfig(ifaceMap, ifname)
					result = append(result, info)
				}
			}
		}
	}
	return result, nil
}
