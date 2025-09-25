package bgp

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

// ErrorHandler implements structured error handling for BGP operations
type ErrorHandler struct {
	logger *logger.Logger
}

// NewErrorHandler creates a new error handler
func NewErrorHandler(logger *logger.Logger) ErrorHandlerInterface {
	return &ErrorHandler{
		logger: logger,
	}
}

// NewConfigError creates a configuration-related error
func (eh *ErrorHandler) NewConfigError(op string, err error) *BgpError {
	return &BgpError{
		Operation:   op,
		Component:   "config",
		ErrorType:   ErrorTypeConfiguration,
		Message:     fmt.Sprintf("configuration error in %s: %v", op, err),
		Cause:       err,
		Retryable:   eh.isConfigErrorRetryable(err),
		UserMessage: eh.formatConfigErrorForUser(op, err),
	}
}

// NewValidationError creates a validation-related error
func (eh *ErrorHandler) NewValidationError(field string, value any, reason string) *BgpError {
	return &BgpError{
		Operation:   "validation",
		Component:   "validator",
		ErrorType:   ErrorTypeValidation,
		Message:     fmt.Sprintf("validation failed for field '%s' with value '%v': %s", field, value, reason),
		Cause:       nil,
		Retryable:   false,
		UserMessage: fmt.Sprintf("Invalid %s: %s", field, reason),
	}
}

// NewOperationError creates an operation-related error
func (eh *ErrorHandler) NewOperationError(op string, err error) *BgpError {
	errorType := eh.classifyOperationError(err)
	retryable := eh.isOperationErrorRetryable(err)

	return &BgpError{
		Operation:   op,
		Component:   "operation",
		ErrorType:   errorType,
		Message:     fmt.Sprintf("operation '%s' failed: %v", op, err),
		Cause:       err,
		Retryable:   retryable,
		UserMessage: eh.formatOperationErrorForUser(op, err),
	}
}

// IsRetryableError determines if an error is retryable
func (eh *ErrorHandler) IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for syscall errors
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return eh.isSyscallRetryable(errno)
	}

	// Check error message patterns
	errMsg := strings.ToLower(err.Error())

	// Retryable patterns
	retryablePatterns := []string{
		"connection refused",
		"connection timeout",
		"timeout",
		"temporary failure",
		"resource temporarily unavailable",
		"try again",
		"network unreachable",
		"host unreachable",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}

	// Non-retryable patterns
	nonRetryablePatterns := []string{
		"invalid",
		"not found",
		"already exists",
		"permission denied",
		"access denied",
		"malformed",
		"syntax error",
		"parse error",
	}

	for _, pattern := range nonRetryablePatterns {
		if strings.Contains(errMsg, pattern) {
			return false
		}
	}

	// Default to non-retryable for safety
	return false
}

// isConfigErrorRetryable determines if a configuration error is retryable
func (eh *ErrorHandler) isConfigErrorRetryable(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())

	// VTY shell connection issues are retryable
	if strings.Contains(errMsg, "connection") || strings.Contains(errMsg, "timeout") {
		return true
	}

	// Syntax errors are not retryable
	if strings.Contains(errMsg, "syntax") || strings.Contains(errMsg, "invalid") {
		return false
	}

	return false
}

// isOperationErrorRetryable determines if an operation error is retryable
func (eh *ErrorHandler) isOperationErrorRetryable(err error) bool {
	return eh.IsRetryableError(err)
}

// isSyscallRetryable determines if a syscall error is retryable
func (eh *ErrorHandler) isSyscallRetryable(errno syscall.Errno) bool {
	switch errno {
	case syscall.EAGAIN, syscall.ETIMEDOUT:
		return true
	case syscall.ECONNREFUSED, syscall.ENETUNREACH, syscall.EHOSTUNREACH:
		return true
	case syscall.EINVAL, syscall.EPERM, syscall.EACCES, syscall.ENOENT:
		return false
	default:
		return false
	}
}

// classifyOperationError classifies the type of an operation error
func (eh *ErrorHandler) classifyOperationError(err error) ErrorType {
	if err == nil {
		return ErrorTypeOperation
	}

	errMsg := strings.ToLower(err.Error())

	if strings.Contains(errMsg, "connection") || strings.Contains(errMsg, "network") {
		return ErrorTypeConnection
	}
	if strings.Contains(errMsg, "protocol") || strings.Contains(errMsg, "bgp") {
		return ErrorTypeProtocol
	}
	if strings.Contains(errMsg, "config") {
		return ErrorTypeConfiguration
	}
	if strings.Contains(errMsg, "permission") || strings.Contains(errMsg, "access") {
		return ErrorTypeSystem
	}

	return ErrorTypeOperation
}

// formatConfigErrorForUser formats configuration errors for users
func (eh *ErrorHandler) formatConfigErrorForUser(op string, err error) string {
	errMsg := strings.ToLower(err.Error())

	if strings.Contains(errMsg, "syntax") {
		return fmt.Sprintf("Configuration syntax error in %s. Please check your settings.", op)
	}
	if strings.Contains(errMsg, "invalid") {
		return fmt.Sprintf("Invalid configuration for %s. Please verify your input.", op)
	}
	if strings.Contains(errMsg, "connection") {
		return "Unable to connect to BGP daemon. Please check if FRR is running."
	}

	return fmt.Sprintf("Configuration operation '%s' failed. Please check your settings.", op)
}

// formatOperationErrorForUser formats operation errors for users
func (eh *ErrorHandler) formatOperationErrorForUser(op string, err error) string {
	errMsg := strings.ToLower(err.Error())

	if strings.Contains(errMsg, "not found") {
		return fmt.Sprintf("Resource not found during %s operation.", op)
	}
	if strings.Contains(errMsg, "already exists") {
		return fmt.Sprintf("Resource already exists during %s operation.", op)
	}
	if strings.Contains(errMsg, "connection") {
		return "Unable to connect to BGP daemon. Please check if FRR is running."
	}
	if strings.Contains(errMsg, "timeout") {
		return fmt.Sprintf("Operation '%s' timed out. Please try again.", op)
	}

	return fmt.Sprintf("Operation '%s' failed. Please check system status and try again.", op)
}

// IsValidationError checks if an error is a validation error
func IsValidationError(err error) bool {
	var bgpErr *BgpError
	return errors.As(err, &bgpErr) && bgpErr.ErrorType == ErrorTypeValidation
}
