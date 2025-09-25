package services

import (
	"context"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-client/internal/config"
	grpcClient "github.com/CloudNativeWorks/elchi-client/internal/grpc"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/ping"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

const (
	HeartbeatInterval = 5 * time.Minute
	PingTimeout       = 10 * time.Second
)

// HeartbeatService manages periodic ping requests to the controller
type HeartbeatService struct {
	logger     *logger.Logger
	pingClient *ping.Client
	grpcConn   *grpcClient.Client // Own gRPC connection
	config     *config.Config
	clientID   string
	
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.RWMutex
	running    bool
}

// NewHeartbeatService creates a new heartbeat service
func NewHeartbeatService(baseLogger *logger.Logger, cfg *config.Config) *HeartbeatService {
	return &HeartbeatService{
		logger:     baseLogger,
		pingClient: ping.NewClient(baseLogger, nil, ""),
		config:     cfg,
	}
}

// Initialize creates dedicated gRPC connection and sets client ID
func (h *HeartbeatService) Initialize(clientID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	
	h.clientID = clientID
	
	// Create dedicated gRPC connection for heartbeat
	h.logger.Info("Creating dedicated gRPC connection for heartbeat")
	grpcConn, err := grpcClient.NewClient(h.config)
	if err != nil {
		return err
	}
	
	// Set client ID for metadata
	grpcConn.SetClientID(clientID)
	
	// Connect to server
	ctx := context.Background()
	if err := grpcConn.Connect(ctx); err != nil {
		return err
	}
	
	h.grpcConn = grpcConn
	h.pingClient.UpdateConnection(grpcConn.GetConnection(), clientID)
	
	h.logger.WithFields(logger.Fields{
		"client_id": clientID,
		"connected": true,
	}).Info("Heartbeat service initialized with dedicated connection")
	
	// Start connection monitoring for heartbeat
	go h.monitorConnection()
	
	return nil
}

// Start begins the heartbeat service
func (h *HeartbeatService) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	
	if h.running {
		h.logger.Warn("Heartbeat service is already running")
		return nil
	}
	
	if !h.pingClient.IsReady() {
		h.logger.Error("Cannot start heartbeat service: ping client not ready")
		return nil // Don't return error, wait for connection to be set
	}
	
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.running = true
	
	h.wg.Add(1)
	go h.run()
	
	h.logger.WithFields(logger.Fields{
		"interval": HeartbeatInterval.String(),
		"timeout":  PingTimeout.String(),
	}).Info("Heartbeat service started")
	
	return nil
}

// Stop gracefully stops the heartbeat service
func (h *HeartbeatService) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	
	if !h.running {
		h.logger.Debug("Heartbeat service is not running")
		return
	}
	
	h.logger.Info("Stopping heartbeat service")
	h.cancel()
	h.running = false
	
	// Wait for goroutine to finish with timeout
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		h.logger.Info("Heartbeat service stopped gracefully")
	case <-time.After(5 * time.Second):
		h.logger.Warn("Heartbeat service stop timed out")
	}
	
	// Close dedicated gRPC connection
	if h.grpcConn != nil {
		h.logger.Info("Closing heartbeat gRPC connection")
		if err := h.grpcConn.Close(); err != nil {
			h.logger.Errorf("Failed to close heartbeat connection: %v", err)
		}
		h.grpcConn = nil
	}
}

// IsRunning returns whether the heartbeat service is running
func (h *HeartbeatService) IsRunning() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.running
}

// SendPing sends a single ping request (can be used by other components)
func (h *HeartbeatService) SendPing() (*client.PingResponse, error) {
	h.mu.RLock()
	pingClient := h.pingClient
	h.mu.RUnlock()
	
	return pingClient.SendPingWithTimeout(PingTimeout)
}

// run is the main heartbeat loop
func (h *HeartbeatService) run() {
	defer h.wg.Done()
	
	// Send initial ping immediately
	h.SendPing()
	
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-h.ctx.Done():
			h.logger.Debug("Heartbeat service context cancelled")
			return
			
		case <-ticker.C:
			h.mu.RLock()
			pingClient := h.pingClient
			h.mu.RUnlock()
			
			if !pingClient.IsReady() {
				h.logger.Debug("Skipping heartbeat: ping client not ready")
				continue
			}
			
			h.SendPing()
		}
	}
}

// monitorConnection monitors the heartbeat connection and reconnects if needed
func (h *HeartbeatService) monitorConnection() {
	h.logger.Info("Heartbeat connection monitoring started")
	defer h.logger.Info("Heartbeat connection monitoring stopped")
	
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()
	
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.mu.RLock()
			grpcConn := h.grpcConn
			clientID := h.clientID
			config := h.config
			h.mu.RUnlock()
			
			if grpcConn == nil {
				continue
			}
			
			// Check connection state
			state := grpcConn.GetConnection().GetState()
			switch state.String() {
			case "TRANSIENT_FAILURE", "SHUTDOWN":
				h.logger.WithFields(logger.Fields{
					"state": state.String(),
				}).Warn("Heartbeat connection lost, attempting reconnect")
				
				// Reconnect
				if err := h.reconnectHeartbeat(clientID, config); err != nil {
					h.logger.Errorf("Failed to reconnect heartbeat: %v", err)
				}
				
			case "READY":
				// Connection is healthy, do nothing
			default:
				h.logger.WithFields(logger.Fields{
					"state": state.String(),
				}).Debug("Heartbeat connection state")
			}
		}
	}
}

// reconnectHeartbeat attempts to reconnect the heartbeat connection
func (h *HeartbeatService) reconnectHeartbeat(clientID string, config *config.Config) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	
	h.logger.Info("Reconnecting heartbeat service")
	
	// Close old connection
	if h.grpcConn != nil {
		h.grpcConn.Close()
		h.grpcConn = nil
	}
	
	// Create new connection
	grpcConn, err := grpcClient.NewClient(config)
	if err != nil {
		return err
	}
	
	// Set client ID and connect
	grpcConn.SetClientID(clientID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	if err := grpcConn.Connect(ctx); err != nil {
		return err
	}
	
	// Update connections
	h.grpcConn = grpcConn
	h.pingClient.UpdateConnection(grpcConn.GetConnection(), clientID)
	
	h.logger.Info("Heartbeat service reconnected successfully")
	return nil
}

// GetStats returns heartbeat service statistics
func (h *HeartbeatService) GetStats() map[string]interface{} {
	h.mu.RLock()
	defer h.mu.RUnlock()
	
	connectionState := "disconnected"
	if h.grpcConn != nil && h.grpcConn.GetConnection() != nil {
		connectionState = h.grpcConn.GetConnection().GetState().String()
	}
	
	return map[string]interface{}{
		"running":           h.running,
		"ping_ready":        h.pingClient.IsReady(),
		"connection_state":  connectionState,
		"interval_minutes":  HeartbeatInterval.Minutes(),
		"timeout_seconds":   PingTimeout.Seconds(),
	}
}