package filebeat

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/common"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"gopkg.in/yaml.v3"
	"sync"
)

const (
	ConfigPath      = "/etc/filebeat/filebeat.yml"
	filebeatService = "filebeat"
)

// updateMu serializes UpdateConfig so a control-plane UPDATE and the reconcile loop
// (a separate goroutine) can never stage to the shared temp path or restart the
// service at the same time.
var updateMu sync.Mutex

// FilebeatConfig represents the full filebeat.yml structure
type FilebeatConfig struct {
	FilebeatInputs      []FilebeatInputConfig      `yaml:"filebeat.inputs"`
	Processors          []any                      `yaml:"processors"`
	OutputLogstash      *LogstashOutputConfig      `yaml:"output.logstash,omitempty"`
	OutputElasticsearch *ElasticsearchOutputConfig `yaml:"output.elasticsearch,omitempty"`
	SetupILM            *SetupILMConfig            `yaml:"setup.ilm,omitempty"`
	SetupTemplate       *SetupTemplateConfig       `yaml:"setup.template,omitempty"`
}

// SetupILMConfig represents ILM (Index Lifecycle Management) configuration
type SetupILMConfig struct {
	Enabled bool `yaml:"enabled"`
}

// SetupTemplateConfig represents index template configuration
type SetupTemplateConfig struct {
	Name    string `yaml:"name"`
	Pattern string `yaml:"pattern"`
}

// FilebeatInputConfig represents a single filebeat input
type FilebeatInputConfig struct {
	Type    string   `yaml:"type"`
	Enabled bool     `yaml:"enabled"`
	ID      string   `yaml:"id"`
	Paths   []string `yaml:"paths"`
}

// LogstashOutputConfig represents logstash output configuration
type LogstashOutputConfig struct {
	Hosts       []string `yaml:"hosts"`
	Loadbalance bool     `yaml:"loadbalance"`
}

// ElasticsearchOutputConfig represents elasticsearch output configuration
type ElasticsearchOutputConfig struct {
	Hosts               []string `yaml:"hosts"`
	Index               string   `yaml:"index,omitempty"` // Index pattern (e.g., "elchi-%{+yyyy.MM.dd}")
	Loadbalance         bool     `yaml:"loadbalance,omitempty"`
	SSLVerificationMode string   `yaml:"ssl.verification_mode,omitempty"` // "none" to skip SSL verify
	APIKey              string   `yaml:"api_key,omitempty"`
	Username            string   `yaml:"username,omitempty"`
	Password            string   `yaml:"password,omitempty"`
}

