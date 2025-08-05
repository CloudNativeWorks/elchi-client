package parsers

import (
	"net"
	"os"

	"github.com/CloudNativeWorks/elchi-proto/client"
	"gopkg.in/yaml.v3"
)

// Sistemdeki interface'in up/down durumunu döner
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

// Parses interface configuration from a map
func parseInterfaceConfig(ifaceMap map[string]any, ifname string) *client.Interface {
	ci := &client.Interface{}

	if dhcp, ok := ifaceMap["dhcp4"].(bool); ok {
		ci.Dhcp4 = dhcp
	}

	if addrs, ok := ifaceMap["addresses"].([]any); ok && len(addrs) > 0 {
		ci.Addresses = parseAddresses(addrs)
	}

	if mtu, ok := ifaceMap["mtu"].(int); ok {
		ci.Mtu = uint32(mtu)
	} else if mtu, ok := ifaceMap["mtu"].(float64); ok {
		ci.Mtu = uint32(mtu)
	}

	ci.State = getInterfaceState(ifname)

	return ci
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

// Parses a netplan interface file and returns a *client.Interfaces
func ParseNetplanInterfaceFile(path string) ([]*client.Interfaces, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	var result []*client.Interfaces
	if network, ok := parsed["network"].(map[string]any); ok {
		if ethernets, ok := network["ethernets"].(map[string]any); ok {
			for ifname, ifaceData := range ethernets {
				iface := &client.Interfaces{Ifname: ifname}
				if ifaceMap, ok := ifaceData.(map[string]any); ok {
					iface.Interface = parseInterfaceConfig(ifaceMap, ifname)
				}
				result = append(result, iface)
			}
		}
	}
	return result, nil
}
