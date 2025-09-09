package bgp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

// ============================================================================
// State Manager Implementation
// ============================================================================

// StateManager implements BGP state monitoring
type StateManager struct {
	vtysh        *frr.VtyshManager
	logger       *logger.Logger
	errorHandler ErrorHandlerInterface
}

// NewStateManager creates a new state manager
func NewStateManager(vtysh *frr.VtyshManager, logger *logger.Logger) StateManagerInterface {
	return &StateManager{
		vtysh:        vtysh,
		logger:       logger,
		errorHandler: NewErrorHandler(logger),
	}
}

// ============================================================================
// BGP State Operations
// ============================================================================

// GetBgpState returns current BGP state information with new Ipv4UnicastSummary structure
func (sm *StateManager) GetBgpState() (*client.Ipv4UnicastSummary, error) {
	sm.logger.Info("Getting BGP state with new Ipv4UnicastSummary structure")

	// Parse BGP summary from JSON
	summary, err := sm.ParseBgpSummaryNew()
	if err != nil {
		return nil, sm.errorHandler.NewOperationError("parse_bgp_summary", err)
	}

	return summary, nil
}



// ResetBgpSession resets a BGP session
func (sm *StateManager) ResetBgpSession(neighborIP string) error {
	sm.logger.Info(fmt.Sprintf("Resetting BGP session for neighbor %s", neighborIP))

	cmd := fmt.Sprintf("clear bgp %s", neighborIP)
	if _, err := sm.vtysh.ExecuteCommand(cmd); err != nil {
		return sm.errorHandler.NewOperationError("reset_session", err)
	}

	return nil
}

// ClearBgpRoutes clears BGP routes based on ClearBgp parameters
func (sm *StateManager) ClearBgpRoutes(clearBgp *client.ClearBgp) error {
	if clearBgp == nil {
		return sm.errorHandler.NewValidationError("clear_bgp", nil, "ClearBgp configuration cannot be nil")
	}

	sm.logger.Info(fmt.Sprintf("Clearing BGP routes: soft=%v, direction=%s, neighbor=%s", 
		clearBgp.Soft, clearBgp.Direction, clearBgp.Neighbor))

	// Build clear command
	cmd := "clear bgp"

	// Add neighbor specification first
	if clearBgp.Neighbor != "" {
		if clearBgp.Neighbor == "*" {
			// For all neighbors, use "*"
			cmd += " *"
		} else {
			// Validate IP address format for specific neighbor
			if !sm.isValidIP(clearBgp.Neighbor) {
				return sm.errorHandler.NewValidationError("neighbor_ip", clearBgp.Neighbor, 
					fmt.Sprintf("Invalid neighbor IP address: %s", clearBgp.Neighbor))
			}
			cmd += fmt.Sprintf(" %s", clearBgp.Neighbor)
		}
	}

	// Add soft if requested (FRR syntax: clear bgp [neighbor] soft [in|out])
	if clearBgp.Soft {
		cmd += " soft"
	}

	// Add direction if specified and not "all"
	if clearBgp.Direction != "" && clearBgp.Direction != "all" {
		switch clearBgp.Direction {
		case "in":
			cmd += " in"
		case "out":
			cmd += " out"
		default:
			return sm.errorHandler.NewValidationError("direction", clearBgp.Direction, 
				fmt.Sprintf("Invalid direction: %s (must be 'all', 'in', or 'out')", clearBgp.Direction))
		}
	}

	sm.logger.Info(fmt.Sprintf("Executing command: %s", cmd))

	// Execute the clear command
	output, err := sm.vtysh.ExecuteCommand(cmd)
	if err != nil {
		sm.logger.Error(fmt.Sprintf("Clear BGP command failed: %v", err))
		return sm.errorHandler.NewOperationError("clear_bgp_routes", err)
	}

	sm.logger.Info(fmt.Sprintf("Clear BGP command executed successfully. Output: %s", output))
	return nil
}

// SoftResetBgpSession performs a soft reset of BGP session
func (sm *StateManager) SoftResetBgpSession(neighborIP string, direction string) error {
	sm.logger.Info(fmt.Sprintf("Soft resetting BGP session for neighbor %s (%s)", neighborIP, direction))

	var cmd string
	switch direction {
	case "in":
		cmd = fmt.Sprintf("clear bgp %s soft in", neighborIP)
	case "out":
		cmd = fmt.Sprintf("clear bgp %s soft out", neighborIP)
	default:
		cmd = fmt.Sprintf("clear bgp %s soft", neighborIP)
	}

	if _, err := sm.vtysh.ExecuteCommand(cmd); err != nil {
		return sm.errorHandler.NewOperationError("soft_reset_session", err)
	}

	return nil
}

// CheckBgpHealth checks BGP daemon health
func (sm *StateManager) CheckBgpHealth() (*HealthStatus, error) {
	sm.logger.Info("Checking BGP health")

	health := &HealthStatus{
		Healthy:       true,
		DaemonRunning: false,
		ConfigValid:   false,
		Issues:        make([]string, 0),
	}

	// Check if daemon is running
	if running, err := sm.vtysh.CheckProtocolRunning("bgp"); err != nil {
		health.Issues = append(health.Issues, fmt.Sprintf("Failed to check BGP daemon: %v", err))
		health.Healthy = false
	} else {
		health.DaemonRunning = running
		if !running {
			health.Issues = append(health.Issues, "BGP daemon is not running")
			health.Healthy = false
		}
	}

	// Get neighbor states via summary
	summary, err := sm.ParseBgpSummaryNew()
	if err != nil {
		health.Issues = append(health.Issues, fmt.Sprintf("Failed to get BGP summary: %v", err))
		health.Healthy = false
	} else {
		health.TotalNeighbors = len(summary.Peers)
		for peerIP, peer := range summary.Peers {
			if peer.State == "Established" || peer.PeerState == "Established" {
				health.NeighborsConnected++
			} else {
				health.Issues = append(health.Issues, fmt.Sprintf("Neighbor %s is in %s state", peerIP, peer.State))
			}
		}
	}

	// Check configuration validity
	if config, err := sm.vtysh.GetCurrentConfig("bgp"); err != nil {
		health.Issues = append(health.Issues, fmt.Sprintf("Failed to get BGP configuration: %v", err))
		health.Healthy = false
	} else {
		health.ConfigValid = !strings.Contains(config, "% Invalid") && !strings.Contains(config, "% Error")
		if !health.ConfigValid {
			health.Issues = append(health.Issues, "BGP configuration contains errors")
			health.Healthy = false
		}
	}

	return health, nil
}

