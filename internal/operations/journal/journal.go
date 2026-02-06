package journal

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/coreos/go-systemd/v22/sdjournal"
)

var (
	logLineRegex = regexp.MustCompile(`^\[([^]]+)]\[([^]]+)]\[([^]]+)]\[([^]]+)] \[([^]]+)] (.*)$`)
)

// LogType represents the type of log entry
type LogType int

const (
	LogTypeUnknown LogType = iota
	LogTypeSystem
	LogTypeAccess
)

// detectLogType determines if a log line is a system log or access log
func detectLogType(line string) LogType {
	// System logs have the format: [timestamp][pid][level][component] [source] message
	if logLineRegex.MatchString(line) {
		return LogTypeSystem
	}

	// Access logs can be in two formats:
	// 1. Classic format: [timestamp] "GET /path HTTP/1.1" status ...
	if strings.HasPrefix(line, "[") && (strings.Contains(line, `"GET `) ||
		strings.Contains(line, `"POST `) || strings.Contains(line, `"PUT `) ||
		strings.Contains(line, `"DELETE `) || strings.Contains(line, `"HEAD `) ||
		strings.Contains(line, `"OPTIONS `) || strings.Contains(line, `"PATCH `)) {
		return LogTypeAccess
	}

	// 2. JSON format: {"...":"..."}
	if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
		return LogTypeAccess
	}

	return LogTypeUnknown
}

// parseSystemLogLine parses Envoy system logs
func parseSystemLogLine(line string) (timestamp, pid, level, component, source, message string) {
	matches := logLineRegex.FindStringSubmatch(line)
	if len(matches) == 7 {
		ts, err := time.Parse("2006-01-02 15:04:05.000", matches[1])
		if err == nil {
			matches[1] = ts.Format("02-01-06 15:04:05.000")
		}
		return matches[1], matches[2], matches[3], matches[4], matches[5], matches[6]
	}
	return "", "", "", "", "", ""
}

// splitAccessLogs splits multi-line access log entries by timestamp patterns
func splitAccessLogs(message string) []string {
	var entries []string

	// Pattern to match access log timestamp: [2025-09-22T15:31:55.540Z]
	accessTimestampPattern := regexp.MustCompile(`\[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z)\]`)

	// Pattern to match system log embedded in access logs: [timestamp][pid][level][component]
	systemLogPattern := regexp.MustCompile(`\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3})\]\[\d+\]\[[^\]]+\]\[[^\]]+\]`)

	// Find all access log timestamp positions
	accessMatches := accessTimestampPattern.FindAllStringIndex(message, -1)

	if len(accessMatches) == 0 {
		// No access log timestamps found
		return []string{message}
	}

	if len(accessMatches) == 1 {
		// Single access log, but check for embedded system logs
		systemMatches := systemLogPattern.FindAllStringIndex(message, -1)
		if len(systemMatches) == 0 {
			return []string{message}
		}

		// Split by system log boundaries
		var parts []string
		lastEnd := 0

		for _, sysMatch := range systemMatches {
			// Add access log part before system log
			if sysMatch[0] > lastEnd {
				part := strings.TrimSpace(message[lastEnd:sysMatch[0]])
				if part != "" && accessTimestampPattern.MatchString(part) {
					parts = append(parts, part)
				}
			}
			lastEnd = sysMatch[1]
		}

		// Add remaining part after last system log
		if lastEnd < len(message) {
			part := strings.TrimSpace(message[lastEnd:])
			if part != "" && accessTimestampPattern.MatchString(part) {
				parts = append(parts, part)
			}
		}

		if len(parts) > 0 {
			return parts
		}
		return []string{message}
	}

	// Multiple access log timestamps
	for i, match := range accessMatches {
		start := match[0]
		var end int
		if i+1 < len(accessMatches) {
			end = accessMatches[i+1][0]
		} else {
			end = len(message)
		}

		entry := strings.TrimSpace(message[start:end])
		if entry != "" {
			// Check if this entry contains embedded system logs
			systemMatches := systemLogPattern.FindAllStringIndex(entry, -1)
			if len(systemMatches) > 0 {
				// Split this entry by system log boundaries
				lastEnd := 0
				for _, sysMatch := range systemMatches {
					if sysMatch[0] > lastEnd {
						part := strings.TrimSpace(entry[lastEnd:sysMatch[0]])
						if part != "" && accessTimestampPattern.MatchString(part) {
							entries = append(entries, part)
						}
					}
					lastEnd = sysMatch[1]
				}
				// Add remaining part
				if lastEnd < len(entry) {
					part := strings.TrimSpace(entry[lastEnd:])
					if part != "" && accessTimestampPattern.MatchString(part) {
						entries = append(entries, part)
					}
				}
			} else {
				entries = append(entries, entry)
			}
		}
	}

	return entries
}

