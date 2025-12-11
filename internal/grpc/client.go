package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-client/internal/config"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

const (
	initialBackoff = 2 * time.Second
	maxBackoff     = 1 * time.Minute
)

// IPv4 connections specific dialer
func ipv4Dialer(ctx context.Context, addr string) (net.Conn, error) {
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "tcp4", addr)
}

// Client represents a GRPC client
type Client struct {
	conn     *grpc.ClientConn
	config   *config.Config
	logger   *logger.Logger
	mu       sync.Mutex
	clientID string // Add clientID field

	// Monitor goroutine management
	monitorCtx     context.Context
	monitorCancel  context.CancelFunc
	monitorWg      sync.WaitGroup
	monitorRunning bool
}

// NewClient creates a new GRPC client
func NewClient(cfg *config.Config) (*Client, error) {
	log := logger.NewLogger("grpc")

	log.Info("Initializing ELCHI Client")
	return &Client{
		config: cfg,
		logger: log,
	}, nil
}

// SetClientID sets the client ID for metadata
func (c *Client) SetClientID(clientID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clientID = clientID
	c.logger.Debugf("Client ID set to: %s", clientID)
}

// clientIDInterceptor adds client ID to outgoing requests
func (c *Client) clientIDInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		c.mu.Lock()
		clientID := c.clientID
		c.mu.Unlock()

		if clientID != "" {
			// Add client ID to outgoing metadata
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				md = metadata.New(nil)
			} else {
				md = md.Copy()
			}
			md.Set("client-id", clientID)
			ctx = metadata.NewOutgoingContext(ctx, md)
		}

		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// streamClientIDInterceptor adds client ID to outgoing stream requests
func (c *Client) streamClientIDInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		c.mu.Lock()
		clientID := c.clientID
		c.mu.Unlock()

		if clientID != "" {
			// Add client ID to outgoing metadata
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				md = metadata.New(nil)
			} else {
				md = md.Copy()
			}
			md.Set("client-id", clientID)
			ctx = metadata.NewOutgoingContext(ctx, md)
		}

		return streamer(ctx, desc, cc, method, opts...)
	}
}

// Connect establishes a connection to the specified address
func (c *Client) Connect(ctx context.Context) error {
	return c.connectInternal(ctx, true)
}

// connectInternal is the internal connection method
// startMonitor parameter controls whether to start a new monitoring goroutine
func (c *Client) connectInternal(ctx context.Context, startMonitor bool) error {
	address := fmt.Sprintf("%s:%d", c.config.Server.Host, c.config.Server.Port)
	c.logger.WithFields(logger.Fields{
		"address": address,
		"tls":     c.config.Server.TLS,
	}).Info("Connecting to ELCHI Server")

	// Connection parameters
	params := c.getConnectionParams()
	c.logConnectionParams(params)

	// Create connection with appropriate credentials
	if err := c.createConnection(address, params); err != nil {
		return err
	}

	// Check connection state and handle result
	connectCtx, cancel := context.WithTimeout(ctx, params.connectTimeout)
	defer cancel()

	success, err := c.checkConnectionState(connectCtx, address)
	if err != nil {
		return err
	}

	if success {
		if startMonitor {
			// Connection successful, start monitor (safely replacing any existing one)
			c.startMonitor(ctx)
		}
		return nil
	}

	// Connection not ready, clean up and return error
	if c.conn != nil {
		c.conn.Close()
	}
	return fmt.Errorf("connection not ready")
}

// ConnectionParams holds all parameters needed for establishing a connection
type connectionParams struct {
	connectTimeout   time.Duration
	keepaliveTime    time.Duration
	keepaliveTimeout time.Duration
}

// getConnectionParams returns connection parameters
func (c *Client) getConnectionParams() *connectionParams {
	return &connectionParams{
		connectTimeout:   30 * time.Second,
		keepaliveTime:    1 * time.Minute,
		keepaliveTimeout: 20 * time.Second,
	}
}

// logConnectionParams logs connection parameters for debugging
func (c *Client) logConnectionParams(params *connectionParams) {
	c.logger.WithFields(logger.Fields{
		"timeout":           params.connectTimeout.String(),
		"keepalive_time":    params.keepaliveTime.String(),
		"keepalive_timeout": params.keepaliveTimeout.String(),
		"max_retries":       5,
	}).Debug("Connection parameters")
}