// GetProtocolStatus returns BGP protocol status
func (sm *StateManager) GetProtocolStatus() (*ProtocolStatus, error) {
	sm.logger.Info("Getting BGP protocol status")

	status := &ProtocolStatus{}

	// Check if BGP is enabled
	running, err := sm.vtysh.CheckProtocolRunning("bgp")
	if err != nil {
		return nil, err
	}
	status.Enabled = running

	// Get BGP summary
	summary, err := sm.ParseBgpSummary()
	if err != nil {
		return nil, err
	}

	// Get first instance
	var instance *client.BgpSummaryInstance
	for _, inst := range summary.GetInstances() {
		instance = inst
		break
	}
	if instance == nil {
		return nil, sm.errorHandler.NewValidationError("bgp", nil, "No BGP instance found")
	}

	status.Version = "4" // BGP version 4
	status.RouterID = fmt.Sprintf("%d", instance.GetRouterId())
	status.AS = instance.GetAs()
	status.RouteCount = instance.GetRibEntries()
	status.NeighborCount = instance.GetPeerCount()

	return status, nil
}

// ParseBgpSummary parses BGP summary information
func (sm *StateManager) ParseBgpSummary() (*client.ShowBgpSummary, error) {
	sm.logger.Info("Parsing BGP summary")

	output, err := sm.vtysh.ExecuteCommand("show bgp summary json")
	if err != nil {
		return nil, sm.errorHandler.NewOperationError("get_bgp_summary", err)
	}

	var summary client.ShowBgpSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		return nil, sm.errorHandler.NewOperationError("parse_bgp_summary", err)
	}

	return &summary, nil
}

