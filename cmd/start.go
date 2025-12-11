package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CloudNativeWorks/elchi-client/internal/config"
	grpcClient "github.com/CloudNativeWorks/elchi-client/internal/grpc"
	"github.com/CloudNativeWorks/elchi-client/internal/handlers"
	"github.com/CloudNativeWorks/elchi-client/internal/initializer"
	"github.com/CloudNativeWorks/elchi-client/internal/services"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/sony/gobreaker"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
)

const (
	shutdownTimeout      = 5 * time.Second
	maxRequestsPerSecond = 20
	maxBurstSize         = 50
	maxConcurrentWorkers = 10
)

// ClientSession represents a client session with the server
type ClientSession struct {
	sync.RWMutex
	grpcConn     *grpcClient.Client
	cmdClient    client.CommandServiceClient
	cmdManager   *handlers.CommandManager
	log          *logger.Logger
	sessionToken string
	clientInfo   *client.RegisterRequest
	isConnected  bool
	workerPool   chan struct{}              // Semaphore for worker pool
	rateLimiter  *rate.Limiter              // Rate limiter for requests
	breaker      *gobreaker.CircuitBreaker  // Circuit breaker for error handling
	heartbeat    *services.HeartbeatService // Heartbeat service for periodic pings
}

// SessionManager handles the lifecycle of a client session
type SessionManager struct {
	session *ClientSession
	logger  *logger.Logger
	ctx     context.Context
	cancel  context.CancelFunc
	sigChan chan os.Signal
}

var StartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the client",
	Long:  `Start the client and connect to the remote server.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		logger := logger.NewLogger("main")

		return NewSessionManager(logger).Run()
	},
}

// NewSessionManager creates a new session manager
func NewSessionManager(log *logger.Logger) *SessionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &SessionManager{
		logger:  log,
		ctx:     ctx,
		cancel:  cancel,
		sigChan: make(chan os.Signal, 1),
	}
}

// Run starts the client session
func (m *SessionManager) Run() error {
	if err := m.initialize(); err != nil {
		return fmt.Errorf("initialization failed: %v", err)
	}

	defer m.cleanup()

	// Start signal handler
	go m.handleSignals()

	return m.mainLoop()
}

// initialize sets up the session
func (m *SessionManager) initialize() error {
	init := initializer.NewInitializer()

	if err := init.PlaceHotRestarter(); err != nil {
		m.logger.Fatal("Failed to place hotrestarter.py: ", err)
	}

	if Cfg == nil {
		return fmt.Errorf("configuration not loaded")
	}

	session, err := m.createSession()
	if err != nil {
		return err
	}

	m.session = session

	signal.Notify(m.sigChan, syscall.SIGINT, syscall.SIGTERM)
	return nil
}

// cleanup performs cleanup operations
func (m *SessionManager) cleanup() {
	m.logger.Info("Cleaning up resources...")

	if m.session != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		m.session.Shutdown(shutdownCtx)
	}

	signal.Stop(m.sigChan)
	close(m.sigChan)
	m.cancel()

	m.logger.Info("Cleanup completed")
}

// handleSignals handles OS signals
func (m *SessionManager) handleSignals() {
	for {
		select {
		case sig, ok := <-m.sigChan:
			if !ok {
				// Channel closed, exit gracefully
				return
			}
			m.logger.Warnf("Received signal %s, initiating shutdown...", sig)
			m.cancel()
			return
		case <-m.ctx.Done():
			return
		}
	}
}

// mainLoop runs the main processing loop
func (m *SessionManager) mainLoop() error {
	retryCount := 0
	maxRetries := 5
	backoffDuration := time.Second

	for {
		// Context check at loop start
		select {
		case <-m.ctx.Done():
			return nil
		default:
		}

		// Connection attempt
		if err := m.session.Connect(m.ctx); err != nil {
			if m.ctx.Err() != nil {
				return nil
			}

			retryCount++
			m.logger.Warnf("Connection error (%d/%d) retrying: %v", retryCount, maxRetries, err)

			if retryCount >= maxRetries {
				m.logger.Error("Max retry attempts reached, exiting")
				return fmt.Errorf("failed to connect after %d attempts", maxRetries)
			}

			// Exponential backoff
			sleepDuration := backoffDuration * time.Duration(1<<uint(retryCount-1))
			if sleepDuration > 30*time.Second {
				sleepDuration = 30 * time.Second
			}

			m.logger.Infof("Waiting %v before next retry", sleepDuration)
			select {
			case <-m.ctx.Done():
				return nil
			case <-time.After(sleepDuration):
			}

			continue
		}

		// When connection is successful, reset retry count
		retryCount = 0

		// Create a cancellable context for this stream session
		streamCtx, streamCancel := context.WithCancel(m.ctx)
		streamErrChan := make(chan error, 1)
		streamDone := make(chan struct{})

		// Start stream management in a goroutine
		go func() {
			defer close(streamDone)
			if err := m.handleCommandStream(streamCtx); err != nil {
				select {
				case streamErrChan <- err:
				default:
				}
			}
		}()

		// Wait for stream error or context cancellation
		select {
		case <-m.ctx.Done():
			streamCancel()
			<-streamDone // Wait for goroutine to finish
			return nil

		case err := <-streamErrChan:
			m.logger.Warnf("Stream error received, reconnecting: %v", err)
			streamCancel()
			<-streamDone // Wait for goroutine to finish

			// Clean up the connection
			m.session.Lock()
			m.session.isConnected = false
			m.session.sessionToken = ""
			m.session.Unlock()

			// Context-aware sleep before reconnecting
			select {
			case <-m.ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
}

// createSession creates a new client session
func (m *SessionManager) createSession() (*ClientSession, error) {
	grpcConn, err := grpcClient.NewClient(Cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create GRPC client: %v", err)
	}

	// Circuit breaker configuration
	breakerSettings := gobreaker.Settings{
		Name:    "command-breaker",
		Timeout: 60 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= 3 && failureRatio >= 0.6
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			m.logger.Warnf("Circuit breaker state changed from %v to %v", from, to)
		},
	}

	// Get stored client ID
	clientID, err := config.GetStoredClientID()
	if err != nil {
		return nil, fmt.Errorf("failed to get client ID: %v", err)
	}

	// Validate required client configuration
	if Cfg.Client.Name == "" {
		return nil, fmt.Errorf("client name is required in configuration")
	}

	if Cfg.Client.BGP == nil {
		return nil, fmt.Errorf("client bgp capability is required in configuration (must be true or false)")
	}

	// Set default cloud value if empty
	if Cfg.Client.Cloud == "" {
		Cfg.Client.Cloud = "other"
	}

	// Detect cloud provider and get metadata
	cloudDetector := services.NewCloudDetector()
	detectedProvider := cloudDetector.DetectProvider(context.Background())
	cloudMetadata := cloudDetector.GetMetadata(context.Background(), Cfg.Client.Cloud, detectedProvider)

	// Log detected cloud information
	m.logger.Infof("Cloud detection - User defined: %s, Auto detected: %s", Cfg.Client.Cloud, detectedProvider)

	hostname, _ := os.Hostname()
	clientInfo := &client.RegisterRequest{
		ClientId:  clientID,
		Token:     Cfg.Server.Token,
		Name:      Cfg.Client.Name,
		Version:   Version,
		Hostname:  hostname,
		Os:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		ProjectId: config.ExtractProjectIDFromToken(Cfg.Server.Token),
		Bgp:       *Cfg.Client.BGP,
		Cloud:     Cfg.Client.Cloud, // User-defined cloud name from config
		Provider:  detectedProvider, // Auto-detected provider (aws, gcp, azure, openstack)
		Kernel: func() string {
			out, err := exec.Command("uname", "-r").Output()
			if err != nil {
				return runtime.GOOS
			}
			return strings.TrimSpace(string(out))
		}(),
		Metadata: cloudMetadata, // Use auto-detected cloud metadata instead of config metadata
	}

	// Add start time to metadata
	clientInfo.Metadata["startTime"] = time.Now().Format(time.RFC3339)

	// Set client ID in GRPC client for metadata
	grpcConn.SetClientID(clientID)

	// Create heartbeat service
	heartbeatService := services.NewHeartbeatService(m.logger, Cfg)

	session := &ClientSession{
		grpcConn:    grpcConn,
		cmdManager:  handlers.NewCommandManagerWithGRPC(grpcConn),
		log:         m.logger,
		clientInfo:  clientInfo,
		workerPool:  make(chan struct{}, maxConcurrentWorkers),
		rateLimiter: rate.NewLimiter(rate.Limit(maxRequestsPerSecond), maxBurstSize),
		breaker:     gobreaker.NewCircuitBreaker(breakerSettings),
		heartbeat:   heartbeatService,
	}

	// Set callback for re-registration when controller reports client is not registered
	heartbeatService.SetReregisterCallback(func() {
		session.TriggerReconnect()
	})

	return session, nil
}

// handleCommandStream manages the command stream
func (m *SessionManager) handleCommandStream(ctx context.Context) error {
	errChan := make(chan error, 1)
	streamDone := make(chan struct{})

	go func() {
		defer close(streamDone)
		m.session.StreamCommands(ctx, errChan)
	}()

	select {
	case <-ctx.Done():
		<-streamDone // Wait for StreamCommands to finish
		return ctx.Err()

	case err := <-errChan:
		<-streamDone // Wait for StreamCommands to finish

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err == nil {
			return nil
		}

		// Special handling for critical errors
		if strings.Contains(err.Error(), "ENHANCE_YOUR_CALM") {
			m.logger.Warn("Rate limit exceeded, waiting before reconnecting")
			return err
		}

		// When connection is closed
		if strings.Contains(err.Error(), "transport is closing") ||
			strings.Contains(err.Error(), "connection is closing") {
			m.logger.Info("Connection closed, reconnecting...")
			return err
		}

		m.logger.Errorf("Stream error: %v", err)
		return err

	case <-streamDone:
		// StreamCommands finished without sending error
		return fmt.Errorf("stream ended unexpectedly")
	}
}

// Connect establishes a connection to the server
func (s *ClientSession) Connect(ctx context.Context) error {
	s.Lock()
	defer s.Unlock()

	if s.isConnected {
		return nil
	}

	s.log.Info("Connecting to server...")
	if err := s.grpcConn.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to server: %v", err)
	}

	s.cmdClient = client.NewCommandServiceClient(s.grpcConn.GetConnection())

	s.log.Info("Registering on server...")
	resp, err := s.cmdClient.Register(ctx, s.clientInfo)
	if err != nil {
		s.grpcConn.Close()
		return fmt.Errorf("registration failed: %v", err)
	}

	if !resp.Success {
		s.grpcConn.Close()
		s.log.Error(resp.Error)
		os.Exit(1)
	}

	if resp.SessionToken == "" {
		s.grpcConn.Close()
		s.log.Error("invalid response from server: empty session token")
		os.Exit(1)
	}

	s.sessionToken = resp.SessionToken
	s.isConnected = true
	s.log.Infof("Registration successful! Session token: %s", s.sessionToken)
	s.log.Debugf("Server response details: %+v", resp)

	// Initialize and start heartbeat service after successful connection
	if s.heartbeat != nil {
		// Initialize heartbeat with dedicated connection
		if err := s.heartbeat.Initialize(s.clientInfo.ClientId); err != nil {
			s.log.Warnf("Failed to initialize heartbeat service: %v", err)
		} else {
			// Start heartbeat service
			if err := s.heartbeat.Start(); err != nil {
				s.log.Warnf("Failed to start heartbeat service: %v", err)
			}
		}
	}

	return nil
}

// TriggerReconnect marks the session as disconnected to trigger re-registration
func (s *ClientSession) TriggerReconnect() {
	s.Lock()
	defer s.Unlock()

	if !s.isConnected {
		return
	}

	s.log.Info("Triggering reconnect due to unregistered client detection")
	s.isConnected = false
	s.sessionToken = ""

	// Stop heartbeat service - it will be restarted after re-registration
	if s.heartbeat != nil {
		s.heartbeat.Stop()
	}

	// Close current connection to force reconnect
	if s.grpcConn != nil {
		s.grpcConn.Close()
	}
}

// StreamCommands handles the command stream from the server
func (s *ClientSession) StreamCommands(ctx context.Context, errChan chan<- error) {
	s.Lock()
	if !s.isConnected || s.sessionToken == "" {
		s.Unlock()
		errChan <- fmt.Errorf("client is not connected or session token is missing")
		return
	}
	currentSessionToken := s.sessionToken
	currentClientID := s.clientInfo.ClientId
	s.Unlock()

	// Create stream with retry mechanism (context-aware)
	var stream client.CommandService_CommandStreamClient
	var err error
	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		// Context check before each attempt
		select {
		case <-ctx.Done():
			errChan <- ctx.Err()
			return
		default:
		}

		stream, err = s.cmdClient.CommandStream(ctx)
		if err == nil {
			break
		}
		s.log.Warnf("Failed to create stream (attempt %d/%d): %v", i+1, maxRetries, err)
		if i < maxRetries-1 {
			// Context-aware sleep
			select {
			case <-ctx.Done():
				errChan <- ctx.Err()
				return
			case <-time.After(time.Duration(i+1) * time.Second):
			}
		}
	}

	if err != nil {
		errChan <- fmt.Errorf("failed to start command stream after %d attempts: %v", maxRetries, err)
		return
	}

	// Send identity information in the first message
	initialResponse := &client.CommandResponse{
		CommandId: "initial_connection",
		Success:   true,
		Identity: &client.Identity{
			ClientId:     currentClientID,
			SessionToken: currentSessionToken,
			ClientName:   s.clientInfo.Name,
		},
	}

	s.log.Debugf("Sending initial response with session token: %s", currentSessionToken)

	// Send initial response with retry (context-aware)
	for i := 0; i < maxRetries; i++ {
		if err = stream.Send(initialResponse); err == nil {
			s.log.Debugf("Initial response sent successfully (attempt %d)", i+1)
			break
		}
		s.log.Warnf("Failed to send initial response (attempt %d/%d): %v", i+1, maxRetries, err)
		if i < maxRetries-1 {
			// Context-aware sleep
			select {
			case <-ctx.Done():
				errChan <- ctx.Err()
				return
			case <-time.After(time.Duration(i+1) * time.Second):
			}
		}
	}

	if err != nil {
		errChan <- fmt.Errorf("failed to send initial response: %v", err)
		return
	}

	s.log.Infof("Stream connection started (Client ID: %s, Session: %s)", currentClientID, currentSessionToken)

	// Health monitor done channel - signals health monitor to stop
	healthDone := make(chan struct{})

	// Create a goroutine to monitor stream health
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-healthDone:
				return
			case <-ticker.C:
				s.RLock()
				isConnected := s.isConnected
				s.RUnlock()

				if !isConnected {
					s.log.Warn("Stream connection lost, triggering reconnect")
					select {
					case errChan <- fmt.Errorf("stream connection lost"):
					default:
					}
					return
				}
			}
		}
	}()

	// Start command handling - when this returns, signal health monitor to stop
	s.handleCommands(ctx, stream, errChan)
	close(healthDone)
}

// handleCommands processes incoming commands
func (s *ClientSession) handleCommands(ctx context.Context, stream client.CommandService_CommandStreamClient, errChan chan<- error) {
	s.log.Info("Starting command processing loop")
	defer s.log.Info("Command processing loop ended")

	for {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			s.log.Info("Context cancelled, stopping command processing")
			errChan <- ctx.Err()
			return
		default:
		}

		// Wait for next command
		s.log.Debug("Waiting for next command from server")
		cmd, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				s.log.Info("Stream closed by server")
				errChan <- err
				return
			}
			s.log.Error(fmt.Sprintf("Failed to receive command: %v", err))
			// Check if it's a session validation error
			if strings.Contains(err.Error(), "session validation error") {
				s.log.Error("Session validation error detected - this may be due to Kubernetes load balancing or session storage issues")
			}
			errChan <- fmt.Errorf("receive error: %v", err)
			return
		}

		// Log received command
		s.log.WithFields(logger.Fields{
			"command_id": cmd.CommandId,
			"type":       cmd.Type,
			"sub_type":   cmd.SubType,
		}).Info("Received command")

		// Validate command
		if !s.validateCommand(cmd) {
			s.log.Error("Command validation failed")
			if err := stream.Send(helper.NewErrorResponse(cmd, "Command validation failed")); err != nil {
				s.log.Error(fmt.Sprintf("Failed to send error response: %v", err))
			}
			continue
		}

		// Process command and send response
		response := s.cmdManager.HandleCommand(cmd)
		if response == nil {
			s.log.Error("Received nil response from command handler")
			if err := stream.Send(helper.NewErrorResponse(cmd, "Internal error: nil response")); err != nil {
				s.log.Error(fmt.Sprintf("Failed to send error response: %v", err))
			}
			continue
		}

		// Set response metadata
		response.CommandId = cmd.CommandId
		response.Identity = &client.Identity{
			ClientId:     cmd.Identity.ClientId,
			SessionToken: s.sessionToken,
			ClientName:   cmd.Identity.ClientName,
		}

		// Send response
		if err := stream.Send(response); err != nil {
			s.log.Error(fmt.Sprintf("Failed to send response: %v", err))
			errChan <- fmt.Errorf("send error: %v", err)
			return
		}

		s.log.WithFields(logger.Fields{
			"command_id": cmd.CommandId,
			"type":       cmd.Type,
			"success":    response.Success,
		}).Info("Response sent")
	}
}

// validateCommand validates the incoming command
func (s *ClientSession) validateCommand(cmd *client.Command) bool {
	s.RLock()
	defer s.RUnlock()

	if cmd.Identity.SessionToken == "" {
		s.log.Warn("Command session token is empty")
		return false
	}

	if cmd.Identity.SessionToken != s.sessionToken {
		s.log.Warnf("Invalid session token received. Expected: %s, Received: %s", s.sessionToken, cmd.Identity.SessionToken)
		return false
	}

	return true
}

// Shutdown performs a graceful shutdown of the session
func (s *ClientSession) Shutdown(ctx context.Context) {
	s.Lock()
	defer s.Unlock()

	if !s.isConnected {
		return
	}

	// Stop heartbeat service first
	if s.heartbeat != nil {
		s.heartbeat.Stop()
	}

	s.log.Info("Unregistering from server...")
	if s.sessionToken != "" {
		_, err := s.cmdClient.Unregister(ctx, &client.UnregisterRequest{
			Identity: &client.Identity{
				ClientId:     s.clientInfo.ClientId,
				SessionToken: s.sessionToken,
				ClientName:   s.clientInfo.Name,
			},
		})
		if err != nil {
			s.log.Errorf("Failed to unregister: %v", err)
		}
	}

	if err := s.grpcConn.Close(); err != nil {
		s.log.Errorf("Failed to close GRPC connection: %v", err)
	}

	s.isConnected = false
	s.sessionToken = ""
}

func init() {
	RootCmd.AddCommand(StartCmd)
}