// GetCurrentConfig reads the current filebeat configuration
func GetCurrentConfig(logger *logger.Logger) (*client.RequestFilebeat, error) {
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read filebeat config: %w", err)
	}

	var config FilebeatConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse filebeat config: %w", err)
	}

	// Convert to proto format
	protoConfig := &client.RequestFilebeat{
		Inputs:              make([]*client.FilebeatInput, 0),
		TimestampProcessor:  &client.TimestampProcessor{},
		DropFieldsProcessor: &client.DropFieldsProcessor{},
		FilebeatOutput:      &client.FilebeatOutput{},
	}

	// Convert inputs
	for _, input := range config.FilebeatInputs {
		protoConfig.Inputs = append(protoConfig.Inputs, &client.FilebeatInput{
			Type:    input.Type,
			Enabled: input.Enabled,
			Id:      input.ID,
			Paths:   input.Paths,
		})
	}

	// Parse processors
	for _, proc := range config.Processors {
		if procMap, ok := proc.(map[string]interface{}); ok {
			// Timestamp processor
			if tsProc, exists := procMap["timestamp"]; exists {
				if tsMap, ok := tsProc.(map[string]interface{}); ok {
					if field, ok := tsMap["field"].(string); ok {
						protoConfig.TimestampProcessor.Field = field
					}
					if layouts, ok := tsMap["layouts"].([]interface{}); ok {
						for _, layout := range layouts {
							if layoutStr, ok := layout.(string); ok {
								protoConfig.TimestampProcessor.Layouts = append(protoConfig.TimestampProcessor.Layouts, layoutStr)
							}
						}
					}
					if tests, ok := tsMap["test"].([]interface{}); ok {
						for _, test := range tests {
							if testStr, ok := test.(string); ok {
								protoConfig.TimestampProcessor.Test = append(protoConfig.TimestampProcessor.Test, testStr)
							}
						}
					}
				}
			}

			// Drop fields processor
			if dropProc, exists := procMap["drop_fields"]; exists {
				if dropMap, ok := dropProc.(map[string]interface{}); ok {
					if fields, ok := dropMap["fields"].([]interface{}); ok {
						for _, field := range fields {
							if fieldStr, ok := field.(string); ok {
								protoConfig.DropFieldsProcessor.Fields = append(protoConfig.DropFieldsProcessor.Fields, fieldStr)
							}
						}
					}
				}
			}
		}
	}

	// Convert output - check which output type is configured
	if config.OutputElasticsearch != nil {
		esOutput := &client.ElasticsearchOutput{
			Hosts:       config.OutputElasticsearch.Hosts,
			Loadbalance: config.OutputElasticsearch.Loadbalance,
		}

		// Check if SSL verification is disabled
		if config.OutputElasticsearch.SSLVerificationMode == "none" {
			esOutput.SkipSslVerify = true
		}

		// Check which auth method is used
		if config.OutputElasticsearch.APIKey != "" {
			esOutput.Auth = &client.ElasticsearchOutput_ApiKey{
				ApiKey: config.OutputElasticsearch.APIKey,
			}
		} else if config.OutputElasticsearch.Username != "" {
			esOutput.Auth = &client.ElasticsearchOutput_BasicAuth{
				BasicAuth: &client.BasicAuth{
					Username: config.OutputElasticsearch.Username,
					Password: config.OutputElasticsearch.Password,
				},
			}
		}

		protoConfig.FilebeatOutput.Output = &client.FilebeatOutput_Elasticsearch{
			Elasticsearch: esOutput,
		}
	} else if config.OutputLogstash != nil {
		protoConfig.FilebeatOutput.Output = &client.FilebeatOutput_Logstash{
			Logstash: &client.LogstashOutput{
				Hosts:       config.OutputLogstash.Hosts,
				Loadbalance: config.OutputLogstash.Loadbalance,
			},
		}
	}

	return protoConfig, nil
}

