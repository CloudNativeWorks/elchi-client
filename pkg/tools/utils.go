package tools

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
)

func GetIPv4CIDR(ipv4 string) (string, error) {
	if strings.Contains(ipv4, "/") {
		return "", fmt.Errorf("invalid IPv4 address: CIDR notation not allowed, got %s", ipv4)
	}

	ip := net.ParseIP(ipv4)
	if ip == nil {
		return "", fmt.Errorf("invalid IPv4 address: %s", ipv4)
	}

	if ip.To4() == nil {
		return "", fmt.Errorf("not an IPv4 address: %s", ipv4)
	}

	return ipv4 + "/32", nil
}

// this is for development debuging
func PrettyPrint(data any) {
	if data == nil {
		return
	}

	var jsonData any
	switch v := data.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &jsonData); err != nil {
			fmt.Println(v)
			return
		}
	default:
		jsonData = v
	}

	prettyJSON, err := json.MarshalIndent(jsonData, "", "    ")
	if err != nil {
		log.Printf("JSON marshaling error: %v", err)
	}

	fmt.Println(string(prettyJSON))
}
