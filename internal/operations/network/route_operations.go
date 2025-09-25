package network

// RemoveNetplanRouteFile removes route netplan file (legacy)
func RemoveNetplanRouteFile(ifname, dir string) error {
	return RemoveNetplanFile(GetRouteFilePath(ifname, dir))
}

// RemoveNetplanPolicyFile removes policy netplan file (legacy)
func RemoveNetplanPolicyFile(ifname, dir string) error {
	return RemoveNetplanFile(GetPolicyFilePath(ifname, dir))
}

// InterfaceTableID returns routing table ID for interface (legacy)
func InterfaceTableID(ifname string) int {
	return GetInterfaceTableID(ifname) // Delegate to netlink_helpers.go
}