// RenderConfig builds the filebeat.yml content from the request. It is the single
// source of truth for the rendered file, shared by UpdateConfig and the reconcile
// loop (which compares it against the live file to detect drift), so the two can
// never diverge.
func RenderConfig(config *client.RequestFilebeat) ([]byte, error) {
	// Build YAML config
	filebeatConfig := FilebeatConfig{
		FilebeatInputs: make([]FilebeatInputConfig, 0),
		Processors:     make([]interface{}, 0),
	}

	// Set output based on proto config
	if config.FilebeatOutput != nil {
		switch output := config.FilebeatOutput.Output.(type) {
		case *client.FilebeatOutput_Elasticsearch:
			esConfig := &ElasticsearchOutputConfig{
				Hosts:       output.Elasticsearch.Hosts,
				Index:       "fs-elchi-%{+yyyy.MM.dd}", // Static index pattern
				Loadbalance: output.Elasticsearch.Loadbalance,
			}

			// Set SSL verification skip if enabled
			if output.Elasticsearch.SkipSslVerify {
				esConfig.SSLVerificationMode = "none"
			}

			// Set auth method
			switch auth := output.Elasticsearch.Auth.(type) {
			case *client.ElasticsearchOutput_ApiKey:
				esConfig.APIKey = auth.ApiKey
			case *client.ElasticsearchOutput_BasicAuth:
				esConfig.Username = auth.BasicAuth.Username
				esConfig.Password = auth.BasicAuth.Password
			}

			filebeatConfig.OutputElasticsearch = esConfig

			// Add static setup configuration for Elasticsearch
			filebeatConfig.SetupILM = &SetupILMConfig{
				Enabled: false,
			}
			filebeatConfig.SetupTemplate = &SetupTemplateConfig{
				Name:    "fs-elchi",
				Pattern: "fs-elchi-*",
			}

		case *client.FilebeatOutput_Logstash:
			filebeatConfig.OutputLogstash = &LogstashOutputConfig{
				Hosts:       output.Logstash.Hosts,
				Loadbalance: output.Logstash.Loadbalance,
			}
		}
	}

	// Convert inputs
	for _, input := range config.Inputs {
		filebeatConfig.FilebeatInputs = append(filebeatConfig.FilebeatInputs, FilebeatInputConfig{
			Type:    input.Type,
			Enabled: input.Enabled,
			ID:      input.Id,
			Paths:   input.Paths,
		})
	}

	// Add timestamp processor
	if config.TimestampProcessor != nil && config.TimestampProcessor.Field != "" {
		timestampProc := map[string]interface{}{
			"timestamp": map[string]interface{}{
				"field":   config.TimestampProcessor.Field,
				"layouts": config.TimestampProcessor.Layouts,
				"test":    config.TimestampProcessor.Test,
			},
		}
		filebeatConfig.Processors = append(filebeatConfig.Processors, timestampProc)
	}

	// Add drop_fields processor
	if config.DropFieldsProcessor != nil && len(config.DropFieldsProcessor.Fields) > 0 {
		dropFieldsProc := map[string]interface{}{
			"drop_fields": map[string]interface{}{
				"fields": config.DropFieldsProcessor.Fields,
			},
		}
		filebeatConfig.Processors = append(filebeatConfig.Processors, dropFieldsProc)
	}

	// Marshal to YAML
	data, err := yaml.Marshal(&filebeatConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	return data, nil
}

// UpdateConfig renders the filebeat config, writes it atomically (validated), and
// restarts the service.
func UpdateConfig(ctx context.Context, config *client.RequestFilebeat, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	updateMu.Lock()
	defer updateMu.Unlock()

	data, err := RenderConfig(config)
	if err != nil {
		return err
	}

	// Stage to a temp file, validate it with `filebeat test config`, then atomically
	// rename it into place. This keeps an interrupted write or a rejected config from
	// ever leaving a broken filebeat.yml that the following restart would load.
	validate := func(ctx context.Context, tmpPath string) error {
		return validateStagedConfig(ctx, tmpPath, logger, runner)
	}
	if err := common.AtomicReplaceFileWithS(ctx, runner, ConfigPath, string(data), "644", validate); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	logger.Infof("Filebeat configuration updated successfully")

	// Restart filebeat service to apply changes
	logger.Infof("Restarting filebeat service to apply configuration changes...")
	if err := RestartService(ctx, logger, runner); err != nil {
		return fmt.Errorf("config updated but failed to restart service: %w", err)
	}

	return nil
}

// validateStagedConfig runs `filebeat test config -c <tmpPath>` to check the staged
// config before it is committed. A genuine rejection returns an error (so the live
// file is kept); if the validator itself can't run (e.g. binary absent or keystore
// unavailable), it logs a warning and returns nil so the push still proceeds
// (best-effort, matching shield).
func validateStagedConfig(ctx context.Context, tmpPath string, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	out, err := runner.RunWithOutputSNoErrLog(ctx, "filebeat", "test", "config", "-c", tmpPath)
	switch common.ClassifyValidatorResult(err, string(out)) {
	case common.ConfigInvalid:
		return fmt.Errorf("filebeat config validation failed: %s", strings.TrimSpace(string(out)))
	case common.ConfigValidatorUnavailable:
		logger.Warnf("filebeat config validator could not run, proceeding without pre-flight validation: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GetServiceStatus returns the current filebeat service status using systemd package
func GetServiceStatus(ctx context.Context, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	return systemd.GetServiceStatus(ctx, filebeatService, logger, runner)
}

// ServiceControl performs service control operations (start/stop/restart/status)
func ServiceControl(ctx context.Context, serviceName string, action client.SubCommandType, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	return systemd.ServiceControl(ctx, serviceName, action, logger, runner)
}

// RestartService restarts the filebeat service
func RestartService(ctx context.Context, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	_, err := systemd.ServiceControl(ctx, filebeatService, client.SubCommandType_SUB_RESTART, logger, runner)
	if err != nil {
		return fmt.Errorf("failed to restart filebeat: %w", err)
	}

	logger.Infof("Filebeat service restarted successfully")
	return nil
}