// ParseBgpNeighbors parses BGP neighbor information
func (sm *StateManager) ParseBgpNeighbors() (*client.ShowBgpNeighbors, error) {
	sm.logger.Info("Parsing BGP neighbors")

	output, err := sm.vtysh.ExecuteCommand("show bgp neighbors json")
	if err != nil {
		return nil, err
	}

	var neighborsData map[string]any
	if err := json.Unmarshal([]byte(output), &neighborsData); err != nil {
		return nil, fmt.Errorf("failed to parse BGP neighbors JSON: %v", err)
	}

	response := &client.ShowBgpNeighbors{
		Neighbors: make(map[string]*client.BgpNeighborInfo),
	}

	for peerIP, data := range neighborsData {
		if data == nil {
			sm.logger.Warn(fmt.Sprintf("Nil data for neighbor %s, skipping", peerIP))
			continue
		}

		neighborData, ok := data.(map[string]any)
		if !ok {
			sm.logger.Warn(fmt.Sprintf("Invalid data format for neighbor %s, skipping", peerIP))
			continue
		}

		info := &client.BgpNeighborInfo{}

		// Safely parse float64 fields
		if val, ok := neighborData["remoteAs"].(float64); ok {
			info.RemoteAs = uint32(val)
		}
		if val, ok := neighborData["localAs"].(float64); ok {
			info.LocalAs = uint32(val)
		}
		if val, ok := neighborData["bgpVersion"].(float64); ok {
			info.BgpVersion = uint32(val)
		}
		if val, ok := neighborData["bgpTimerLastRead"].(float64); ok {
			info.BgpTimerLastRead = uint64(val)
		}
		if val, ok := neighborData["bgpTimerLastWrite"].(float64); ok {
			info.BgpTimerLastWrite = uint64(val)
		}
		if val, ok := neighborData["bgpInUpdateElapsedTimeMsecs"].(float64); ok {
			info.BgpInUpdateElapsedTimeMsecs = uint64(val)
		}
		if val, ok := neighborData["bgpTimerConfiguredHoldTimeMsecs"].(float64); ok {
			info.BgpTimerConfiguredHoldTimeMsecs = uint64(val)
		}
		if val, ok := neighborData["bgpTimerConfiguredKeepAliveIntervalMsecs"].(float64); ok {
			info.BgpTimerConfiguredKeepAliveIntervalMsecs = uint64(val)
		}
		if val, ok := neighborData["bgpTimerHoldTimeMsecs"].(float64); ok {
			info.BgpTimerHoldTimeMsecs = uint64(val)
		}
		if val, ok := neighborData["bgpTimerKeepAliveIntervalMsecs"].(float64); ok {
			info.BgpTimerKeepAliveIntervalMsecs = uint64(val)
		}
		if val, ok := neighborData["bgpTcpMssConfigured"].(float64); ok {
			info.BgpTcpMssConfigured = uint32(val)
		}
		if val, ok := neighborData["bgpTcpMssSynced"].(float64); ok {
			info.BgpTcpMssSynced = uint32(val)
		}
		if val, ok := neighborData["connectionsEstablished"].(float64); ok {
			info.ConnectionsEstablished = uint32(val)
		}
		if val, ok := neighborData["connectionsDropped"].(float64); ok {
			info.ConnectionsDropped = uint32(val)
		}
		if val, ok := neighborData["lastResetTimerMsecs"].(float64); ok {
			info.LastResetTimerMsecs = uint64(val)
		}
		if val, ok := neighborData["lastResetCode"].(float64); ok {
			info.LastResetCode = uint32(val)
		}
		if val, ok := neighborData["portLocal"].(float64); ok {
			info.PortLocal = uint32(val)
		}
		if val, ok := neighborData["portForeign"].(float64); ok {
			info.PortForeign = uint32(val)
		}
		if val, ok := neighborData["connectRetryTimer"].(float64); ok {
			info.ConnectRetryTimer = uint32(val)
		}
		if val, ok := neighborData["nextConnectTimerDueInMsecs"].(float64); ok {
			info.NextConnectTimerDueInMsecs = uint64(val)
		}

		// Safely parse boolean fields
		if val, ok := neighborData["localAsReplaceAsDualAs"].(bool); ok {
			info.LocalAsReplaceAsDualAs = val
		}
		if val, ok := neighborData["nbrExternalLink"].(bool); ok {
			info.NbrExternalLink = val
		}
		if val, ok := neighborData["extendedOptionalParametersLength"].(bool); ok {
			info.ExtendedOptionalParametersLength = val
		}

		// Safely parse string fields
		if val, ok := neighborData["localRole"].(string); ok {
			info.LocalRole = val
		}
		if val, ok := neighborData["remoteRole"].(string); ok {
			info.RemoteRole = val
		}
		if val, ok := neighborData["nbrDesc"].(string); ok {
			info.NbrDesc = val
		}
		if val, ok := neighborData["hostname"].(string); ok {
			info.Hostname = val
		}
		if val, ok := neighborData["remoteRouterId"].(string); ok {
			info.RemoteRouterId = val
		}
		if val, ok := neighborData["localRouterId"].(string); ok {
			info.LocalRouterId = val
		}
		if val, ok := neighborData["bgpState"].(string); ok {
			info.BgpState = val
		} else {
			info.BgpState = "Unknown"
		}
		if val, ok := neighborData["lastResetDueTo"].(string); ok {
			info.LastResetDueTo = val
		}
		if val, ok := neighborData["softwareVersion"].(string); ok {
			info.SoftwareVersion = val
		}
		if val, ok := neighborData["hostLocal"].(string); ok {
			info.HostLocal = val
		}
		if val, ok := neighborData["hostForeign"].(string); ok {
			info.HostForeign = val
		}
		if val, ok := neighborData["nexthop"].(string); ok {
			info.Nexthop = val
		}
		if val, ok := neighborData["nexthopGlobal"].(string); ok {
			info.NexthopGlobal = val
		}
		if val, ok := neighborData["nexthopLocal"].(string); ok {
			info.NexthopLocal = val
		}
		if val, ok := neighborData["bgpConnection"].(string); ok {
			info.BgpConnection = val
		}
		if val, ok := neighborData["readThread"].(string); ok {
			info.ReadThread = val
		}
		if val, ok := neighborData["writeThread"].(string); ok {
			info.WriteThread = val
		}

		// Parse message statistics
		if msgStats, ok := neighborData["messageStats"].(map[string]any); ok {
			info.MessageStats = &client.BgpMessageStats{}
			if val, ok := msgStats["depthInq"].(float64); ok {
				info.MessageStats.DepthInq = uint32(val)
			}
			if val, ok := msgStats["depthOutq"].(float64); ok {
				info.MessageStats.DepthOutq = uint32(val)
			}
			if val, ok := msgStats["opensSent"].(float64); ok {
				info.MessageStats.OpensSent = uint32(val)
			}
			if val, ok := msgStats["opensRecv"].(float64); ok {
				info.MessageStats.OpensRecv = uint32(val)
			}
			if val, ok := msgStats["updatesSent"].(float64); ok {
				info.MessageStats.UpdatesSent = uint32(val)
			}
			if val, ok := msgStats["updatesRecv"].(float64); ok {
				info.MessageStats.UpdatesRecv = uint32(val)
			}
			if val, ok := msgStats["keepalivesSent"].(float64); ok {
				info.MessageStats.KeepalivesSent = uint32(val)
			}
			if val, ok := msgStats["keepalivesRecv"].(float64); ok {
				info.MessageStats.KeepalivesRecv = uint32(val)
			}
			if val, ok := msgStats["notificationsSent"].(float64); ok {
				info.MessageStats.NotificationsSent = uint32(val)
			}
			if val, ok := msgStats["notificationsRecv"].(float64); ok {
				info.MessageStats.NotificationsRecv = uint32(val)
			}
			if val, ok := msgStats["routeRefreshSent"].(float64); ok {
				info.MessageStats.RouteRefreshSent = uint32(val)
			}
			if val, ok := msgStats["routeRefreshRecv"].(float64); ok {
				info.MessageStats.RouteRefreshRecv = uint32(val)
			}
			if val, ok := msgStats["capabilitySent"].(float64); ok {
				info.MessageStats.CapabilitySent = uint32(val)
			}
			if val, ok := msgStats["capabilityRecv"].(float64); ok {
				info.MessageStats.CapabilityRecv = uint32(val)
			}
			if val, ok := msgStats["totalSent"].(float64); ok {
				info.MessageStats.TotalSent = uint32(val)
			}
			if val, ok := msgStats["totalRecv"].(float64); ok {
				info.MessageStats.TotalRecv = uint32(val)
			}
		}

		// Parse graceful restart info
		if grInfo, ok := neighborData["gracefulRestartInfo"].(map[string]any); ok {
			info.GracefulRestartInfo = &client.BgpGracefulRestartInfo{}
			if val, ok := grInfo["localGrMode"].(string); ok {
				info.GracefulRestartInfo.LocalGrMode = val
			}
			if val, ok := grInfo["remoteGrMode"].(string); ok {
				info.GracefulRestartInfo.RemoteGrMode = val
			}
			if val, ok := grInfo["rBit"].(bool); ok {
				info.GracefulRestartInfo.RBit = val
			}
			if val, ok := grInfo["nBit"].(bool); ok {
				info.GracefulRestartInfo.NBit = val
			}

			// Parse GR timers
			if timers, ok := grInfo["timers"].(map[string]any); ok {
				info.GracefulRestartInfo.Timers = &client.BgpGracefulRestartTimers{}
				if val, ok := timers["configuredRestartTimer"].(float64); ok {
					info.GracefulRestartInfo.Timers.ConfiguredRestartTimer = uint32(val)
				}
				if val, ok := timers["configuredLlgrStaleTime"].(float64); ok {
					info.GracefulRestartInfo.Timers.ConfiguredLlgrStaleTime = uint32(val)
				}
				if val, ok := timers["receivedRestartTimer"].(float64); ok {
					info.GracefulRestartInfo.Timers.ReceivedRestartTimer = uint32(val)
				}
			}
		}

		// Parse prefix stats
		if prefixStats, ok := neighborData["prefixStats"].(map[string]any); ok {
			info.PrefixStats = &client.BgpPrefixStats{}
			if val, ok := prefixStats["inboundFiltered"].(float64); ok {
				info.PrefixStats.InboundFiltered = uint32(val)
			}
			if val, ok := prefixStats["aspathLoop"].(float64); ok {
				info.PrefixStats.AspathLoop = uint32(val)
			}
			if val, ok := prefixStats["originatorLoop"].(float64); ok {
				info.PrefixStats.OriginatorLoop = uint32(val)
			}
			if val, ok := prefixStats["clusterLoop"].(float64); ok {
				info.PrefixStats.ClusterLoop = uint32(val)
			}
			if val, ok := prefixStats["invalidNextHop"].(float64); ok {
				info.PrefixStats.InvalidNextHop = uint32(val)
			}
			if val, ok := prefixStats["withdrawn"].(float64); ok {
				info.PrefixStats.Withdrawn = uint32(val)
			}
			if val, ok := prefixStats["attributesDiscarded"].(float64); ok {
				info.PrefixStats.AttributesDiscarded = uint32(val)
			}
		}

		// Parse address family info
		if addrFamInfo, ok := neighborData["addressFamilyInfo"].(map[string]any); ok {
			if ipv4Info, ok := addrFamInfo["ipv4Unicast"].(map[string]any); ok {
				info.AddressFamilyInfo = &client.BgpAddressFamilyInfo{
					Ipv4Unicast: &client.BgpIpv4UnicastInfo{},
				}
				if val, ok := ipv4Info["commAttriSentToNbr"].(string); ok {
					info.AddressFamilyInfo.Ipv4Unicast.CommAttriSentToNbr = val
				}
				if val, ok := ipv4Info["inboundEbgpRequiresPolicy"].(string); ok {
					info.AddressFamilyInfo.Ipv4Unicast.InboundEbgpRequiresPolicy = val
				}
				if val, ok := ipv4Info["outboundEbgpRequiresPolicy"].(string); ok {
					info.AddressFamilyInfo.Ipv4Unicast.OutboundEbgpRequiresPolicy = val
				}
				if val, ok := ipv4Info["acceptedPrefixCounter"].(float64); ok {
					info.AddressFamilyInfo.Ipv4Unicast.AcceptedPrefixCounter = uint32(val)
				}
				if val, ok := ipv4Info["routerAlwaysNextHop"].(bool); ok {
					info.AddressFamilyInfo.Ipv4Unicast.RouterAlwaysNextHop = val
				}
			}
		}

		response.Neighbors[peerIP] = info
	}

	return response, nil
}

