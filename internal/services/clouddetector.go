package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

// CloudDetector handles cloud provider detection
type CloudDetector struct {
	log    *logger.Logger
	client *http.Client
	config CloudDetectorConfig
}

// CloudDetectorConfig holds timeout configurations
type CloudDetectorConfig struct {
	DetectionTimeout time.Duration // Timeout for detection requests
	MetadataTimeout  time.Duration // Timeout for metadata requests
	OverallTimeout   time.Duration // Overall HTTP client timeout
}

// NewCloudDetector creates a new cloud detector
func NewCloudDetector() *CloudDetector {
	config := CloudDetectorConfig{
		DetectionTimeout: 2 * time.Second,  // Quick detection
		MetadataTimeout:  5 * time.Second,  // More time for metadata
		OverallTimeout:   10 * time.Second, // Overall client timeout
	}
	
	return &CloudDetector{
		log:    logger.NewLogger("clouddetector"),
		config: config,
		client: &http.Client{
			Timeout: config.OverallTimeout,
		},
	}
}

// DetectProvider tries to detect the cloud provider
func (cd *CloudDetector) DetectProvider(ctx context.Context) string {
	cd.log.Debugf("Starting cloud provider detection (detection timeout: %v, metadata timeout: %v)...", 
		cd.config.DetectionTimeout, cd.config.MetadataTimeout)
	
	// Try OpenStack first
	if cd.tryOpenStack(ctx) {
		cd.log.Info("Detected cloud provider: OpenStack")
		return "openstack"
	}
	
	// Try AWS
	if cd.tryAWS(ctx) {
		cd.log.Info("Detected cloud provider: AWS")
		return "aws"
	}
	
	// Try GCP
	if cd.tryGCP(ctx) {
		cd.log.Info("Detected cloud provider: GCP")
		return "gcp"
	}
	
	// Try Azure
	if cd.tryAzure(ctx) {
		cd.log.Info("Detected cloud provider: Azure")
		return "azure"
	}
	
	cd.log.Info("No cloud provider detected, using 'other'")
	return "other"
}

// GetMetadata returns metadata based on cloud name and detected provider
func (cd *CloudDetector) GetMetadata(ctx context.Context, cloudName, detectedProvider string) map[string]string {
	metadata := map[string]string{
		"cloud_name": cloudName,
	}
	
	// If cloud is "other", don't fetch additional metadata
	if cloudName == "other" {
		cd.log.Debug("Cloud is 'other', skipping metadata collection")
		return metadata
	}
	
	cd.log.Debugf("Collecting metadata for cloud: %s, provider: %s", cloudName, detectedProvider)
	
	// Try to get provider-specific metadata
	switch detectedProvider {
	case "openstack":
		cd.log.Debug("Fetching OpenStack metadata...")
		if osmeta := cd.getOpenStackMetadata(ctx); osmeta != nil {
			cd.log.Debugf("Retrieved %d OpenStack metadata fields", len(osmeta))
			maps.Copy(metadata, osmeta)
		}
	case "aws":
		cd.log.Debug("AWS detected, adding basic metadata")
		metadata["aws_detected"] = "true"
	case "gcp":
		cd.log.Debug("Fetching GCP metadata...")
		if gcpmeta := cd.getGCPMetadata(ctx); gcpmeta != nil {
			cd.log.Debugf("Retrieved %d GCP metadata fields", len(gcpmeta))
			maps.Copy(metadata, gcpmeta)
		}
	case "azure":
		cd.log.Debug("Fetching Azure metadata...")
		if azmeta := cd.getAzureMetadata(ctx); azmeta != nil {
			cd.log.Debugf("Retrieved %d Azure metadata fields", len(azmeta))
			maps.Copy(metadata, azmeta)
		}
	}
	
	return metadata
}

// tryOpenStack checks if running on OpenStack
func (cd *CloudDetector) tryOpenStack(ctx context.Context) bool {
	cd.log.Debug("Checking OpenStack metadata endpoint...")
	reqCtx, cancel := context.WithTimeout(ctx, cd.config.DetectionTimeout)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://169.254.169.254/openstack/latest/meta_data.json", nil)
	if err != nil {
		cd.log.Debugf("OpenStack request creation failed: %v", err)
		return false
	}
	
	resp, err := cd.client.Do(req)
	if err != nil {
		cd.log.Debugf("OpenStack detection failed: %v", err)
		return false
	}
	defer resp.Body.Close()
	
	success := resp.StatusCode == http.StatusOK
	cd.log.Debugf("OpenStack detection result: %v (status: %d)", success, resp.StatusCode)
	return success
}

