package ping

import (
	"context"
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
	if p.grpcConn == nil {
		p.logger.Error("Cannot send ping: no gRPC connection")
		return nil, nil
	}
	
	if p.clientID == "" {
		p.logger.Error("Cannot send ping: no client ID")
		return nil, nil
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	
	pingClient := client.NewCommandServiceClient(p.grpcConn)
	req := &client.PingRequest{
		Timestamp: time.Now().Unix(),
		ClientId:  p.clientID,
	}
	
	p.logger.WithFields(logger.Fields{
		"client_id": p.clientID,
		"timestamp": req.Timestamp,
		"timeout":   timeout.String(),
	}).Debug("Sending ping request")
	
	resp, err := pingClient.Ping(ctx, req)
	if err != nil {
		p.logger.WithFields(logger.Fields{
			"error":     err.Error(),
			"client_id": p.clientID,
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
	p.grpcConn = grpcConn
	p.clientID = clientID
	
	p.logger.WithFields(logger.Fields{
		"client_id": clientID,
		"connected": grpcConn != nil,
	}).Debug("Ping client connection updated")
}

// IsReady returns true if the ping client is ready to send pings
func (p *Client) IsReady() bool {
	return p.grpcConn != nil && p.clientID != ""
}