// createConnection creates a new gRPC connection with the appropriate credentials
func (c *Client) createConnection(address string, params *connectionParams) error {
	// Get transport credentials based on TLS setting
	transportCreds := c.getTransportCredentials()

	// Create context with timeout for connection
	ctx, cancel := context.WithTimeout(context.Background(), params.connectTimeout)
	defer cancel()

	// Create connection with blocking and timeout
	conn, err := grpc.DialContext(
		ctx,
		address,
		transportCreds,
		grpc.WithContextDialer(ipv4Dialer),
		grpc.WithDisableServiceConfig(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                params.keepaliveTime,
			Timeout:             params.keepaliveTimeout,
			PermitWithoutStream: false,
		}),
		grpc.WithAuthority(c.config.Server.Host),
		grpc.WithDefaultCallOptions(
			grpc.WaitForReady(true),
		),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  initialBackoff,
				Multiplier: 2.0,
				Jitter:     0.2,
				MaxDelay:   maxBackoff,
			},
			MinConnectTimeout: params.connectTimeout,
		}),
		// Add client ID interceptors
		grpc.WithUnaryInterceptor(c.clientIDInterceptor()),
		grpc.WithStreamInterceptor(c.streamClientIDInterceptor()),
	)

	if err != nil {
		c.logger.WithFields(logger.Fields{
			"error":            err.Error(),
			"address":          address,
			"tls":              c.config.Server.TLS,
			"server_host":      c.config.Server.Host,
			"server_port":      c.config.Server.Port,
			"connect_timeout":  params.connectTimeout.String(),
			"keepalive_time":   params.keepaliveTime.String(),
		}).Error("failed to create gRPC connection - check TLS/ALPN configuration")
		return fmt.Errorf("connection error: %v", err)
	}

	// Wait for connection to be ready
	state := conn.GetState()
	c.logger.WithFields(logger.Fields{
		"state": state.String(),
	}).Debug("checking initial connection state")

	if state == connectivity.TransientFailure || state == connectivity.Shutdown {
		c.logger.WithFields(logger.Fields{
			"state":      state.String(),
			"address":    address,
			"tls":        c.config.Server.TLS,
			"suggestion": "Check server availability, TLS/ALPN configuration, or network connectivity",
		}).Error("connection failed immediately")
		conn.Close()
		return fmt.Errorf("connection in failed state: %s - possible TLS/ALPN mismatch", state.String())
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()

	if !conn.WaitForStateChange(waitCtx, state) {
		conn.Close()
		return fmt.Errorf("connection state change timeout")
	}

	// Final state check
	finalState := conn.GetState()
	if finalState != connectivity.Ready {
		c.logger.WithFields(logger.Fields{
			"final_state": finalState.String(),
			"expected":    "READY",
			"address":     address,
			"tls":         c.config.Server.TLS,
			"debug_hint":  "TLS handshake may have failed or ALPN negotiation issue",
		}).Error("connection state is not ready after waiting")
		conn.Close()
		return fmt.Errorf("connection not ready, current state: %s - likely TLS/ALPN issue", finalState.String())
	}

	c.conn = conn
	
	// Get connection info for detailed logging
	connState := conn.GetState()
	c.logger.WithFields(logger.Fields{
		"address":           address,
		"state":            "READY",
		"connection_state": connState.String(),
		"tls_enabled":      c.config.Server.TLS,
		"target":           conn.Target(),
	}).Info("GRPC connection established successfully")
	
	return nil
}

// getTransportCredentials returns the appropriate transport credentials based on TLS setting
func (c *Client) getTransportCredentials() grpc.DialOption {
	if c.config.Server.TLS {
		// Use TLS credentials without verification (insecure skip verify)
		creds := credentials.NewTLS(&tls.Config{
			ServerName:         c.config.Server.Host,
			InsecureSkipVerify: true,
		})
		c.logger.Debug("Using TLS for transport")
		return grpc.WithTransportCredentials(creds)
	}

	// Use insecure credentials (no TLS)
	c.logger.Debug("Using insecure transport (no TLS)")
	return grpc.WithTransportCredentials(insecure.NewCredentials())
}

// stopMonitor cancels the current monitoring goroutine and waits for it to finish
func (c *Client) stopMonitor() {
	c.mu.Lock()
	if c.monitorCancel != nil {
		c.monitorCancel()
	}
	running := c.monitorRunning
	c.mu.Unlock()

	if running {
		// Wait for the goroutine to finish with a timeout
		done := make(chan struct{})
		go func() {
			c.monitorWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			c.logger.Debug("monitor goroutine stopped successfully")
		case <-time.After(5 * time.Second):
			c.logger.Warn("timeout waiting for monitor goroutine to stop")
		}
	}

	c.mu.Lock()
	c.monitorRunning = false
	c.monitorCancel = nil
	c.monitorCtx = nil
	c.mu.Unlock()
}