// ParseBgpRoutesNew parses BGP routes with new Routes structure
func (sm *StateManager) ParseBgpRoutesNew() (*client.Routes, error) {
	sm.logger.Info("Parsing BGP routes with new Routes structure")

	routes := &client.Routes{}

	// 1. Get received routes
	receivedRoutes, err := sm.parseReceivedRoutes()
	if err != nil {
		return nil, sm.errorHandler.NewOperationError("parse_received_routes", err)
	}
	routes.Received = receivedRoutes

	// 2. Get advertised routes for all neighbors
	advertisedRoutes, err := sm.parseAdvertisedRoutes()
	if err != nil {
		sm.logger.Warn(fmt.Sprintf("Failed to parse advertised routes: %v", err))
		// Don't fail the whole operation, just return empty advertised routes
		routes.Advertised = []*client.AdvertisedRoutes{}
	} else {
		routes.Advertised = advertisedRoutes
	}

	sm.logger.Info(fmt.Sprintf("Parsed routes: Received total=%d, Advertised neighbors=%d",
		routes.Received.GetTotalRoutes(), len(routes.Advertised)))

	return routes, nil
}

// parseReceivedRoutes parses received routes using "show bgp ipv4 unicast json"
func (sm *StateManager) parseReceivedRoutes() (*client.ReceivedRoutes, error) {
	sm.logger.Info("Getting received routes")

	output, err := sm.vtysh.ExecuteCommand("show bgp ipv4 unicast json")
	if err != nil {
		return nil, fmt.Errorf("failed to get BGP routes: %v", err)
	}

	// Parse JSON response
	var jsonData map[string]any
	if err := json.Unmarshal([]byte(output), &jsonData); err != nil {
		return nil, fmt.Errorf("failed to parse BGP routes JSON: %v", err)
	}

	receivedRoutes := &client.ReceivedRoutes{
		Routes: make(map[string]*client.RouteEntry),
	}

	// Extract basic information
	if routerId, ok := jsonData["routerId"].(string); ok {
		receivedRoutes.RouterId = routerId
	}
	if totalRoutes, ok := jsonData["totalRoutes"].(float64); ok {
		receivedRoutes.TotalRoutes = uint32(totalRoutes)
	}
	if totalPaths, ok := jsonData["totalPaths"].(float64); ok {
		receivedRoutes.TotalPaths = uint32(totalPaths)
	}

	// Parse routes
	if routesData, ok := jsonData["routes"].(map[string]any); ok {
		for prefix, pathsData := range routesData {
			if pathsArray, ok := pathsData.([]any); ok {
				routeEntry := &client.RouteEntry{
					Paths: []*client.Path{},
				}

				for _, pathData := range pathsArray {
					if pathMap, ok := pathData.(map[string]any); ok {
						path := sm.parsePathData(pathMap)
						if path != nil {
							routeEntry.Paths = append(routeEntry.Paths, path)
						}
					}
				}

				if len(routeEntry.Paths) > 0 {
					receivedRoutes.Routes[prefix] = routeEntry
				}
			}
		}
	}

	sm.logger.Info(fmt.Sprintf("Parsed %d received routes with %d total paths",
		len(receivedRoutes.Routes), receivedRoutes.TotalPaths))

	return receivedRoutes, nil
}

