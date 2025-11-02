package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Fields type is an alias for logrus.Fields
type Fields = logrus.Fields

// Logger is a wrapper around logrus.Logger
type Logger struct {
	*logrus.Logger
	module string
}

// Global logger instance
var globalLogger *Logger

// Configuration for the logger
type Config struct {
	Level      string `mapstructure:"level"`
	Format     string `mapstructure:"format"`
	Module     string `mapstructure:"module"`
	MaxSize    int    `mapstructure:"max_size"`
	MaxAge     int    `mapstructure:"max_age"`
	MaxBackups int    `mapstructure:"max_backups"`
	Compress   bool   `mapstructure:"compress"`
}

// Init initializes the global logger with the provided configuration
func Init(config Config) error {
	level, err := logrus.ParseLevel(config.Level)
	if err != nil {
		return fmt.Errorf("invalid log level: %v", err)
	}

	logger := logrus.New()
	logger.SetLevel(level)

	// Set formatter based on config
	if config.Format == "json" {
		logger.SetFormatter(&logrus.JSONFormatter{
			CallerPrettyfier: callerPrettyfier,
			TimestampFormat:  "2006-01-02 15:04:05",
			FieldMap: logrus.FieldMap{
				logrus.FieldKeyTime:  "timestamp",
				logrus.FieldKeyLevel: "level",
				logrus.FieldKeyMsg:   "message",
			},
		})
	} else {
		logger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:          true,
			CallerPrettyfier:       callerPrettyfier,
			DisableSorting:         true,
			DisableTimestamp:       false,
			DisableLevelTruncation: true,
			ForceColors:            true,
			PadLevelText:           true,
			TimestampFormat:        "2006-01-02 15:04:05",
		})
	}

	// Get log file path
	logPath := getDefaultLogPath()

	// Configure outputs
	var outputs []io.Writer

	// Always add stdout
	outputs = append(outputs, os.Stdout)

	// Create log directory and test write permissions
	logDir := filepath.Dir(logPath)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Printf("Warning: Could not create log directory %s: %v\n", logDir, err)
	} else {
		// Configure log rotation
		rotateLogger := &lumberjack.Logger{
			Filename:   logPath,
			MaxSize:    config.MaxSize,
			MaxAge:     config.MaxAge,
			MaxBackups: config.MaxBackups,
			Compress:   config.Compress,
		}

		// Test if we can write to the log file
		if _, err := rotateLogger.Write([]byte("Logger initialization test\n")); err != nil {
			fmt.Printf("Warning: Could not write to log file %s: %v\n", logPath, err)
		} else {
			outputs = append(outputs, rotateLogger)
			fmt.Printf("Logging to file: %s\n", logPath)
		}
	}

	// Set multi-writer if we have multiple outputs
	if len(outputs) > 1 {
		logger.SetOutput(io.MultiWriter(outputs...))
	} else {
		logger.SetOutput(outputs[0])
	}

	// Enable caller info
	logger.SetReportCaller(true)

	globalLogger = &Logger{
		Logger: logger,
		module: config.Module,
	}

	// Log initialization success with details
	if len(outputs) > 1 {
		globalLogger.WithFields(Fields{
			"file_path": logPath,
			"level":     level.String(),
			"format":    config.Format,
		}).Info("Logger initialized successfully with file output")
	} else {
		globalLogger.WithFields(Fields{
			"level":  level.String(),
			"format": config.Format,
		}).Info("Logger initialized successfully with stdout only")
	}

	return nil
}

// getDefaultLogPath returns the default log file path
func getDefaultLogPath() string {
	return "/var/log/elchi-client.log"
}

// callerPrettyfier is used to format the caller information
func callerPrettyfier(f *runtime.Frame) (string, string) {
	// Walk up the stack until we find the actual caller
	pcs := make([]uintptr, 15)
	n := runtime.Callers(4, pcs) // Start from 4 to skip more internal frames
	if n == 0 {
		return "", fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line)
	}

	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		// Skip logrus and our logger package frames
		if !strings.Contains(frame.File, "pkg/logger") &&
			!strings.Contains(frame.File, "sirupsen/logrus") {
			return "", fmt.Sprintf("%s:%d", filepath.Base(frame.File), frame.Line)
		}
		if !more {
			break
		}
	}

	// Fallback to original frame if we couldn't find a better one
	return "", fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line)
}

// NewLogger creates a new logger instance with the specified module
func NewLogger(module string) *Logger {
	if globalLogger == nil {
		panic("logger not initialized. Call logger.Init() first")
	}

	return &Logger{
		Logger: globalLogger.Logger,
		module: module,
	}
}

// withModule adds the module field to the entry
func (l *Logger) withModule(fields Fields) *logrus.Entry {
	if l.module != "" {
		if fields == nil {
			fields = Fields{}
		}
		fields["module"] = l.module
	}
	return l.WithFields(fields)
}

// Debug logs a message at the debug level
func (l *Logger) Debug(args ...any) {
	l.withModule(nil).Debug(args...)
}

// Debugf logs a formatted message at the debug level
func (l *Logger) Debugf(format string, args ...any) {
	l.withModule(nil).Debugf(format, args...)
}

// Info logs a message at the info level
func (l *Logger) Info(args ...any) {
	l.withModule(nil).Info(args...)
}

// Infof logs a formatted message at the info level
func (l *Logger) Infof(format string, args ...any) {
	l.withModule(nil).Infof(format, args...)
}

// Warn logs a message at the warn level
func (l *Logger) Warn(args ...any) {
	l.withModule(nil).Warn(args...)
}

// Warnf logs a formatted message at the warn level
func (l *Logger) Warnf(format string, args ...any) {
	l.withModule(nil).Warnf(format, args...)
}

// Error logs a message at the error level
func (l *Logger) Error(args ...any) {
	l.withModule(nil).Error(args...)
}

// Errorf logs a formatted message at the error level
func (l *Logger) Errorf(format string, args ...any) {
	l.withModule(nil).Errorf(format, args...)
}

// Fatal logs a message at the fatal level and then exits
func (l *Logger) Fatal(args ...any) {
	l.withModule(nil).Fatal(args...)
}

// Fatalf logs a formatted message at the fatal level and then exits
func (l *Logger) Fatalf(format string, args ...any) {
	l.withModule(nil).Fatalf(format, args...)
}

// WithFields adds fields to the logger
func (l *Logger) WithFields(fields Fields) *logrus.Entry {
	if l.module != "" {
		if fields == nil {
			fields = Fields{}
		}
		fields["module"] = l.module
	}
	return l.Logger.WithFields(fields)
}

// WithError adds an error to the logger
func (l *Logger) WithError(err error) *logrus.Entry {
	return l.WithFields(Fields{"error": err})
}

// Middleware functions for global access if needed
func Fatalf(format string, args ...any) {
	if globalLogger != nil {
		globalLogger.Fatalf(format, args...)
	}
}
