package ping

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/grpc"
)

const (
	DefaultPingTimeout = 10 * time.Second
)

// Client represents a ping client
type Client struct {
	mu       sync.RWMutex
	logger   *logger.Logger
	grpcConn *grpc.ClientConn
	clientID string
}

// NewClient creates a new ping client
func NewClient(logger *logger.Logger, grpcConn *grpc.ClientConn, clientID string) *Client {
	return &Client{
		logger:   logger,
		grpcConn: grpcConn,
		clientID: clientID,
	}
}

// SendPingWithTimeout sends a ping request with custom timeout
func (p *Client) SendPingWithTimeout(timeout time.Duration) (*client.PingResponse, error) {
	p.mu.RLock()
	grpcConn := p.grpcConn
	clientID := p.clientID
	p.mu.RUnlock()

	if grpcConn == nil {
		return nil, fmt.Errorf("no gRPC connection available")
	}

	if clientID == "" {
		return nil, fmt.Errorf("no client ID configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	pingClient := client.NewCommandServiceClient(grpcConn)
	req := &client.PingRequest{
		Timestamp: time.Now().Unix(),
		ClientId:  clientID,
	}

	p.logger.WithFields(logger.Fields{
		"client_id": clientID,
		"timestamp": req.Timestamp,
		"timeout":   timeout.String(),
	}).Debug("Sending ping request")

	resp, err := pingClient.Ping(ctx, req)
	if err != nil {
		p.logger.WithFields(logger.Fields{
			"error":     err.Error(),
			"client_id": clientID,
		}).Error("Ping request failed")
		return nil, err
	}

	if resp != nil && resp.Success {
		latency := time.Now().Unix() - resp.ClientTimestamp
		p.logger.WithFields(logger.Fields{
			"success":          resp.Success,
			"server_timestamp": resp.ServerTimestamp,
			"client_timestamp": resp.ClientTimestamp,
			"latency_seconds":  latency,
		}).Debug("Ping request successful")
	} else {
		p.logger.WithFields(logger.Fields{
			"success": resp != nil && resp.Success,
		}).Warn("Ping request unsuccessful")
	}

	return resp, nil
}

// UpdateConnection updates the gRPC connection and client ID
func (p *Client) UpdateConnection(grpcConn *grpc.ClientConn, clientID string) {
	p.mu.Lock()
	p.grpcConn = grpcConn
	p.clientID = clientID
	p.mu.Unlock()

	p.logger.WithFields(logger.Fields{
		"client_id": clientID,
		"connected": grpcConn != nil,
	}).Debug("Ping client connection updated")
}

// IsReady returns true if the ping client is ready to send pings
func (p *Client) IsReady() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.grpcConn != nil && p.clientID != ""
}
