package journal

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-proto/client"
)

var (
	systemLogPatternCompiled = regexp.MustCompile(`\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3})\]\[\d+\]\[[^\]]+\]\[[^\]]+\]`)
)

// GetLastNLogs reads Envoy logs from file system instead of systemd journal
func GetLastNLogs(identifier string, serviceReq *client.RequestService) ([]*client.Logs, error) {
	logTypeFilter := serviceReq.GetLogType()

	// Determine which log files to read based on LogType
	var logPaths []string
	switch logTypeFilter {
	case client.LogType_LOG_TYPE_SYSTEM:
		// System logs (stderr) - only read _system.log
		logPaths = append(logPaths, "/var/log/elchi/"+identifier+"_system.log")
	case client.LogType_LOG_TYPE_ACCESS:
		// Access logs (stdout) - only read _access.log
		logPaths = append(logPaths, "/var/log/elchi/"+identifier+"_access.log")
	default:
		// LOG_TYPE_ALL - read both files
		logPaths = append(logPaths, "/var/log/elchi/"+identifier+"_access.log")
		logPaths = append(logPaths, "/var/log/elchi/"+identifier+"_system.log")
	}

	fmt.Printf("DEBUG: Reading logs from: %v (identifier: %s, count: %d, logType: %v)\n",
		logPaths, identifier, serviceReq.GetCount(), logTypeFilter)

	// Read lines from all specified log files
	// Prevent count overflow: cap at reasonable maximum
	maxLinesToRead := serviceReq.GetCount() * 2
	if maxLinesToRead > 10000 || maxLinesToRead < serviceReq.GetCount() { // overflow check
		maxLinesToRead = 10000
	}

	var allLines []string
	for _, logPath := range logPaths {
		lines, err := readLastNLinesFromRotatedLogs(logPath, maxLinesToRead)
		if err != nil {
			fmt.Printf("DEBUG: Failed to read log file %s: %v\n", logPath, err)
			// Continue to next file instead of failing completely
			continue
		}
		allLines = append(allLines, lines...)
	}

	if len(allLines) == 0 {
		return nil, fmt.Errorf("no log files found or all log files are empty")
	}

	fmt.Printf("DEBUG: Read %d total lines from %d log file(s)\n", len(allLines), len(logPaths))

	levels := serviceReq.GetLevels()
	components := serviceReq.GetComponents()
	searchTerm := serviceReq.GetSearch()

	levelMap := make(map[string]bool)
	for _, level := range levels {
		levelMap[strings.ToUpper(level)] = true
	}

	componentMap := make(map[string]bool)
	for _, component := range components {
		componentMap[component] = true
	}

	var logs []*client.Logs
	var currentHeader string
	var currentMsgLines []string

	// Process lines in reverse order (newest first)
	for i := len(allLines) - 1; i >= 0 && uint32(len(logs)) < serviceReq.GetCount(); i-- {
		message := strings.TrimSpace(allLines[i])

		// Skip empty lines
		if message == "" {
			continue
		}

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
			systemMatches := systemLogPatternCompiled.FindAllStringSubmatch(message, -1)

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

		// Process system logs
		if logType == LogTypeSystem {
			// First, process the previous accumulated log (if any)
			if currentHeader != "" {
				ts, _, level, component, _, msg := parseSystemLogLine(currentHeader)

				// Apply filters
				passesFilter := true
				if len(levels) > 0 && !levelMap[strings.ToUpper(level)] {
					passesFilter = false
				}
				if len(components) > 0 && !componentMap[component] {
					passesFilter = false
				}

				if passesFilter {
					var msgBuffer strings.Builder
					msgBuffer.WriteString(msg)

					if len(currentMsgLines) > 0 {
						msgBuffer.WriteByte('\n')
						// Reverse the continuation lines since we collected them in reverse order
						for i := len(currentMsgLines) - 1; i >= 0; i-- {
							msgBuffer.WriteString("    ")
							msgBuffer.WriteString(currentMsgLines[i])
							if i > 0 {
								msgBuffer.WriteByte('\n')
							}
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

			// Set new header and reset continuation lines
			currentHeader = message
			currentMsgLines = nil
		} else if logType == LogTypeUnknown {
			// Continuation line for a system log (reading in reverse, so append)
			// Even if currentHeader is empty, collect continuation lines for the next header
			currentMsgLines = append(currentMsgLines, message)
		}
	}

	// Process the last accumulated log entry
	if currentHeader != "" && uint32(len(logs)) < serviceReq.GetCount() {
		ts, _, level, component, _, msg := parseSystemLogLine(currentHeader)

		if (len(levels) == 0 || levelMap[strings.ToUpper(level)]) &&
			(len(components) == 0 || componentMap[component]) {

			var msgBuffer strings.Builder
			msgBuffer.WriteString(msg)

			if len(currentMsgLines) > 0 {
				msgBuffer.WriteByte('\n')
				// Reverse the continuation lines since we collected them in reverse order
				for i := len(currentMsgLines) - 1; i >= 0; i-- {
					msgBuffer.WriteString("    ")
					msgBuffer.WriteString(currentMsgLines[i])
					if i > 0 {
						msgBuffer.WriteByte('\n')
					}
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

	// Reverse logs to get newest first
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}

	// Limit to requested count
	if uint32(len(logs)) > serviceReq.GetCount() {
		logs = logs[:serviceReq.GetCount()]
	}

	fmt.Printf("DEBUG: Returning %d logs (filters: levels=%v, components=%v, logType=%v)\n",
		len(logs), serviceReq.GetLevels(), serviceReq.GetComponents(), serviceReq.GetLogType())

	return logs, nil
}

// parseAccessLogLine parses Envoy access logs (handles both classic and JSON formats)
func parseAccessLogLine(line string) *client.Logs {
	if line == "" {
		return nil
	}

	timestamp := ""
	message := line
	level := "INFO"

	// Handle classic format: [timestamp] "GET /path HTTP/1.1" status ...
	if strings.HasPrefix(line, "[") {
		idx := strings.Index(line, "]")
		if idx > 0 && idx < len(line) {
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
			if startTimeIdx < len(line) {
				if endIdx := strings.Index(line[startTimeIdx:], `"`); endIdx > 0 {
					endPos := startTimeIdx + endIdx
					if endPos <= len(line) {
						timestampStr := line[startTimeIdx:endPos]

						// Parse UTC timestamp and convert to local time
						if ts, err := time.Parse("2006-01-02T15:04:05.000Z", timestampStr); err == nil {
							timestamp = ts.Local().Format("02-01-06 15:04:05.000")
						}
					}
				}
			}
		}

		// Extract response_code for level determination
		if responseCodeIdx := strings.Index(line, `"response_code":`); responseCodeIdx >= 0 {
			responseCodeIdx += len(`"response_code":`)
			if responseCodeIdx < len(line) {
				// Find the next comma or closing brace
				endIdx := strings.IndexAny(line[responseCodeIdx:], ",}")
				if endIdx > 0 {
					endPos := responseCodeIdx + endIdx
					if endPos <= len(line) {
						responseCodeStr := strings.TrimSpace(line[responseCodeIdx:endPos])

						// Determine level based on response code
						if strings.HasPrefix(responseCodeStr, "5") {
							level = "ERROR"
						} else if strings.HasPrefix(responseCodeStr, "4") {
							level = "WARN"
						}
					}
				}
			}
		}
	}

	return &client.Logs{
		Timestamp: timestamp, // Local time format like system logs
		Message:   message,   // Keep the entire line as message (JSON string for JSON logs)
		Level:     level,
		Component: "ACCESS",
	}
}
