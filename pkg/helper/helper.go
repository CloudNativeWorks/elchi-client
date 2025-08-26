package helper

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

func NewErrorResponse(cmd *client.Command, errMsg string) *client.CommandResponse {
	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   false,
		Error:     errMsg,
	}
}

func LoadInterfaceTableMap() (map[string]int, error) {
	m := make(map[string]int)
	f, err := os.Open(models.InterfaceTableMap)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		idStr, name := parts[0], parts[1]
		if !strings.HasPrefix(name, "elchi-if-") {
			continue
		}
		ifname := strings.TrimPrefix(name, "elchi-if-")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		m[ifname] = id
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return m, nil
}
