package network

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/ping"
	"google.golang.org/grpc"
)

// Connection monitoring constants are defined in netplan.go

// ConnectionMonitor monitors controller connection during network changes
type ConnectionMonitor struct {
	logger       *logger.Logger
	controllerIP string
	pingClient   *ping.Client
}

func NewConnectionMonitor(logger *logger.Logger) *ConnectionMonitor {
	return &ConnectionMonitor{
		logger:     logger,
		pingClient: ping.NewClient(logger, nil, ""),
	}
}

// SetGRPCConnection sets the gRPC connection for ping testing
func (cm *ConnectionMonitor) SetGRPCConnection(conn *grpc.ClientConn, clientID string) {
	cm.pingClient.UpdateConnection(conn, clientID)
}

// MonitorConnectionDuringApply monitors controller connection during netplan apply
func (cm *ConnectionMonitor) MonitorConnectionDuringApply(ctx context.Context) bool {
	cm.logger.Info("Starting connection monitoring")
	cm.logger.Debugf("Monitoring context timeout: %v", ctx)

	// Detect controller IP from environment or config
	cm.logger.Debug("Detecting controller IP")
	controllerIP := cm.detectControllerIP()
	if controllerIP == "" {
		cm.logger.Warn("Could not detect controller IP, skipping connection monitoring")
		return true // Default to success if we can't determine controller
	}

	cm.controllerIP = controllerIP
	cm.logger.Info("Monitoring controller connection to: " + controllerIP)
	cm.logger.Debugf("Ping client ready: %t", cm.pingClient.IsReady())

	// Allow initial grace period for netplan apply
	cm.logger.Debugf("Waiting %d seconds grace period for netplan apply", ConnectionCheckDelay)
	time.Sleep(ConnectionCheckDelay * time.Second)

	failedChecks := 0
	successfulChecks := 0
	cm.logger.Debugf("Starting connection checks with %v interval", CheckInterval)
	ticker := time.NewTicker(CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Timeout reached - check if we had enough successful checks
			result := failedChecks < MaxFailedChecks && successfulChecks > 0
			cm.logger.Debugf("Context timeout reached - failed:%d, successful:%d, max_failed:%d",
				failedChecks, successfulChecks, MaxFailedChecks)
			if result {
				cm.logger.Info("Connection monitoring completed successfully")
			} else {
				cm.logger.Error("Connection monitoring failed: timeout without sufficient successful checks")
			}
			return result

		case <-ticker.C:
			cm.logger.Debug("Performing connectivity check")
			if cm.checkConnectivity() {
				failedChecks = 0 // Reset failed count
				successfulChecks++
				cm.logger.Debugf("Connection check successful (count: %d)", successfulChecks)

				// After 2 successful checks in a row, we can be confident
				if successfulChecks >= 2 {
					cm.logger.Info("Connection monitoring completed successfully (early success)")
					return true
				}
			} else {
				failedChecks++
				successfulChecks = 0 // Reset successful count
				cm.logger.Warnf("Connection check failed (attempt %d/%d)", failedChecks, MaxFailedChecks)

				if failedChecks >= MaxFailedChecks {
					cm.logger.Error("Maximum failed connection checks reached")
					return false
				}
			}
		}
	}
}

// checkConnectivity performs multiple connectivity checks
func (cm *ConnectionMonitor) checkConnectivity() bool {
	// Method 1: gRPC Ping (best - if available)
	if cm.pingClient.IsReady() {
		cm.logger.Debug("Trying gRPC ping check")
		if cm.grpcPingCheck() {
			cm.logger.Debug("gRPC ping check succeeded")
			return true
		}
		cm.logger.Debug("gRPC ping check failed, trying TCP")
	} else {
		cm.logger.Debug("Ping client not ready, skipping gRPC ping")
	}

	// Method 2: TCP connection check (fallback)
	cm.logger.Debug("Trying TCP connection check")
	if cm.tcpCheck() {
		cm.logger.Debug("TCP connection check succeeded")
		return true
	}
	cm.logger.Debug("TCP connection check failed, trying ICMP ping")

	// Method 3: ICMP ping (last resort)
	cm.logger.Debug("Trying ICMP ping check")
	if cm.pingCheck() {
		cm.logger.Debug("ICMP ping check succeeded")
		return true
	}
	cm.logger.Debug("All connectivity checks failed")

	return false
}

// grpcPingCheck performs gRPC ping check using the shared ping client
func (cm *ConnectionMonitor) grpcPingCheck() bool {
	resp, err := cm.pingClient.SendPingWithTimeout(2 * time.Second)
	if err != nil {
		cm.logger.Debugf("gRPC ping failed: %v", err)
		return false
	}

	if resp != nil && resp.Success {
		latency := time.Now().Unix() - resp.ClientTimestamp
		cm.logger.Debugf("gRPC ping successful (latency: %ds)", latency)
		return true
	}

	return false
}

// tcpCheck performs TCP connectivity check
func (cm *ConnectionMonitor) tcpCheck() bool {
	// Try common controller ports
	ports := []string{"443", "50051", "8080", "9090"}
	cm.logger.Debugf("Testing TCP connectivity to %s on ports: %v", cm.controllerIP, ports)

	for _, port := range ports {
		address := cm.controllerIP + ":" + port
		cm.logger.Debugf("Trying TCP connection to %s", address)
		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			conn.Close()
			cm.logger.Debugf("TCP connection successful to %s", address)
			return true
		}
		cm.logger.Debugf("TCP connection failed to %s: %v", address, err)
	}

	return false
}