// tryAWS checks if running on AWS
func (cd *CloudDetector) tryAWS(ctx context.Context) bool {
	cd.log.Debug("Checking AWS metadata endpoint...")
	reqCtx, cancel := context.WithTimeout(ctx, cd.config.DetectionTimeout)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		cd.log.Debugf("AWS request creation failed: %v", err)
		return false
	}
	
	resp, err := cd.client.Do(req)
	if err != nil {
		cd.log.Debugf("AWS detection failed: %v", err)
		return false
	}
	defer resp.Body.Close()
	
	success := resp.StatusCode == http.StatusOK
	cd.log.Debugf("AWS detection result: %v (status: %d)", success, resp.StatusCode)
	return success
}

// tryGCP checks if running on GCP
func (cd *CloudDetector) tryGCP(ctx context.Context) bool {
	cd.log.Debug("Checking GCP metadata endpoint...")
	reqCtx, cancel := context.WithTimeout(ctx, cd.config.DetectionTimeout)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://metadata.google.internal/computeMetadata/v1/instance/", nil)
	if err != nil {
		cd.log.Debugf("GCP request creation failed: %v", err)
		return false
	}
	req.Header.Set("Metadata-Flavor", "Google")
	
	resp, err := cd.client.Do(req)
	if err != nil {
		cd.log.Debugf("GCP detection failed: %v", err)
		return false
	}
	defer resp.Body.Close()
	
	success := resp.StatusCode == http.StatusOK
	cd.log.Debugf("GCP detection result: %v (status: %d)", success, resp.StatusCode)
	return success
}

// tryAzure checks if running on Azure
func (cd *CloudDetector) tryAzure(ctx context.Context) bool {
	cd.log.Debug("Checking Azure metadata endpoint...")
	reqCtx, cancel := context.WithTimeout(ctx, cd.config.DetectionTimeout)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://169.254.169.254/metadata/instance?api-version=2021-02-01", nil)
	if err != nil {
		cd.log.Debugf("Azure request creation failed: %v", err)
		return false
	}
	req.Header.Set("Metadata", "true")
	
	resp, err := cd.client.Do(req)
	if err != nil {
		cd.log.Debugf("Azure detection failed: %v", err)
		return false
	}
	defer resp.Body.Close()
	
	success := resp.StatusCode == http.StatusOK
	cd.log.Debugf("Azure detection result: %v (status: %d)", success, resp.StatusCode)
	return success
}

// getOpenStackMetadata fetches OpenStack metadata
func (cd *CloudDetector) getOpenStackMetadata(ctx context.Context) map[string]string {
	reqCtx, cancel := context.WithTimeout(ctx, cd.config.MetadataTimeout)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://169.254.169.254/openstack/latest/meta_data.json", nil)
	if err != nil {
		return nil
	}
	
	resp, err := cd.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil
	}
	
	metadata := make(map[string]string)
	for key, value := range data {
		switch v := value.(type) {
		case string:
			metadata["os_"+key] = v
		case map[string]interface{}:
			if jsonBytes, err := json.Marshal(v); err == nil {
				metadata["os_"+key] = string(jsonBytes)
			}
		default:
			metadata["os_"+key] = fmt.Sprintf("%v", v)
		}
	}
	
	return metadata
}

// getGCPMetadata fetches GCP metadata
func (cd *CloudDetector) getGCPMetadata(ctx context.Context) map[string]string {
	reqCtx, cancel := context.WithTimeout(ctx, cd.config.MetadataTimeout)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://metadata.google.internal/computeMetadata/v1/instance/?recursive=true", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Metadata-Flavor", "Google")
	
	resp, err := cd.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil
	}
	
	metadata := make(map[string]string)
	for key, value := range data {
		switch v := value.(type) {
		case string:
			if !strings.Contains(v, "/") { // Skip URLs
				metadata["gcp_"+key] = v
			}
		default:
			metadata["gcp_"+key] = fmt.Sprintf("%v", v)
		}
	}
	
	return metadata
}

// getAzureMetadata fetches Azure metadata
func (cd *CloudDetector) getAzureMetadata(ctx context.Context) map[string]string {
	reqCtx, cancel := context.WithTimeout(ctx, cd.config.MetadataTimeout)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://169.254.169.254/metadata/instance?api-version=2021-02-01", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Metadata", "true")
	
	resp, err := cd.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil
	}
	
	metadata := make(map[string]string)
	if compute, ok := data["compute"].(map[string]interface{}); ok {
		for key, value := range compute {
			metadata["azure_"+key] = fmt.Sprintf("%v", value)
		}
	}
	
	return metadata
}