// parseAdvertisedRoutes parses advertised routes for all neighbors
func (sm *StateManager) parseAdvertisedRoutes() ([]*client.AdvertisedRoutes, error) {
	sm.logger.Info("Getting advertised routes")

	// First get list of neighbors
	neighbors, err := sm.getNeighborIPs()
	if err != nil {
		sm.logger.Error(fmt.Sprintf("Failed to get neighbors: %v", err))
		return nil, fmt.Errorf("failed to get neighbors: %v", err)
	}

	sm.logger.Info(fmt.Sprintf("Found %d neighbors to process: %v", len(neighbors), neighbors))

	var advertisedRoutes []*client.AdvertisedRoutes

	for _, neighborIP := range neighbors {
		sm.logger.Info(fmt.Sprintf("Getting advertised routes for neighbor %s", neighborIP))

		cmd := fmt.Sprintf("show bgp ipv4 unicast neighbors %s advertised-routes json", neighborIP)
		output, err := sm.vtysh.ExecuteCommand(cmd)
		if err != nil {
			sm.logger.Error(fmt.Sprintf("Failed to get advertised routes for neighbor %s: %v", neighborIP, err))
			continue
		}

		sm.logger.Debug(fmt.Sprintf("Raw advertised output for %s: %s", neighborIP, output))

		advertisedRoute, err := sm.parseAdvertisedRouteForNeighbor(neighborIP, output)
		if err != nil {
			sm.logger.Error(fmt.Sprintf("Failed to parse advertised routes for neighbor %s: %v", neighborIP, err))
			continue
		}

		if advertisedRoute != nil {
			sm.logger.Info(fmt.Sprintf("Successfully parsed advertised routes for neighbor %s: %d routes",
				neighborIP, len(advertisedRoute.Advertised)))
			advertisedRoutes = append(advertisedRoutes, advertisedRoute)
		}
	}

	sm.logger.Info(fmt.Sprintf("Parsed advertised routes for %d neighbors", len(advertisedRoutes)))
	return advertisedRoutes, nil
}

// parsePathData parses a single path from JSON data
func (sm *StateManager) parsePathData(pathMap map[string]any) *client.Path {
	path := &client.Path{}

	// Parse basic path information
	if valid, ok := pathMap["valid"].(bool); ok {
		path.Valid = valid
	}
	if bestpath, ok := pathMap["bestpath"].(bool); ok {
		path.Bestpath = bestpath
	}
	if selectionReason, ok := pathMap["selectionReason"].(string); ok {
		path.SelectionReason = selectionReason
	}
	if pathFrom, ok := pathMap["pathFrom"].(string); ok {
		path.PathFrom = pathFrom
	}
	if prefix, ok := pathMap["prefix"].(string); ok {
		path.Prefix = prefix
	}
	if prefixLen, ok := pathMap["prefixLen"].(float64); ok {
		path.PrefixLen = uint32(prefixLen)
	}
	if network, ok := pathMap["network"].(string); ok {
		path.Network = network
	}
	if version, ok := pathMap["version"].(float64); ok {
		path.Version = uint32(version)
	}
	if metric, ok := pathMap["metric"].(float64); ok {
		path.Metric = uint32(metric)
	}
	if weight, ok := pathMap["weight"].(float64); ok {
		path.Weight = uint32(weight)
	}
	if peerId, ok := pathMap["peerId"].(string); ok {
		path.PeerId = peerId
	}
	if pathStr, ok := pathMap["path"].(string); ok {
		path.Path = pathStr
	}
	if origin, ok := pathMap["origin"].(string); ok {
		path.Origin = origin
	}

	// Parse nexthops
	if nexthopsData, ok := pathMap["nexthops"].([]any); ok {
		for _, nhData := range nexthopsData {
			if nhMap, ok := nhData.(map[string]any); ok {
				nexthop := &client.Nexthop{}

				if ip, ok := nhMap["ip"].(string); ok {
					nexthop.Ip = ip
				}
				if hostname, ok := nhMap["hostname"].(string); ok {
					nexthop.Hostname = hostname
				}
				if afi, ok := nhMap["afi"].(string); ok {
					nexthop.Afi = afi
				}
				if used, ok := nhMap["used"].(bool); ok {
					nexthop.Used = used
				}

				path.Nexthops = append(path.Nexthops, nexthop)
			}
		}
	}

	return path
}

