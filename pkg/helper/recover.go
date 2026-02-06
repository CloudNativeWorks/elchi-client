package helper

import (
	"runtime/debug"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

// RecoverPanic recovers from panics in goroutines and logs the stack trace.
// Usage: defer helper.RecoverPanic(logger, "goroutine-name")
func RecoverPanic(log *logger.Logger, name string) {
	if r := recover(); r != nil {
		log.Errorf("PANIC recovered in %s: %v\nStack: %s", name, r, debug.Stack())
	}
}
