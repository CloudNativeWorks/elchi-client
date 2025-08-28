package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

// CloudDetector handles cloud provider detection
type CloudDetector struct {
	log    *logger.Logger
	client *http.Client
}

// NewCloudDetector creates a new cloud detector
func NewCloudDetector() *CloudDetector {
	return &CloudDetector{
		log: logger.NewLogger("clouddetector"),
		client: &http.Client{
			Timeout: 3 * time.Second,
		},
	}
}

// DetectProvider tries to detect the cloud provider
func (cd *CloudDetector) DetectProvider(ctx context.Context) string {
	cd.log.Debug("Starting cloud provider detection...")
	
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
	
	cd.log.Info("No cloud provider detected, using 'unknown'")
	return "unknown"
}

// GetMetadata returns metadata based on cloud name and detected provider
func (cd *CloudDetector) GetMetadata(ctx context.Context, cloudName, detectedProvider string) map[string]string {
	metadata := map[string]string{
		"cloud_name": cloudName,
		"provider":   detectedProvider,
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
			for k, v := range osmeta {
				metadata[k] = v
			}
		}
	case "aws":
		cd.log.Debug("AWS detected, adding basic metadata")
		metadata["aws_detected"] = "true"
	case "gcp":
		cd.log.Debug("Fetching GCP metadata...")
		if gcpmeta := cd.getGCPMetadata(ctx); gcpmeta != nil {
			cd.log.Debugf("Retrieved %d GCP metadata fields", len(gcpmeta))
			for k, v := range gcpmeta {
				metadata[k] = v
			}
		}
	case "azure":
		cd.log.Debug("Fetching Azure metadata...")
		if azmeta := cd.getAzureMetadata(ctx); azmeta != nil {
			cd.log.Debugf("Retrieved %d Azure metadata fields", len(azmeta))
			for k, v := range azmeta {
				metadata[k] = v
			}
		}
	}
	
	return metadata
}

// tryOpenStack checks if running on OpenStack
func (cd *CloudDetector) tryOpenStack(ctx context.Context) bool {
	cd.log.Debug("Checking OpenStack metadata endpoint...")
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://169.254.169.254/openstack/latest/meta_data.json", nil)
	if err != nil {
		return false
	}
	
	resp, err := cd.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	
	return resp.StatusCode == http.StatusOK
}

// tryAWS checks if running on AWS
func (cd *CloudDetector) tryAWS(ctx context.Context) bool {
	cd.log.Debug("Checking AWS metadata endpoint...")
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		return false
	}
	
	resp, err := cd.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	
	return resp.StatusCode == http.StatusOK
}

// tryGCP checks if running on GCP
func (cd *CloudDetector) tryGCP(ctx context.Context) bool {
	cd.log.Debug("Checking GCP metadata endpoint...")
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://metadata.google.internal/computeMetadata/v1/instance/", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Metadata-Flavor", "Google")
	
	resp, err := cd.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	
	return resp.StatusCode == http.StatusOK
}

// tryAzure checks if running on Azure
func (cd *CloudDetector) tryAzure(ctx context.Context) bool {
	cd.log.Debug("Checking Azure metadata endpoint...")
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	
	req, err := http.NewRequestWithContext(reqCtx, "GET", "http://169.254.169.254/metadata/instance?api-version=2021-02-01", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Metadata", "true")
	
	resp, err := cd.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	
	return resp.StatusCode == http.StatusOK
}

// getOpenStackMetadata fetches OpenStack metadata
func (cd *CloudDetector) getOpenStackMetadata(ctx context.Context) map[string]string {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
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
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
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
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
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