// parseAdvertisedRouteForNeighbor parses advertised routes for a specific neighbor
func (sm *StateManager) parseAdvertisedRouteForNeighbor(neighborIP, output string) (*client.AdvertisedRoutes, error) {
	sm.logger.Debug(fmt.Sprintf("Parsing advertised routes for neighbor %s", neighborIP))

	var jsonData map[string]any
	if err := json.Unmarshal([]byte(output), &jsonData); err != nil {
		sm.logger.Error(fmt.Sprintf("Failed to parse JSON for neighbor %s: %v", neighborIP, err))
		sm.logger.Debug(fmt.Sprintf("Raw JSON: %s", output))
		return nil, fmt.Errorf("failed to parse advertised routes JSON: %v", err)
	}

	sm.logger.Debug(fmt.Sprintf("JSON keys for neighbor %s: %v", neighborIP, getMapKeys(jsonData)))

	advertisedRoutes := &client.AdvertisedRoutes{
		NeighborIp: neighborIP,
		Advertised: make(map[string]*client.AdvertisedRouteEntry),
	}

	// Parse basic information
	if routerId, ok := jsonData["bgpLocalRouterId"].(string); ok {
		advertisedRoutes.RouterId = routerId
		sm.logger.Debug(fmt.Sprintf("Found routerId: %s", routerId))
	}
	if tableVersion, ok := jsonData["bgpTableVersion"].(float64); ok {
		advertisedRoutes.BgpTableVersion = uint32(tableVersion)
		sm.logger.Debug(fmt.Sprintf("Found tableVersion: %v", tableVersion))
	}
	if localAS, ok := jsonData["localAS"].(float64); ok {
		advertisedRoutes.LocalAs = uint32(localAS)
		sm.logger.Debug(fmt.Sprintf("Found localAS: %v", localAS))
	}
	if defaultLocPrf, ok := jsonData["defaultLocPrf"].(float64); ok {
		advertisedRoutes.DefaultLocalPref = uint32(defaultLocPrf)
		sm.logger.Debug(fmt.Sprintf("Found defaultLocPrf: %v", defaultLocPrf))
	}
	if totalPrefixCounter, ok := jsonData["totalPrefixCounter"].(float64); ok {
		advertisedRoutes.TotalPrefixCount = uint32(totalPrefixCounter)
		sm.logger.Debug(fmt.Sprintf("Found totalPrefixCounter: %v", totalPrefixCounter))
	}
	if filteredPrefixCounter, ok := jsonData["filteredPrefixCounter"].(float64); ok {
		advertisedRoutes.FilteredPrefixCount = uint32(filteredPrefixCounter)
		sm.logger.Debug(fmt.Sprintf("Found filteredPrefixCounter: %v", filteredPrefixCounter))
	}

	// Parse advertised routes
	if advertisedData, ok := jsonData["advertisedRoutes"].(map[string]any); ok {
		sm.logger.Debug(fmt.Sprintf("Found advertisedRoutes with %d entries", len(advertisedData)))

		for prefix, routeData := range advertisedData {
			sm.logger.Debug(fmt.Sprintf("Processing prefix: %s", prefix))

			if routeMap, ok := routeData.(map[string]any); ok {
				routeEntry := &client.AdvertisedRouteEntry{}

				if addrPrefix, ok := routeMap["addrPrefix"].(string); ok {
					routeEntry.AddrPrefix = addrPrefix
				}
				if prefixLen, ok := routeMap["prefixLen"].(float64); ok {
					routeEntry.PrefixLen = uint32(prefixLen)
				}
				if network, ok := routeMap["network"].(string); ok {
					routeEntry.Network = network
				}
				if nextHop, ok := routeMap["nextHop"].(string); ok {
					routeEntry.NextHop = nextHop
				}
				if metric, ok := routeMap["metric"].(float64); ok {
					routeEntry.Metric = uint32(metric)
				}
				if weight, ok := routeMap["weight"].(float64); ok {
					routeEntry.Weight = uint32(weight)
				}
				if path, ok := routeMap["path"].(string); ok {
					routeEntry.Path = path
				}
				if origin, ok := routeMap["origin"].(string); ok {
					routeEntry.Origin = origin
				}
				if valid, ok := routeMap["valid"].(bool); ok {
					routeEntry.Valid = valid
				}
				if best, ok := routeMap["best"].(bool); ok {
					routeEntry.Best = best
				}

				advertisedRoutes.Advertised[prefix] = routeEntry
				sm.logger.Debug(fmt.Sprintf("Added route entry for prefix %s: network=%s, valid=%v",
					prefix, routeEntry.Network, routeEntry.Valid))
			} else {
				sm.logger.Warn(fmt.Sprintf("Route data for prefix %s is not a map", prefix))
			}
		}
	} else {
		sm.logger.Warn(fmt.Sprintf("No advertisedRoutes found in JSON for neighbor %s", neighborIP))
	}

	sm.logger.Info(fmt.Sprintf("Parsed %d advertised routes for neighbor %s",
		len(advertisedRoutes.Advertised), neighborIP))

	return advertisedRoutes, nil
}

// getMapKeys helper function to get map keys for debugging
func getMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// getNeighborIPs gets list of all neighbor IPs using multiple methods
func (sm *StateManager) getNeighborIPs() ([]string, error) {
	sm.logger.Info("Getting neighbor IPs using bgp neighbors command")

	// Method 1: Try "show bgp neighbors json" directly
	output, err := sm.vtysh.ExecuteCommand("show bgp neighbors json")
	if err != nil {
		sm.logger.Warn(fmt.Sprintf("Failed to get BGP neighbors: %v", err))
		// Method 2: Fallback to BGP summary
		return sm.getNeighborIPsFromSummary()
	}

	sm.logger.Debug(fmt.Sprintf("Raw neighbors JSON: %s", output))

	var jsonData map[string]any
	if err := json.Unmarshal([]byte(output), &jsonData); err != nil {
		sm.logger.Warn(fmt.Sprintf("Failed to parse BGP neighbors JSON: %v", err))
		// Method 2: Fallback to BGP summary
		return sm.getNeighborIPsFromSummary()
	}

	var neighborIPs []string

	// Parse neighbor IPs from the root level keys
	for neighborIP := range jsonData {
		neighborIPs = append(neighborIPs, neighborIP)
		sm.logger.Debug(fmt.Sprintf("Found neighbor: %s", neighborIP))
	}

	sm.logger.Info(fmt.Sprintf("Found %d neighbors via neighbors command: %v", len(neighborIPs), neighborIPs))
	return neighborIPs, nil
}

