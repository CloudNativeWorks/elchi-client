package journal

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

func GetLastNGeneralLogs(identifier string, count uint32, log *logger.Logger) ([]*client.GeneralLogs, error) {
	if count > 10000 {
		count = 10000
	}

	logPath := "/var/log/" + identifier + ".log"

	lines, err := readLastNLinesFromRotatedLogs(logPath, count, log)
	if err != nil {
		return nil, fmt.Errorf("log file not found: %w", err)
	}

	var logs []*client.GeneralLogs
	for _, line := range lines {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		level, _ := raw["level"].(string)
		message, _ := raw["message"].(string)
		timestamp, _ := raw["timestamp"].(string)
		fileField, _ := raw["file"].(string)
		module, _ := raw["module"].(string)

		if level == "" || message == "" || timestamp == "" {
			continue
		}

		metadata := make(map[string]string)
		for k, v := range raw {
			if k == "level" || k == "message" || k == "timestamp" || k == "file" || k == "module" {
				continue
			}
			if str, ok := v.(string); ok {
				metadata[k] = str
			} else {
				metadata[k] = fmt.Sprintf("%v", v)
			}
		}

		logs = append(logs, &client.GeneralLogs{
			Message:   message,
			Level:     level,
			Module:    module,
			Timestamp: timestamp,
			Metadata:  metadata,
			File:      fileField,
		})
	}

	sort.Slice(logs, func(i, j int) bool {
		return logs[i].Timestamp < logs[j].Timestamp
	})

	return logs, nil
}

func readLastNLinesFromRotatedLogs(basePath string, n uint32, log *logger.Logger) ([]string, error) {
	// Only read the main log file (no rotated or compressed files)
	return readAllLines(basePath, n, log)
}
