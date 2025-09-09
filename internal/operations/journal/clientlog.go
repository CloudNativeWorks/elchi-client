package journal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/CloudNativeWorks/elchi-proto/client"
)

func GetLastNGeneralLogs(identifier string, count uint32) ([]*client.GeneralLogs, error) {
	logPath := "/var/log/elchi/" + identifier + ".log"

	lines, err := readLastNLinesFromRotatedLogs(logPath, count)
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

func readLastNLinesFromRotatedLogs(basePath string, n uint32) ([]string, error) {
	files := getOrderedLogFiles(basePath)

	var allLines []string
	for _, file := range files {
		var lines []string
		var err error
		if strings.HasSuffix(file, ".gz") {
			lines, err = readAllLinesGz(file, n)
		} else {
			lines, err = readAllLines(file, n)
		}
		if err != nil {
			continue
		}
		for i := len(lines) - 1; i >= 0 && uint32(len(allLines)) < n; i-- {
			allLines = append(allLines, lines[i])
		}
		if uint32(len(allLines)) >= n {
			break
		}
	}
	for i, j := 0, len(allLines)-1; i < j; i, j = i+1, j-1 {
		allLines[i], allLines[j] = allLines[j], allLines[i]
	}
	return allLines, nil
}