// getNeighborIPsFromSummary gets neighbor IPs from BGP summary (fallback method)
func (sm *StateManager) getNeighborIPsFromSummary() ([]string, error) {
	sm.logger.Info("Fallback: Getting neighbor IPs from BGP summary")

	output, err := sm.vtysh.ExecuteCommand("show bgp summary json")
	if err != nil {
		return nil, fmt.Errorf("failed to get BGP summary: %v", err)
	}

	sm.logger.Debug(fmt.Sprintf("Raw summary JSON: %s", output))

	var jsonData map[string]any
	if err := json.Unmarshal([]byte(output), &jsonData); err != nil {
		return nil, fmt.Errorf("failed to parse BGP summary JSON: %v", err)
	}

	var neighborIPs []string

	// Try different JSON structures
	// Structure 1: Check for instances
	if instances, ok := jsonData["instances"].(map[string]any); ok {
		sm.logger.Debug("Found instances in summary")
		for instanceName, instanceData := range instances {
			sm.logger.Debug(fmt.Sprintf("Processing instance: %s", instanceName))
			if instanceMap, ok := instanceData.(map[string]any); ok {
				if neighbors, ok := instanceMap["neighbors"].(map[string]any); ok {
					for neighborIP := range neighbors {
						neighborIPs = append(neighborIPs, neighborIP)
						sm.logger.Debug(fmt.Sprintf("Found neighbor in instance: %s", neighborIP))
					}
				}
			}
		}
	}

	// Structure 2: Check for direct neighbors key
	if neighbors, ok := jsonData["neighbors"].(map[string]any); ok {
		sm.logger.Debug("Found direct neighbors in summary")
		for neighborIP := range neighbors {
			neighborIPs = append(neighborIPs, neighborIP)
			sm.logger.Debug(fmt.Sprintf("Found direct neighbor: %s", neighborIP))
		}
	}

	// Structure 3: Check for AS-specific structure
	for key, value := range jsonData {
		if strings.HasPrefix(key, "as") || strings.Contains(key, "65") {
			sm.logger.Debug(fmt.Sprintf("Processing potential AS key: %s", key))
			if asData, ok := value.(map[string]any); ok {
				if neighbors, ok := asData["neighbors"].(map[string]any); ok {
					for neighborIP := range neighbors {
						neighborIPs = append(neighborIPs, neighborIP)
						sm.logger.Debug(fmt.Sprintf("Found neighbor in AS: %s", neighborIP))
					}
				}
			}
		}
	}

	sm.logger.Info(fmt.Sprintf("Found %d neighbors via summary: %v", len(neighborIPs), neighborIPs))

	// If still no neighbors found, try manual extraction
	if len(neighborIPs) == 0 {
		return sm.getNeighborIPsManual()
	}

	return neighborIPs, nil
}

// getNeighborIPsManual gets neighbor IPs by parsing text output (last resort)
func (sm *StateManager) getNeighborIPsManual() ([]string, error) {
	sm.logger.Info("Last resort: Getting neighbor IPs from text output")

	output, err := sm.vtysh.ExecuteCommand("show bgp summary")
	if err != nil {
		return nil, fmt.Errorf("failed to get BGP summary text: %v", err)
	}

	sm.logger.Debug(fmt.Sprintf("Raw summary text: %s", output))

	var neighborIPs []string
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Look for lines that start with IP addresses
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			// First field should be an IP address
			firstField := fields[0]
			if sm.isValidIP(firstField) {
				neighborIPs = append(neighborIPs, firstField)
				sm.logger.Debug(fmt.Sprintf("Found neighbor via text parsing: %s", firstField))
			}
		}
	}

	sm.logger.Info(fmt.Sprintf("Found %d neighbors via text parsing: %v", len(neighborIPs), neighborIPs))
	return neighborIPs, nil
}

// isValidIP checks if a string is a valid IP address
func (sm *StateManager) isValidIP(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}

	for _, part := range parts {
		if num, err := strconv.Atoi(part); err != nil || num < 0 || num > 255 {
			return false
		}
	}

	return true
}

// ParseBgpSummaryNew parses BGP summary with new Ipv4UnicastSummary structure
func (sm *StateManager) ParseBgpSummaryNew() (*client.Ipv4UnicastSummary, error) {
	sm.logger.Info("Parsing BGP summary with new structure")

	output, err := sm.vtysh.ExecuteCommand("show bgp summary json")
	if err != nil {
		return nil, sm.errorHandler.NewOperationError("get_bgp_summary", err)
	}

	sm.logger.Debug(fmt.Sprintf("Raw BGP summary JSON: %s", output))

	var jsonData map[string]any
	if err := json.Unmarshal([]byte(output), &jsonData); err != nil {
		return nil, sm.errorHandler.NewOperationError("parse_bgp_summary_json", err)
	}

	summary := &client.Ipv4UnicastSummary{
		Peers: make(map[string]*client.PeerSummary),
	}

	// Parse based on FRR JSON structure
	// Check for "ipv4Unicast" section first
	if ipv4Data, ok := jsonData["ipv4Unicast"].(map[string]any); ok {
		sm.logger.Debug("Found ipv4Unicast section")
		sm.parseIpv4UnicastSection(ipv4Data, summary)
	} else {
		// If no ipv4Unicast section, try parsing from root level
		sm.logger.Debug("No ipv4Unicast section found, parsing from root")
		sm.parseFromRootLevel(jsonData, summary)
	}

	sm.logger.Info(fmt.Sprintf("Parsed BGP summary: AS=%d, RouterID=%s, Peers=%d",
		summary.AsNumber, summary.RouterId, len(summary.Peers)))

	return summary, nil
}

