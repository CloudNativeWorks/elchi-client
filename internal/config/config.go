package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

const (
	clientIDFile = ".elchi_client_id" // File to store the client ID
)

// Config holds all application configuration
type Config struct {
	Server  ServerConfig  `mapstructure:"server"`
	Logging LoggingConfig `mapstructure:"logging"`
	Client  ClientConfig  `mapstructure:"client"`
}

// ServerConfig holds GRPC server configuration
type ServerConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	Token        string `mapstructure:"token"`
	TLS          bool   `mapstructure:"tls"`
	Timeout      string `mapstructure:"timeout"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// ClientConfig holds client-specific configuration
type ClientConfig struct {
	Name  string `mapstructure:"name"`
	BGP   *bool  `mapstructure:"bgp"`   // Pointer to detect if explicitly set
	Cloud string `mapstructure:"cloud"`
}

// GetStoredClientID reads the client ID from the storage file
func GetStoredClientID() (string, error) {
	idPath := filepath.Join(models.ElchiLibPath, clientIDFile)
	if id, err := os.ReadFile(idPath); err == nil {
		return string(id), nil
	}

	newID := uuid.New().String()
	err := os.WriteFile(idPath, []byte(newID), 0600)
	if err != nil {
		return "", fmt.Errorf("failed to save client ID: %v", err)
	}

	return newID, nil
}

// ExtractProjectIDFromToken extracts the project ID from a token
// Token format: additionalData--projectID
func ExtractProjectIDFromToken(token string) string {
	parts := strings.Split(token, "--")
	if len(parts) > 1 {
		return parts[1]
	}
	return ""
}

// LoadConfig loads configuration from file
func LoadConfig(path string) (*Config, error) {
	v := viper.New()

	// Default values
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 50051)
	v.SetDefault("server.timeout", "30s")

	// Logging defaults
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")

	v.SetDefault("client.tls", false)

	// Configuration file name and path
	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.elchi")
		v.AddConfigPath(models.ElchiPath)
	}

	// Read environment variables
	v.AutomaticEnv()
	v.SetEnvPrefix("ELCHI")

	// Read configuration file
	err := v.ReadInConfig()
	if err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	// Bind configuration to struct
	var config Config
	err = v.Unmarshal(&config)
	if err != nil {
		return nil, err
	}

	// Initialize logger
	err = initLogger(&config.Logging)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

// initLogger initializes the logger with the provided configuration
func initLogger(cfg *LoggingConfig) error {
	logConfig := logger.Config{
		Level:      cfg.Level,
		Format:     cfg.Format,
		Module:     "main",
	}

	return logger.Init(logConfig)
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:         "localhost",
			Port:         50051,
			Timeout:      "30s",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Client: ClientConfig{
			Name: "new-client",
		},
	}
}
