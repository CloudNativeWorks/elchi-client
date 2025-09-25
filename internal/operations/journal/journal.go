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

// parseAccessLogLine parses Envoy access logs (handles both classic and JSON formats)
func parseAccessLogLine(line string) *client.Logs {
	timestamp := ""
	message := line
	level := "INFO"
	
	// Handle classic format: [timestamp] "GET /path HTTP/1.1" status ...
	if strings.HasPrefix(line, "[") {
		if idx := strings.Index(line, "]"); idx > 0 {
			timestampStr := line[1:idx]
			
			// Try to parse UTC timestamp and convert to local time format
			if ts, err := time.Parse("2006-01-02T15:04:05.000Z", timestampStr); err == nil {
				// Convert to local time and format like system logs
				timestamp = ts.Local().Format("02-01-06 15:04:05.000")
			} else {
				// Fallback to original timestamp if parsing fails
				timestamp = timestampStr
			}
		}
		
		// Determine level based on status code in classic format
		if strings.Contains(line, " 500 ") || strings.Contains(line, " 501 ") || 
		   strings.Contains(line, " 502 ") || strings.Contains(line, " 503 ") ||
		   strings.Contains(line, " 504 ") || strings.Contains(line, " 505 ") {
			level = "ERROR"
		} else if strings.Contains(line, " 400 ") || strings.Contains(line, " 401 ") ||
		          strings.Contains(line, " 403 ") || strings.Contains(line, " 404 ") ||
		          strings.Contains(line, " 409 ") || strings.Contains(line, " 429 ") {
			level = "WARN"
		}
	} else if strings.HasPrefix(line, "{") {
		// Handle JSON format: {"start_time":"2025-09-22T17:58:11.681Z","response_code":404,...}
		
		// Extract start_time for timestamp
		if startTimeIdx := strings.Index(line, `"start_time":"`); startTimeIdx >= 0 {
			startTimeIdx += len(`"start_time":"`)
			if endIdx := strings.Index(line[startTimeIdx:], `"`); endIdx > 0 {
				timestampStr := line[startTimeIdx : startTimeIdx+endIdx]
				
				// Parse UTC timestamp and convert to local time
				if ts, err := time.Parse("2006-01-02T15:04:05.000Z", timestampStr); err == nil {
					timestamp = ts.Local().Format("02-01-06 15:04:05.000")
				}
			}
		}
		
		// Extract response_code for level determination
		if responseCodeIdx := strings.Index(line, `"response_code":`); responseCodeIdx >= 0 {
			responseCodeIdx += len(`"response_code":`)
			// Find the next comma or closing brace
			endIdx := strings.IndexAny(line[responseCodeIdx:], ",}")
			if endIdx > 0 {
				responseCodeStr := strings.TrimSpace(line[responseCodeIdx : responseCodeIdx+endIdx])
				
				// Determine level based on response code
				if strings.HasPrefix(responseCodeStr, "5") {
					level = "ERROR"
				} else if strings.HasPrefix(responseCodeStr, "4") {
					level = "WARN"
				}
			}
		}
	}
	
	return &client.Logs{
		Timestamp: timestamp,  // Local time format like system logs
		Message:   message,    // Keep the entire line as message (JSON string for JSON logs)
		Level:     level,
		Component: "ACCESS",
	}
}

// parseLogLine is a backward compatibility wrapper
func parseLogLine(line string) (timestamp, pid, level, component, source, message string) {
	return parseSystemLogLine(line)
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
	components := serviceReq.GetComponents()
	searchTerm := serviceReq.GetSearch()
	logTypeFilter := serviceReq.GetLogType()
	
	// Debug: print the received service request
	fmt.Printf("DEBUG: serviceReq.GetLogType() = %v\n", logTypeFilter)

	// For system logs, we can use journal level filtering
	if logTypeFilter == client.LogType_LOG_TYPE_SYSTEM && len(levels) > 0 {
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

	// For system logs, we can use journal component filtering
	if logTypeFilter == client.LogType_LOG_TYPE_SYSTEM && len(components) > 0 {
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
		logType := detectLogType(message)

		// Filter by log type if specified
		if logTypeFilter == client.LogType_LOG_TYPE_SYSTEM && logType != LogTypeSystem {
			continue
		}
		if logTypeFilter == client.LogType_LOG_TYPE_ACCESS && logType != LogTypeAccess {
			continue
		}

		// Process access logs  
		if logType == LogTypeAccess {
			// Check for embedded system logs in access log message
			systemLogPattern := regexp.MustCompile(`\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3})\]\[\d+\]\[[^\]]+\]\[[^\]]+\]`)
			systemMatches := systemLogPattern.FindAllStringSubmatch(message, -1)
			
			// Process embedded system logs first (only if not filtering for ACCESS only)
			if logTypeFilter != client.LogType_LOG_TYPE_ACCESS {
				for _, sysMatch := range systemMatches {
					if len(sysMatch) > 0 {
						embeddedSystemLog := sysMatch[0]
						if detectLogType(embeddedSystemLog) == LogTypeSystem {
							ts, _, level, component, _, msg := parseSystemLogLine(embeddedSystemLog)
							
							if (len(levels) == 0 || levelMap[strings.ToUpper(level)]) &&
								(len(components) == 0 || componentMap[component]) &&
								(searchTerm == "" || strings.Contains(strings.ToLower(msg), strings.ToLower(searchTerm))) {
								
								if ts != "" {
									logs = append(logs, &client.Logs{
										Timestamp: ts,
										Message:   msg,
										Level:     level,
										Component: component,
									})
									
									if uint32(len(logs)) >= serviceReq.GetCount() {
										break
									}
								}
							}
						}
					}
				}
			}
			
			// Process access logs if not filtering for system logs only
			if logTypeFilter != client.LogType_LOG_TYPE_SYSTEM {
				accessLogEntries := splitAccessLogs(message)
				for _, accessLogLine := range accessLogEntries {
					if accessLog := parseAccessLogLine(accessLogLine); accessLog != nil {
						// Apply filters for access logs
						if len(levels) > 0 && !levelMap[strings.ToUpper(accessLog.Level)] {
							continue
						}
						if searchTerm != "" && !strings.Contains(strings.ToLower(accessLog.Message), strings.ToLower(searchTerm)) {
							continue
						}
						logs = append(logs, accessLog)
						
						// Check if we've reached the count limit
						if uint32(len(logs)) >= serviceReq.GetCount() {
							break
						}
					}
				}
			}
			continue
		}

		// Process system logs (existing logic)
		if logType == LogTypeSystem {
			if currentHeader != "" {
				ts, _, level, component, _, msg := parseSystemLogLine(currentHeader)
				
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
		} else if currentHeader != "" && logType == LogTypeUnknown {
			// This is a continuation line for a system log
			currentMsgLines = append(currentMsgLines, message)
		}
	}

	// Process any remaining system log
	if currentHeader != "" && detectLogType(currentHeader) == LogTypeSystem {
		ts, _, level, component, _, msg := parseSystemLogLine(currentHeader)
		
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

	// Reverse logs to get chronological order
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