// parseIpv4UnicastSection parses the ipv4Unicast section from BGP summary
func (sm *StateManager) parseIpv4UnicastSection(ipv4Data map[string]any, summary *client.Ipv4UnicastSummary) {
	// Parse basic IPv4 unicast information
	if routerId, ok := ipv4Data["routerId"].(string); ok {
		summary.RouterId = routerId
	}
	if as, ok := ipv4Data["as"].(float64); ok {
		summary.AsNumber = uint32(as)
	}
	if vrfId, ok := ipv4Data["vrfId"].(float64); ok {
		summary.VrfId = uint32(vrfId)
	}
	if vrfName, ok := ipv4Data["vrfName"].(string); ok {
		summary.VrfName = vrfName
	}
	if tableVersion, ok := ipv4Data["tableVersion"].(float64); ok {
		summary.TableVersion = uint64(tableVersion)
	}
	if ribCount, ok := ipv4Data["ribCount"].(float64); ok {
		summary.RibCount = uint64(ribCount)
	}
	if ribMemory, ok := ipv4Data["ribMemory"].(float64); ok {
		summary.RibMemory = uint64(ribMemory)
	}
	if peerCount, ok := ipv4Data["peerCount"].(float64); ok {
		summary.PeerCount = uint32(peerCount)
	}
	if peerMemory, ok := ipv4Data["peerMemory"].(float64); ok {
		summary.PeerMemory = uint64(peerMemory)
	}
	if peerGroupCount, ok := ipv4Data["peerGroupCount"].(float64); ok {
		summary.PeerGroupCount = uint32(peerGroupCount)
	}
	if peerGroupMemory, ok := ipv4Data["peerGroupMemory"].(float64); ok {
		summary.PeerGroupMemory = uint64(peerGroupMemory)
	}
	if failedPeers, ok := ipv4Data["failedPeers"].(float64); ok {
		summary.FailedPeers = uint32(failedPeers)
	}
	if displayedPeers, ok := ipv4Data["displayedPeers"].(float64); ok {
		summary.DisplayedPeers = uint32(displayedPeers)
	}
	if totalPeers, ok := ipv4Data["totalPeers"].(float64); ok {
		summary.TotalPeers = uint32(totalPeers)
	}
	if dynamicPeers, ok := ipv4Data["dynamicPeers"].(float64); ok {
		summary.DynamicPeers = uint32(dynamicPeers)
	}

	// Parse bestPath options
	if bestPathData, ok := ipv4Data["bestPath"].(map[string]any); ok {
		summary.BestPath = &client.BestPathOptions{}
		if multiPathRelax, ok := bestPathData["multiPathRelax"].(bool); ok {
			summary.BestPath.MultiPathRelax = multiPathRelax
		}
	}

	// Parse peers
	if peersData, ok := ipv4Data["peers"].(map[string]any); ok {
		sm.parsePeers(peersData, summary)
	}
}

// parseFromRootLevel parses BGP summary from root level when no ipv4Unicast section
func (sm *StateManager) parseFromRootLevel(jsonData map[string]any, summary *client.Ipv4UnicastSummary) {
	// Try to extract basic info from root level
	if routerId, ok := jsonData["routerId"].(string); ok {
		summary.RouterId = routerId
	}
	if localAS, ok := jsonData["localAS"].(float64); ok {
		summary.AsNumber = uint32(localAS)
	}

	// Look for peers or neighbors at root level
	if peersData, ok := jsonData["peers"].(map[string]any); ok {
		sm.parsePeers(peersData, summary)
	} else if neighborsData, ok := jsonData["neighbors"].(map[string]any); ok {
		sm.parsePeers(neighborsData, summary)
	}

	// Set default values if not found
	if summary.VrfName == "" {
		summary.VrfName = "default"
	}
}

// parsePeers parses peer information from JSON data
func (sm *StateManager) parsePeers(peersData map[string]any, summary *client.Ipv4UnicastSummary) {
	for peerIP, peerData := range peersData {
		if peerMap, ok := peerData.(map[string]any); ok {
			peer := &client.PeerSummary{}

			// Parse peer fields
			if softwareVersion, ok := peerMap["softwareVersion"].(string); ok {
				peer.SoftwareVersion = softwareVersion
			}
			if remoteAs, ok := peerMap["remoteAs"].(float64); ok {
				peer.RemoteAs = uint32(remoteAs)
			}
			if localAs, ok := peerMap["localAs"].(float64); ok {
				peer.LocalAs = uint32(localAs)
			}
			if version, ok := peerMap["version"].(float64); ok {
				peer.Version = uint32(version)
			}
			if msgRcvd, ok := peerMap["msgRcvd"].(float64); ok {
				peer.MsgRcvd = uint64(msgRcvd)
			}
			if msgSent, ok := peerMap["msgSent"].(float64); ok {
				peer.MsgSent = uint64(msgSent)
			}
			if tableVersion, ok := peerMap["tableVersion"].(float64); ok {
				peer.TableVersion = uint64(tableVersion)
			}
			if outq, ok := peerMap["outq"].(float64); ok {
				peer.Outq = uint32(outq)
			}
			if inq, ok := peerMap["inq"].(float64); ok {
				peer.Inq = uint32(inq)
			}
			if peerUptime, ok := peerMap["peerUptime"].(string); ok {
				peer.PeerUptime = peerUptime
			}
			if peerUptimeMsec, ok := peerMap["peerUptimeMsec"].(float64); ok {
				peer.PeerUptimeMsec = uint64(peerUptimeMsec)
			}
			if peerUptimeEstablishedEpoch, ok := peerMap["peerUptimeEstablishedEpoch"].(float64); ok {
				peer.PeerUptimeEstablishedEpoch = uint64(peerUptimeEstablishedEpoch)
			}
			if pfxRcd, ok := peerMap["pfxRcd"].(float64); ok {
				peer.PfxRcd = uint32(pfxRcd)
			}
			if pfxSnt, ok := peerMap["pfxSnt"].(float64); ok {
				peer.PfxSnt = uint32(pfxSnt)
			}
			if state, ok := peerMap["state"].(string); ok {
				peer.State = state
			}
			if peerState, ok := peerMap["peerState"].(string); ok {
				peer.PeerState = peerState
			}
			if connectionsEstablished, ok := peerMap["connectionsEstablished"].(float64); ok {
				peer.ConnectionsEstablished = uint32(connectionsEstablished)
			}
			if connectionsDropped, ok := peerMap["connectionsDropped"].(float64); ok {
				peer.ConnectionsDropped = uint32(connectionsDropped)
			}
			if desc, ok := peerMap["desc"].(string); ok {
				peer.Desc = desc
			}
			if idType, ok := peerMap["idType"].(string); ok {
				peer.IdType = idType
			}

			summary.Peers[peerIP] = peer
		}
	}
}