// pingCheck performs ICMP ping check
func (cm *ConnectionMonitor) pingCheck() bool {
	cm.logger.Debugf("Testing ICMP ping to %s", cm.controllerIP)
	cmd := exec.Command("ping", "-c", "1", "-W", "2", cm.controllerIP)
	err := cmd.Run()
	if err == nil {
		cm.logger.Debugf("ICMP ping successful to %s", cm.controllerIP)
		return true
	}
	cm.logger.Debugf("ICMP ping failed to %s: %v", cm.controllerIP, err)
	return false
}

// detectControllerIP attempts to detect controller IP from various sources
func (cm *ConnectionMonitor) detectControllerIP() string {
	cm.logger.Debug("Starting controller IP detection")
	// Method 1: Check for gRPC connection environment variables
	cm.logger.Debug("Trying to get controller IP from environment")
	if ip := cm.getControllerFromEnv(); ip != "" {
		cm.logger.Debugf("Found controller IP from environment: %s", ip)
		return ip
	}

	// Method 2: Parse from active gRPC connections
	cm.logger.Debug("Trying to get controller IP from netstat")
	if ip := cm.getControllerFromNetstat(); ip != "" {
		cm.logger.Debugf("Found controller IP from netstat: %s", ip)
		return ip
	}

	// Method 3: Try to detect from routing table (default gateway)
	cm.logger.Debug("Trying to get controller IP from default gateway")
	if ip := cm.getDefaultGateway(); ip != "" {
		cm.logger.Debugf("Using default gateway as controller IP: %s", ip)
		return ip
	}

	cm.logger.Debug("Could not detect controller IP from any source")
	return ""
}

// getControllerFromEnv attempts to get controller IP from environment
func (cm *ConnectionMonitor) getControllerFromEnv() string {
	// Common environment variable patterns
	envVars := []string{
		"ELCHI_SERVER_HOST",
		"CONTROLLER_HOST",
		"GRPC_SERVER_HOST",
	}
	cm.logger.Debugf("Checking environment variables: %v", envVars)

	for _, envVar := range envVars {
		cm.logger.Debugf("Checking environment variable: %s", envVar)
		cmd := exec.Command("printenv", envVar)
		if output, err := cmd.Output(); err == nil {
			ip := strings.TrimSpace(string(output))
			cm.logger.Debugf("Environment variable %s = %s", envVar, ip)
			if net.ParseIP(ip) != nil {
				cm.logger.Debugf("Valid IP found in %s: %s", envVar, ip)
				return ip
			}
			cm.logger.Debugf("Invalid IP format in %s: %s", envVar, ip)
		} else {
			cm.logger.Debugf("Environment variable %s not set: %v", envVar, err)
		}
	}

	return ""
}

// getControllerFromNetstat attempts to find active controller connection
func (cm *ConnectionMonitor) getControllerFromNetstat() string {
	// Look for established gRPC connections
	cm.logger.Debug("Running netstat to find active connections")
	cmd := exec.Command("netstat", "-tn")
	output, err := cmd.Output()
	if err != nil {
		cm.logger.Debugf("netstat command failed: %v", err)
		return ""
	}

	lines := strings.Split(string(output), "\n")
	cm.logger.Debugf("Processing %d netstat lines", len(lines))
	for _, line := range lines {
		if strings.Contains(line, "ESTABLISHED") &&
			(strings.Contains(line, ":443") || strings.Contains(line, ":50051")) {
			cm.logger.Debugf("Found established gRPC connection: %s", line)

			fields := strings.Fields(line)
			if len(fields) >= 5 {
				// Parse foreign address (controller)
				foreignAddr := fields[4]
				cm.logger.Debugf("Parsing foreign address: %s", foreignAddr)
				if colonIndex := strings.LastIndex(foreignAddr, ":"); colonIndex != -1 {
					ip := foreignAddr[:colonIndex]
					cm.logger.Debugf("Extracted IP: %s", ip)
					if net.ParseIP(ip) != nil {
						cm.logger.Debugf("Valid controller IP from netstat: %s", ip)
						return ip
					}
					cm.logger.Debugf("Invalid IP format: %s", ip)
				}
			} else {
				cm.logger.Debugf("Not enough fields in line: %d", len(fields))
			}
		}
	}

	cm.logger.Debug("No controller connection found in netstat")
	return ""
}

// getDefaultGateway gets default gateway as fallback
func (cm *ConnectionMonitor) getDefaultGateway() string {
	cm.logger.Debug("Getting default gateway with ip route")
	cmd := exec.Command("ip", "route", "show", "default")
	output, err := cmd.Output()
	if err != nil {
		cm.logger.Debugf("ip route command failed: %v", err)
		return ""
	}

	routeOutput := strings.TrimSpace(string(output))
	cm.logger.Debugf("ip route output: %s", routeOutput)

	// Parse: default via 10.0.0.1 dev eth0
	fields := strings.Fields(routeOutput)
	cm.logger.Debugf("Route fields: %v", fields)
	for i, field := range fields {
		if field == "via" && i+1 < len(fields) {
			ip := fields[i+1]
			cm.logger.Debugf("Found gateway IP: %s", ip)
			if net.ParseIP(ip) != nil {
				cm.logger.Debugf("Valid default gateway IP: %s", ip)
				return ip
			}
			cm.logger.Debugf("Invalid gateway IP format: %s", ip)
		}
	}

	cm.logger.Debug("No default gateway found")
	return ""
}
