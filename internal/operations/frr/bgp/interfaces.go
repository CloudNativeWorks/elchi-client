package bgp

import (
	"github.com/CloudNativeWorks/elchi-proto/client"
)

// ValidationResult represents validation outcome
type ValidationResult struct {
	Valid    bool
	Errors   []*ValidationError
	Warnings []*ValidationWarning
}

// ValidationError represents a validation error
type ValidationError struct {
	Field   string
	Value   any
	Message string
	Code    string
}

// ValidationWarning represents a validation warning
type ValidationWarning struct {
	Field   string
	Value   any
	Message string
	Code    string
}

// BgpError represents a structured BGP error
type BgpError struct {
	Operation   string
	Component   string
	ErrorType   ErrorType
	Message     string
	Cause       error
	Retryable   bool
	UserMessage string
}

// ErrorType categorizes BGP errors
type ErrorType string

const (
	ErrorTypeValidation    ErrorType = "validation"
	ErrorTypeConfiguration ErrorType = "configuration"
	ErrorTypeOperation     ErrorType = "operation"
	ErrorTypeConnection    ErrorType = "connection"
	ErrorTypeProtocol      ErrorType = "protocol"
	ErrorTypeSystem        ErrorType = "system"
)

// RecoveryStrategy defines how to recover from errors
type RecoveryStrategy string

const (
	RecoveryRetry    RecoveryStrategy = "retry"
	RecoveryRollback RecoveryStrategy = "rollback"
	RecoveryManual   RecoveryStrategy = "manual"
	RecoveryIgnore   RecoveryStrategy = "ignore"
)

// HealthStatus represents BGP daemon health
type HealthStatus struct {
	Healthy            bool
	DaemonRunning      bool
	ConfigValid        bool
	NeighborsConnected int
	TotalNeighbors     int
	Issues             []string
}

// ProtocolStatus represents BGP protocol status
type ProtocolStatus struct {
	Enabled       bool
	Version       string
	RouterID      string
	AS            uint32
	Uptime        int64
	MemoryUsage   uint64
	RouteCount    uint32
	NeighborCount uint32
}

func (e *BgpError) Error() string {
	return e.Message
}

func (vr *ValidationResult) AddError(field, message, code string, value any) {
	vr.Valid = false
	vr.Errors = append(vr.Errors, &ValidationError{
		Field:   field,
		Value:   value,
		Message: message,
		Code:    code,
	})
}

func (vr *ValidationResult) AddWarning(field, message, code string, value any) {
	vr.Warnings = append(vr.Warnings, &ValidationWarning{
		Field:   field,
		Value:   value,
		Message: message,
		Code:    code,
	})
}

// ConfigManagerInterface handles BGP configuration operations
type ConfigManagerInterface interface {
	ValidateConfig(config *client.BgpConfig) error
	ApplyConfig(config *client.BgpConfig) error
	GetCurrentConfig() (*client.BgpConfig, error)
}

// NeighborManagerInterface handles BGP neighbor operations
type NeighborManagerInterface interface {
	AddNeighbor(asNumber uint32, neighbor *client.BgpNeighbor) error
	RemoveNeighbor(asNumber uint32, peerIP string) error
	UpdateNeighbor(asNumber uint32, neighbor *client.BgpNeighbor) error
	GetNeighborByIP(peerIP string) (*client.BgpNeighbor, error)
	ParseNeighborDetails(peerIP string, asNumber uint32) (*client.BgpNeighbor, error)
}

// PolicyManagerInterface handles BGP policy operations
type PolicyManagerInterface interface {
	ApplyRouteMap(routeMap *client.BgpRouteMap) error
	RemoveRouteMap(name string) error
	ApplyCommunityList(communityList *client.BgpCommunityList) error
	RemoveCommunityList(name string) error
	ApplyPrefixList(prefixList *client.BgpPrefixList) error
	RemovePrefixList(name string) error
	GetPrefixListDetails(name string, sequence uint32) (*client.BgpPrefixList, error)
	GetPolicyConfig() (*client.BgpPolicyConfig, error)
	ValidateRouteMap(routeMap *client.BgpRouteMap) error
	ValidateCommunityList(communityList *client.BgpCommunityList) error
	ValidatePrefixList(prefixList *client.BgpPrefixList) error
}

// StateManagerInterface handles BGP state monitoring and statistics
type StateManagerInterface interface {
	GetBgpState() (*client.Ipv4UnicastSummary, error)
	ParseBgpSummary() (*client.ShowBgpSummary, error)
	ParseBgpNeighbors() (*client.ShowBgpNeighbors, error)
	ParseBgpRoutesNew() (*client.Routes, error)
	ClearBgpRoutes(clearBgp *client.ClearBgp) error
}

// ValidationManagerInterface handles all validation operations with caching
type ValidationManagerInterface interface {
	ValidateBgpConfig(config *client.BgpConfig) *ValidationResult
	ValidateNeighbor(neighbor *client.BgpNeighbor) *ValidationResult
	ValidateIPAddresses(addresses []string) error
}

// ErrorHandlerInterface provides structured error handling
type ErrorHandlerInterface interface {
	NewConfigError(op string, err error) *BgpError
	NewValidationError(field string, value any, reason string) *BgpError
	NewOperationError(op string, err error) *BgpError
	IsRetryableError(err error) bool
}

// BGPManagerInterface defines the interface for BGP operations
type BGPManagerInterface interface {
	SetConfig(config *client.BgpConfig) error
	GetConfig() (*client.BgpConfig, error)
	RemoveNeighbor(asNumber uint32, peerIP string) error
	GetState() (*client.Ipv4UnicastSummary, error)
	GetBgpRoutes() (*client.Routes, error)
	GetPolicyManager() PolicyManagerInterface
	GetNeighborManager() NeighborManagerInterface
	GetStateManager() StateManagerInterface
}