// GetLastNGeneralLogsFromSystemd reads logs from standard systemd journal for FRR and other services
func GetLastNGeneralLogsFromSystemd(serviceName string, count uint32) ([]*client.GeneralLogs, error) {
	if count > 10000 {
		count = 10000
	}

	// Open standard systemd journal (not namespace-specific)
	j, err := sdjournal.NewJournal()
	if err != nil {
		return nil, fmt.Errorf("failed to open systemd journal: %w", err)
	}
	defer j.Close()

	// Filter by systemd unit
	if err := j.AddMatch("_SYSTEMD_UNIT=" + serviceName + ".service"); err != nil {
		return nil, fmt.Errorf("failed to add systemd unit match: %w", err)
	}

	// Seek to the end (most recent entries)
	if err := j.SeekTail(); err != nil {
		return nil, fmt.Errorf("failed to seek to end of journal: %w", err)
	}

	var logs []*client.GeneralLogs
	startTime := time.Now()
	timeout := 10 * time.Second

	// Read entries backwards (from newest to oldest)
	for uint32(len(logs)) < count {
		if time.Since(startTime) > timeout {
			break
		}

		n, err := j.Previous()
		if err != nil {
			return nil, fmt.Errorf("failed to read previous journal entry: %w", err)
		}
		if n == 0 {
			break
		}

		entry, err := j.GetEntry()
		if err != nil {
			return nil, fmt.Errorf("failed to get journal entry: %w", err)
		}

		message := entry.Fields["MESSAGE"]
		if message == "" {
			continue
		}

		// Get timestamp
		timestamp := ""
		if entry.RealtimeTimestamp != 0 {
			ts := time.Unix(0, int64(entry.RealtimeTimestamp)*1000)
			timestamp = ts.Format("02-01-06 15:04:05.000")
		}

		// Get priority (log level)
		level := "INFO"
		if priority, ok := entry.Fields["PRIORITY"]; ok {
			level = priorityToLevel(priority)
		}

		// Get additional fields
		hostname := entry.Fields["_HOSTNAME"]
		pid := entry.Fields["_PID"]

		// Create metadata
		metadata := make(map[string]string)
		if hostname != "" {
			metadata["hostname"] = hostname
		}
		if pid != "" {
			metadata["pid"] = pid
		}
		if systemdUnit, ok := entry.Fields["_SYSTEMD_UNIT"]; ok {
			metadata["systemd_unit"] = systemdUnit
		}

		logs = append(logs, &client.GeneralLogs{
			Message:   message,
			Level:     level,
			Module:    serviceName,
			Timestamp: timestamp,
			Metadata:  metadata,
			File:      "",
		})
	}

	return logs, nil
}

// priorityToLevel converts systemd journal priority to log level string
func priorityToLevel(priority string) string {
	switch priority {
	case "0":
		return "EMERG"
	case "1":
		return "ALERT"
	case "2":
		return "CRIT"
	case "3":
		return "ERROR"
	case "4":
		return "WARN"
	case "5":
		return "NOTICE"
	case "6":
		return "INFO"
	case "7":
		return "DEBUG"
	default:
		return "INFO"
	}
}