// startMonitor starts a new monitoring goroutine, stopping any existing one first
func (c *Client) startMonitor(parentCtx context.Context) {
	// Stop any existing monitor first
	c.stopMonitor()

	c.mu.Lock()
	// Create a new context derived from parent context
	c.monitorCtx, c.monitorCancel = context.WithCancel(parentCtx)
	c.monitorRunning = true
	c.monitorWg.Add(1)
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.monitorRunning = false
			c.mu.Unlock()
			c.monitorWg.Done()
		}()
		c.monitorConnection(c.monitorCtx)
	}()
}

// monitorConnection continuously monitors the connection state and reconnects if disconnected
func (c *Client) monitorConnection(ctx context.Context) {
	c.logger.Info("connection monitoring started")
	defer c.logger.Info("connection monitoring stopped")

	retryCount := 0
	maxRetries := 5
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("connection monitoring cancelled")
			return
		case <-ticker.C:
			if c.conn == nil {
				c.logger.Error("connection lost")
				if err := c.reconnect(ctx, &retryCount); err != nil {
					if retryCount >= maxRetries {
						c.logger.Error("max retry attempts reached, stopping monitor")
						return
					}
				}
				continue
			}

			state := c.conn.GetState()
			switch state {
			case connectivity.TransientFailure, connectivity.Shutdown:
				c.logger.WithFields(logger.Fields{
					"state": state.String(),
				}).Error("connection state error")

				if err := c.reconnect(ctx, &retryCount); err != nil {
					if retryCount >= maxRetries {
						c.logger.Error("max retry attempts reached, stopping monitor")
						return
					}
				}
			case connectivity.Ready:
				retryCount = 0 // Reset retry count on successful connection
			}
		}
	}
}

// reconnect attempts to reestablish the connection with exponential backoff
func (c *Client) reconnect(ctx context.Context, retryCount *int) error {
	backoff := time.Duration(math.Min(
		float64(initialBackoff)*math.Pow(2, float64(*retryCount)),
		float64(maxBackoff),
	))

	c.logger.WithFields(logger.Fields{
		"attempt": *retryCount + 1,
		"backoff": backoff.String(),
	}).Info("attempting reconnection")

	// Close existing connection if any
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	time.Sleep(backoff)

	// Don't start a new monitor - the current one is still running and calling this
	if err := c.connectInternal(ctx, false); err != nil {
		*retryCount++
		c.logger.WithFields(logger.Fields{
			"error": err.Error(),
			"retry": *retryCount,
		}).Error("reconnection failed")
		return err
	}

	c.logger.Info("reconnection successful")
	return nil
}

// checkConnectionState verifies that the connection is in a ready state
func (c *Client) checkConnectionState(ctx context.Context, address string) (bool, error) {
	if c.conn == nil {
		return false, fmt.Errorf("connection object is nil")
	}

	state := c.conn.GetState()
	c.logger.WithFields(logger.Fields{
		"state":   state.String(),
		"address": address,
	}).Debug("checking connection state")

	switch state {
	case connectivity.Ready:
		c.logger.WithFields(logger.Fields{
			"address": address,
		}).Info("connection ready")
		return true, nil

	case connectivity.Connecting:
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		if !c.conn.WaitForStateChange(waitCtx, state) {
			return false, fmt.Errorf("connection timeout while connecting")
		}
		return c.checkConnectionState(ctx, address)

	case connectivity.TransientFailure, connectivity.Shutdown:
		return false, fmt.Errorf("connection in failed state: %s", state.String())

	default:
		return false, fmt.Errorf("unexpected connection state: %s", state.String())
	}
}

// Close closes the gRPC connection
func (c *Client) Close() error {
	// Stop the monitoring goroutine first (this acquires/releases mu internally)
	c.stopMonitor()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		c.logger.Info("connection already closed")
		return nil
	}

	c.logger.Info("closing connection")
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("failed to close connection: %v", err)
	}

	c.conn = nil
	c.logger.Info("connection closed successfully")
	return nil
}

// GetConnection returns the GRPC connection
func (c *Client) GetConnection() *grpc.ClientConn {
	return c.conn
}

// GetClientID returns the client ID
func (c *Client) GetClientID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clientID
}
