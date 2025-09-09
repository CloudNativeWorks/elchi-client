package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/coreos/go-systemd/v22/sdjournal"
)

var logLineRegex = regexp.MustCompile(`^\[([^]]+)]\[([^]]+)]\[([^]]+)]\[([^]]+)] \[([^]]+)] (.*)$`)

func parseLogLine(line string) (timestamp, pid, level, component, source, message string) {
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

func OpenNamespace(ns string) (*sdjournal.Journal, error) {
	idBytes, err := os.ReadFile(models.MachineID)
	if err != nil {
		return nil, fmt.Errorf("machine-id not found: %w", err)
	}
	mid := strings.TrimSpace(string(idBytes))
	jdir := filepath.Join(models.JournalLogPath, mid+"."+ns)

	j, err := sdjournal.NewJournalFromDir(jdir)
	if err != nil {
		return nil, fmt.Errorf("journal not found (%s): %w", jdir, err)
	}
	return j, nil
}

func GetLastNLogs(identifier string, serviceReq *client.RequestService) ([]*client.Logs, error) {
	var logs []*client.Logs
	var currentHeader string
	var currentMsgLines []string

	reader, err := OpenNamespace(identifier)
	if err != nil {
		return nil, fmt.Errorf("journal reader not found: %v", err)
	}
	defer reader.Close()

	if err := reader.AddMatch("_NAMESPACE=" + identifier); err != nil {
		return nil, fmt.Errorf("journal matcher not found: %v", err)
	}

	levels := serviceReq.GetLevels()
	if len(levels) > 0 {
		if err := reader.AddDisjunction(); err != nil {
			return nil, fmt.Errorf("journal disjunction not found: %v", err)
		}

		var levelPatterns []string
		for _, level := range levels {
			levelUpper := strings.ToUpper(level)
			levelPatterns = append(levelPatterns, "MESSAGE=*["+levelUpper+"]*")
		}
		if err := reader.AddMatch(strings.Join(levelPatterns, "|")); err != nil {
			return nil, fmt.Errorf("journal level matcher not found: %v", err)
		}
	}

	components := serviceReq.GetComponents()
	if len(components) > 0 {
		if err := reader.AddDisjunction(); err != nil {
			return nil, fmt.Errorf("journal disjunction not found: %v", err)
		}

		var componentPatterns []string
		for _, component := range components {
			componentPatterns = append(componentPatterns, "MESSAGE=*["+component+"]*")
		}
		if err := reader.AddMatch(strings.Join(componentPatterns, "|")); err != nil {
			return nil, fmt.Errorf("journal component matcher not found: %v", err)
		}
	}

	searchTerm := serviceReq.GetSearch()

	if err := reader.SeekTail(); err != nil {
		return nil, fmt.Errorf("journal not found: %v", err)
	}

	levelMap := make(map[string]bool)
	for _, level := range levels {
		levelMap[strings.ToUpper(level)] = true
	}

	componentMap := make(map[string]bool)
	for _, component := range components {
		componentMap[component] = true
	}

	var msgBuffer strings.Builder
	startTime := time.Now()
	timeout := 10 * time.Second

	for uint32(len(logs)) < serviceReq.GetCount() {
		if time.Since(startTime) > timeout {
			break
		}

		n, err := reader.Previous()
		if err != nil {
			return nil, fmt.Errorf("journal previous: %w", err)
		}
		if n == 0 {
			break
		}

		entry, err := reader.GetEntry()
		if err != nil {
			return nil, fmt.Errorf("entry not found: %w", err)
		}

		message := entry.Fields["MESSAGE"]

		if logLineRegex.MatchString(message) {
			if currentHeader != "" {
				ts, _, level, component, _, msg := parseLogLine(currentHeader)
				
				if len(levels) > 0 && !levelMap[strings.ToUpper(level)] {
					currentHeader = message
					currentMsgLines = nil
					continue
				}
				if len(components) > 0 && !componentMap[component] {
					currentHeader = message
					currentMsgLines = nil
					continue
				}

				msgBuffer.Reset()
				msgBuffer.WriteString(msg)
				
				if len(currentMsgLines) > 0 {
					msgBuffer.WriteByte('\n')
					for i, line := range currentMsgLines {
						if i > 0 {
							msgBuffer.WriteByte('\n')
						}
						msgBuffer.WriteString("    ")
						msgBuffer.WriteString(line)
					}
				}

				fullMsg := msgBuffer.String()

				if searchTerm == "" || strings.Contains(strings.ToLower(fullMsg), strings.ToLower(searchTerm)) {
					if ts != "" {
						logs = append(logs, &client.Logs{
							Timestamp: ts,
							Message:   fullMsg,
							Level:     level,
							Component: component,
						})
					}
				}
			}
			currentHeader = message
			currentMsgLines = nil
		} else if currentHeader != "" {
			currentMsgLines = append(currentMsgLines, message)
		}
	}

	if currentHeader != "" {
		ts, _, level, component, _, msg := parseLogLine(currentHeader)
		
		if (len(levels) == 0 || levelMap[strings.ToUpper(level)]) &&
			(len(components) == 0 || componentMap[component]) {
			
			msgBuffer.Reset()
			msgBuffer.WriteString(msg)
			
			if len(currentMsgLines) > 0 {
				msgBuffer.WriteByte('\n')
				for i, line := range currentMsgLines {
					if i > 0 {
						msgBuffer.WriteByte('\n')
					}
					msgBuffer.WriteString("    ")
					msgBuffer.WriteString(line)
				}
			}

			fullMsg := msgBuffer.String()

			if searchTerm == "" || strings.Contains(strings.ToLower(fullMsg), strings.ToLower(searchTerm)) {
				if ts != "" {
					logs = append(logs, &client.Logs{
						Timestamp: ts,
						Message:   fullMsg,
						Level:     level,
						Component: component,
					})
				}
			}
		}
	}

	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}

	return logs, nil
}

// GetLastNGeneralLogsFromSystemd reads logs from standard systemd journal for FRR and other services
func GetLastNGeneralLogsFromSystemd(serviceName string, count uint32) ([]*client.GeneralLogs, error) {
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
