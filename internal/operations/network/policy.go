package network

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
	"gopkg.in/yaml.v3"
)

const (
	MinPolicyPriority = 100  // Elchi policies start from priority 100
	MaxPolicyPriority = 999  // Elchi policies end at priority 999
)

type PolicyManager struct {
	netplanPath string
	logger      *logger.Logger
}

func NewPolicyManager(logger *logger.Logger) *PolicyManager {
	return &PolicyManager{
		netplanPath: models.NetplanPath,
		logger:      logger,
	}
}

// ManagePolicies handles routing policy operations (add/delete/replace)
func (pm *PolicyManager) ManagePolicies(operations []*client.RoutingPolicyOperation) error {
	pm.logger.Info("Managing routing policy operations")

	for _, op := range operations {
		switch op.Action {
		case client.RoutingPolicyOperation_ADD:
			if err := pm.addPolicy(op.Policy); err != nil {
				return fmt.Errorf("failed to add policy: %v", err)
			}
		case client.RoutingPolicyOperation_DELETE:
			if err := pm.deletePolicy(op.Policy); err != nil {
				return fmt.Errorf("failed to delete policy: %v", err)
			}
		case client.RoutingPolicyOperation_REPLACE:
			if err := pm.replacePolicy(op.Policy); err != nil {
				return fmt.Errorf("failed to replace policy: %v", err)
			}
		}
	}

	return nil
}

// addPolicy adds a routing policy both to runtime and persistent config
func (pm *PolicyManager) addPolicy(policy *client.RoutingPolicy) error {
	pm.logger.Infof("Adding routing policy: from=%s, table=%d, priority=%d", 
		policy.From, policy.Table, policy.Priority)
	pm.logger.Debugf("Policy details - To:%s, Interface:%s", policy.To, policy.Interface)

	// Validate policy
	pm.logger.Debug("Validating policy parameters")
	if err := pm.validatePolicy(policy); err != nil {
		pm.logger.Debugf("Policy validation failed: %v", err)
		return err
	}
	pm.logger.Debug("Policy validation successful")

	// Add to runtime (netlink)
	pm.logger.Debug("Adding policy to runtime (netlink)")
	if err := pm.addRuntimePolicy(policy); err != nil {
		pm.logger.Debugf("Runtime policy addition failed: %v", err)
		return fmt.Errorf("failed to add runtime policy: %v", err)
	}
	pm.logger.Debug("Runtime policy added successfully")

	// Add to persistent config (netplan)
	pm.logger.Debug("Adding policy to persistent config (netplan)")
	if err := pm.addPolicyToPersistentConfig(policy); err != nil {
		pm.logger.Warnf("Failed to persist policy to netplan: %v", err)
		// Don't fail the operation, runtime policy was added successfully
	} else {
		pm.logger.Debug("Policy successfully persisted to netplan")
	}

	pm.logger.Debug("Policy addition completed successfully")
	return nil
}

// deletePolicy removes a routing policy from runtime and persistent config
func (pm *PolicyManager) deletePolicy(policy *client.RoutingPolicy) error {
	pm.logger.Infof("Deleting routing policy: from=%s, table=%d, priority=%d", 
		policy.From, policy.Table, policy.Priority)
	pm.logger.Debugf("Policy details - To:%s, Interface:%s", policy.To, policy.Interface)

	// Remove from runtime (netlink)
	pm.logger.Debug("Removing policy from runtime (netlink)")
	if err := pm.deleteRuntimePolicy(policy); err != nil {
		pm.logger.Warnf("Failed to delete runtime policy (may not exist): %v", err)
	} else {
		pm.logger.Debug("Runtime policy removed successfully")
	}

	// Remove from persistent config
	pm.logger.Debug("Removing policy from persistent config (netplan)")
	if err := pm.removePolicyFromPersistentConfig(policy); err != nil {
		pm.logger.Warnf("Failed to remove policy from persistent config: %v", err)
		// Don't fail the operation, runtime policy was removed
	} else {
		pm.logger.Debug("Policy successfully removed from netplan")
	}

	pm.logger.Debug("Policy deletion completed")
	return nil
}

// replacePolicy replaces an existing policy
func (pm *PolicyManager) replacePolicy(policy *client.RoutingPolicy) error {
	pm.logger.Infof("Replacing routing policy: from=%s, table=%d, priority=%d", 
		policy.From, policy.Table, policy.Priority)
	pm.logger.Debugf("Policy details - To:%s, Interface:%s", policy.To, policy.Interface)

	// Delete existing policy (ignore errors if it doesn't exist)
	pm.logger.Debug("Deleting existing policy for replacement")
	pm.deleteRuntimePolicy(policy)

	// Add new policy
	pm.logger.Debug("Adding new policy after deletion")
	return pm.addRuntimePolicy(policy)
}

// addRuntimePolicy adds policy to netlink (ip rule add) - idempotent
func (pm *PolicyManager) addRuntimePolicy(policy *client.RoutingPolicy) error {
	// Use NewRule() to get properly initialized rule
	rule := netlink.NewRule()
	rule.Priority = int(policy.Priority)
	rule.Table = int(policy.Table)
	rule.Family = netlink.FAMILY_V4

	// Parse source address
	if policy.From != "" {
		ip, src, err := net.ParseCIDR(policy.From)
		if err != nil {
			return fmt.Errorf("invalid source address %s: %v", policy.From, err)
		}
		pm.logger.Debugf("Parsed source: Input=%s, IP=%s, Network=%s", policy.From, ip.String(), src.String())
		rule.Src = src
	}

	// Parse destination address
	if policy.To != "" {
		if _, dst, err := net.ParseCIDR(policy.To); err != nil {
			return fmt.Errorf("invalid destination address %s: %v", policy.To, err)
		} else {
			rule.Dst = dst
		}
	}

	// Note: interface field is only used for netplan persistence, not for netlink rules

	// Debug log rule details before adding
	pm.logger.Debugf("Adding netlink rule: Priority=%d, Table=%d, Src=%v, Dst=%v", 
		rule.Priority, rule.Table, rule.Src, rule.Dst)

	// Add rule to netlink (idempotent)
	if err := netlink.RuleAdd(rule); err != nil {
		// Check if rule already exists - this is OK
		if err == syscall.EEXIST {
			pm.logger.Debug("Policy rule already exists, operation is idempotent")
			return nil
		}
		
		// Debug log for real errors
		pm.logger.Debugf("netlink.RuleAdd failed with error: %T: %v", err, err)
		pm.logger.Debugf("Failed rule details: Priority=%d, Table=%d, Family=%d", 
			rule.Priority, rule.Table, rule.Family)
		if rule.Src != nil {
			pm.logger.Debugf("Failed rule Src: %s (IP=%s, Mask=%s, Bits=%d)", 
				rule.Src.String(), rule.Src.IP.String(), rule.Src.Mask.String(), countBits(rule.Src.Mask))
		}
		if rule.Dst != nil {
			pm.logger.Debugf("Failed rule Dst: %s (IP=%s, Mask=%s, Bits=%d)", 
				rule.Dst.String(), rule.Dst.IP.String(), rule.Dst.Mask.String(), countBits(rule.Dst.Mask))
		}
		
		// Try to understand the system error
		if errno, ok := err.(syscall.Errno); ok {
			pm.logger.Debugf("Syscall error number: %d (%s)", errno, errno.Error())
		}
		
		return fmt.Errorf("failed to add netlink rule: %v", err)
	}

	pm.logger.Infof("Successfully added policy: from=%s, table=%d, priority=%d", 
		policy.From, policy.Table, policy.Priority)
	return nil
}

// deleteRuntimePolicy removes policy from netlink (ip rule del) - idempotent
func (pm *PolicyManager) deleteRuntimePolicy(policy *client.RoutingPolicy) error {
	// Use NewRule() to get properly initialized rule
	rule := netlink.NewRule()
	rule.Priority = int(policy.Priority)
	rule.Table = int(policy.Table)
	rule.Family = netlink.FAMILY_V4

	// Parse source address
	if policy.From != "" {
		if _, src, err := net.ParseCIDR(policy.From); err != nil {
			return fmt.Errorf("invalid source address %s: %v", policy.From, err)
		} else {
			rule.Src = src
		}
	}

	// Parse destination address
	if policy.To != "" {
		if _, dst, err := net.ParseCIDR(policy.To); err != nil {
			return fmt.Errorf("invalid destination address %s: %v", policy.To, err)
		} else {
			rule.Dst = dst
		}
	}

	// Note: interface field is only used for netplan persistence, not for netlink rules

	// Delete rule from netlink (idempotent)
	if err := netlink.RuleDel(rule); err != nil {
		// Check if rule doesn't exist - this is OK
		if err == syscall.ESRCH || err == syscall.ENOENT {
			pm.logger.Debug("Policy rule doesn't exist, operation is idempotent")
			return nil
		}
		return fmt.Errorf("failed to delete netlink rule: %v", err)
	}

	pm.logger.Infof("Successfully deleted policy: from=%s, table=%d, priority=%d", 
		policy.From, policy.Table, policy.Priority)
	return nil
}

// validatePolicy validates policy parameters
func (pm *PolicyManager) validatePolicy(policy *client.RoutingPolicy) error {
	if policy.Table == 0 {
		return fmt.Errorf("table ID cannot be zero")
	}

	if policy.Priority == 0 {
		return fmt.Errorf("priority cannot be zero")
	}

	// Check priority range for Elchi-managed policies
	if policy.Priority < MinPolicyPriority || policy.Priority > MaxPolicyPriority {
		return fmt.Errorf("priority must be between %d and %d for Elchi-managed policies",
			MinPolicyPriority, MaxPolicyPriority)
	}

	// Validate IP addresses if provided
	if policy.From != "" {
		if _, _, err := net.ParseCIDR(policy.From); err != nil {
			return fmt.Errorf("invalid source address %s: %v", policy.From, err)
		}
	}

	if policy.To != "" {
		if _, _, err := net.ParseCIDR(policy.To); err != nil {
			return fmt.Errorf("invalid destination address %s: %v", policy.To, err)
		}
	}

	return nil
}

// GetCurrentPolicies returns current routing policies from runtime
func (pm *PolicyManager) GetCurrentPolicies() ([]*client.RoutingPolicy, error) {
	pm.logger.Debug("Getting current routing policies from netlink")
	rules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		pm.logger.Debugf("Failed to list netlink rules: %v", err)
		return nil, fmt.Errorf("failed to list netlink rules: %v", err)
	}
	pm.logger.Debugf("Found %d netlink rules total", len(rules))

	var policies []*client.RoutingPolicy
	
	// Get interface mapping from netplan files
	pm.logger.Debug("Building interface mapping from netplan files")
	interfaceMap := pm.buildPolicyInterfaceMap()
	pm.logger.Debugf("Built interface map with %d entries: %+v", len(interfaceMap), interfaceMap)
	
	for _, rule := range rules {
		// Only include Elchi-managed policies (priority range 100-999)
		if rule.Priority < MinPolicyPriority || rule.Priority > MaxPolicyPriority {
			pm.logger.Debugf("Skipping rule with priority %d (outside Elchi range %d-%d)", 
				rule.Priority, MinPolicyPriority, MaxPolicyPriority)
			continue
		}

		pm.logger.Debugf("Processing Elchi rule: Priority=%d, Table=%d, Family=%d", 
			rule.Priority, rule.Table, rule.Family)

		policy := &client.RoutingPolicy{
			Priority: uint32(rule.Priority),
			Table:    uint32(rule.Table),
		}

		// Convert source address
		if rule.Src != nil {
			policy.From = rule.Src.String()
			pm.logger.Debugf("Rule has source: %s", policy.From)
		}

		// Convert destination address
		if rule.Dst != nil {
			policy.To = rule.Dst.String()
			pm.logger.Debugf("Rule has destination: %s", policy.To)
		}

		// Try to find interface from netplan files
		policyKey := pm.createPolicyKey(policy.From, policy.To, int(policy.Table), int(policy.Priority))
		pm.logger.Debugf("Looking for interface for policy key: %s", policyKey)
		if interfaceName := pm.findInterfaceForPolicy(policy, interfaceMap); interfaceName != "" {
			policy.Interface = interfaceName
			pm.logger.Debugf("Found interface %s for policy %s", interfaceName, policyKey)
		} else {
			pm.logger.Debugf("No interface found for policy %s", policyKey)
		}

		policies = append(policies, policy)
	}

	pm.logger.Debugf("Returning %d Elchi-managed policies", len(policies))

	return policies, nil
}

// countBits counts the number of bits set in a mask
func countBits(mask net.IPMask) int {
	count := 0
	for _, b := range mask {
		for i := 0; i < 8; i++ {
			if b&(1<<uint(7-i)) != 0 {
				count++
			}
		}
	}
	return count
}

// PolicyManage handles SUB_POLICY_MANAGE command
func PolicyManage(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if networkReq == nil {
		return helper.NewErrorResponse(cmd, "network request is nil")
	}

	if len(networkReq.GetPolicyOperations()) == 0 {
		return helper.NewErrorResponse(cmd, "no policy operations specified")
	}

	manager := NewPolicyManager(logger)
	
	if err := manager.ManagePolicies(networkReq.GetPolicyOperations()); err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("policy management failed: %v", err))
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
	}
}

// PolicyList handles SUB_POLICY_LIST command
func PolicyList(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	manager := NewPolicyManager(logger)
	
	policies, err := manager.GetCurrentPolicies()
	if err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to list policies: %v", err))
	}

	// Create network state with policies
	networkState := &client.NetworkState{
		Policies: policies,
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
		Result: &client.CommandResponse_Network{
			Network: &client.ResponseNetwork{
				Success:      true,
				Message:      "Policies listed successfully",
				NetworkState: networkState,
			},
		},
	}
}

// Policy persistence structures for netplan YAML
type NetplanPolicyConfig struct {
	Network NetplanPolicyNetwork `yaml:"network"`
}

type NetplanPolicyNetwork struct {
	Version   int                                `yaml:"version"`
	Renderer  string                             `yaml:"renderer"`
	Ethernets map[string]NetplanInterfaceConfig  `yaml:"ethernets,omitempty"`
}

type NetplanInterfaceConfig struct {
	RoutingPolicy []NetplanPolicyEntry `yaml:"routing-policy,omitempty"`
}

type NetplanPolicyEntry struct {
	From     string `yaml:"from,omitempty"`
	To       string `yaml:"to,omitempty"`
	Table    int    `yaml:"table"`
	Priority int    `yaml:"priority"`
}

// addPolicyToPersistentConfig adds policy to netplan persistent configuration
func (pm *PolicyManager) addPolicyToPersistentConfig(policy *client.RoutingPolicy) error {
	// Use interface field from policy
	interfaceName := policy.Interface
	
	if interfaceName == "" {
		pm.logger.Debug("Policy has no interface specified for netplan persistence")
		return fmt.Errorf("policy must specify interface for netplan persistence")
	}
	
	pm.logger.Debugf("Using interface %s for netplan policy persistence", interfaceName)

	policyFile := fmt.Sprintf("%s/99-elchi-policy-%s.yaml", models.NetplanPath, interfaceName)
	pm.logger.Debugf("Policy file path: %s", policyFile)

	// Load existing config
	config := &NetplanPolicyConfig{
		Network: NetplanPolicyNetwork{
			Version:   2,
			Renderer:  "networkd",
			Ethernets: make(map[string]NetplanInterfaceConfig),
		},
	}

	// Load existing file if it exists
	if data, err := os.ReadFile(policyFile); err == nil {
		pm.logger.Debugf("Loading existing policy file: %s", policyFile)
		if err := yaml.Unmarshal(data, config); err != nil {
			pm.logger.Debugf("Failed to parse existing policy file: %v", err)
		}
	} else {
		pm.logger.Debugf("Policy file does not exist, will create new: %s", policyFile)
	}

	// Initialize interface config if it doesn't exist
	if config.Network.Ethernets == nil {
		config.Network.Ethernets = make(map[string]NetplanInterfaceConfig)
	}

	ifConfig, exists := config.Network.Ethernets[interfaceName]
	if !exists {
		ifConfig = NetplanInterfaceConfig{
			RoutingPolicy: []NetplanPolicyEntry{},
		}
	}

	// Convert client policy to netplan format
	netplanPolicy := NetplanPolicyEntry{
		From:     policy.From,
		To:       policy.To,
		Table:    int(policy.Table),
		Priority: int(policy.Priority),
	}

	// Check if policy already exists (avoid duplicates)
	pm.logger.Debugf("Checking for duplicate policy in %d existing policies", len(ifConfig.RoutingPolicy))
	for _, existingPolicy := range ifConfig.RoutingPolicy {
		if existingPolicy.From == netplanPolicy.From &&
		   existingPolicy.To == netplanPolicy.To &&
		   existingPolicy.Table == netplanPolicy.Table &&
		   existingPolicy.Priority == netplanPolicy.Priority {
			pm.logger.Debug("Policy already exists in netplan, skipping")
			return nil // Policy already exists
		}
	}

	// Add the new policy
	pm.logger.Debug("Adding new policy to netplan config")
	ifConfig.RoutingPolicy = append(ifConfig.RoutingPolicy, netplanPolicy)
	config.Network.Ethernets[interfaceName] = ifConfig
	pm.logger.Debugf("Interface %s now has %d policies", interfaceName, len(ifConfig.RoutingPolicy))

	// Write back to file
	pm.logger.Debug("Marshaling policy config to YAML")
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal policy config: %v", err)
	}

	// Use tee with sudo to write file directly as root (bypass ownership issues)
	pm.logger.Debug("Writing policy config file with sudo tee")
	cmd := exec.Command("sudo", "tee", policyFile)
	cmd.Stdin = strings.NewReader(string(data))
	if err := cmd.Run(); err != nil {
		pm.logger.Debugf("Failed to write policy config: %v", err)
		return fmt.Errorf("failed to write policy config via sudo tee: %v", err)
	}
	pm.logger.Debug("Policy config file written successfully")

	// Set proper permissions  
	chmodCmd := exec.Command("sudo", "chmod", "0600", policyFile)
	if err := chmodCmd.Run(); err != nil {
		pm.logger.Warnf("Failed to set permissions for %s: %v", policyFile, err)
	}

	pm.logger.Infof("Policy persisted to %s for interface %s", policyFile, interfaceName)
	return nil
}


// removePolicyFromPersistentConfig removes policy from netplan persistent configuration
func (pm *PolicyManager) removePolicyFromPersistentConfig(policy *client.RoutingPolicy) error {
	// Use interface field from policy
	interfaceName := policy.Interface
	
	if interfaceName == "" {
		pm.logger.Debug("Policy has no interface specified for netplan removal")
		return fmt.Errorf("policy must specify interface for netplan removal")
	}

	policyFile := fmt.Sprintf("%s/99-elchi-policy-%s.yaml", models.NetplanPath, interfaceName)
	pm.logger.Debugf("Removing policy from file: %s", policyFile)

	// Load existing config
	config := &NetplanPolicyConfig{
		Network: NetplanPolicyNetwork{
			Version:   2,
			Renderer:  "networkd",
			Ethernets: make(map[string]NetplanInterfaceConfig),
		},
	}

	// Load existing file
	if data, err := os.ReadFile(policyFile); err != nil {
		pm.logger.Debug("Policy file does not exist, nothing to remove")
		return nil // File doesn't exist, nothing to remove
	} else {
		pm.logger.Debug("Loading existing policy file for removal")
		if err := yaml.Unmarshal(data, config); err != nil {
			pm.logger.Debugf("Failed to parse policy file: %v", err)
			return fmt.Errorf("failed to parse existing policy config: %v", err)
		}
	}

	// Get interface config
	ifConfig, exists := config.Network.Ethernets[interfaceName]
	if !exists {
		pm.logger.Debug("Interface config doesn't exist in policy file")
		return nil // Interface config doesn't exist
	}
	pm.logger.Debugf("Found interface config with %d policies", len(ifConfig.RoutingPolicy))

	// Remove matching policies
	var filteredPolicies []NetplanPolicyEntry
	removedCount := 0
	for _, existingPolicy := range ifConfig.RoutingPolicy {
		if existingPolicy.From == policy.From &&
		   existingPolicy.To == policy.To &&
		   existingPolicy.Table == int(policy.Table) &&
		   existingPolicy.Priority == int(policy.Priority) {
			pm.logger.Debug("Found matching policy to remove")
			removedCount++
			continue // Skip this policy (remove it)
		}
		filteredPolicies = append(filteredPolicies, existingPolicy)
	}
	pm.logger.Debugf("Removed %d matching policies, %d remaining", removedCount, len(filteredPolicies))

	ifConfig.RoutingPolicy = filteredPolicies

	// Update or remove the config
	if len(filteredPolicies) == 0 {
		// Remove interface config if no policies left
		pm.logger.Debug("No policies left, removing interface config")
		delete(config.Network.Ethernets, interfaceName)
		
		// If no interfaces left, remove the entire file
		if len(config.Network.Ethernets) == 0 {
			pm.logger.Debug("No interfaces left, removing entire policy file")
			if err := os.Remove(policyFile); err != nil && !os.IsNotExist(err) {
				pm.logger.Debugf("Failed to remove policy file: %v", err)
				return fmt.Errorf("failed to remove empty policy config: %v", err)
			}
			pm.logger.Infof("Removed empty policy config file %s", policyFile)
			return nil
		}
	} else {
		// Update the interface config
		pm.logger.Debug("Updating interface config with remaining policies")
		config.Network.Ethernets[interfaceName] = ifConfig
	}

	// Write back to file
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal policy config: %v", err)
	}

	// Use tee with sudo to write file directly as root (bypass ownership issues)
	pm.logger.Debug("Writing updated policy config file with sudo tee")
	cmd := exec.Command("sudo", "tee", policyFile)
	cmd.Stdin = strings.NewReader(string(data))
	if err := cmd.Run(); err != nil {
		pm.logger.Debugf("Failed to write updated policy config: %v", err)
		return fmt.Errorf("failed to write policy config via sudo tee: %v", err)
	}
	pm.logger.Debug("Updated policy config file written successfully")

	// Set proper permissions  
	chmodCmd := exec.Command("sudo", "chmod", "0600", policyFile)
	if err := chmodCmd.Run(); err != nil {
		pm.logger.Warnf("Failed to set permissions for %s: %v", policyFile, err)
	}

	pm.logger.Infof("Policy removed from persistent config %s for interface %s", policyFile, interfaceName)
	return nil
}

// buildPolicyInterfaceMap builds a map of policies to interfaces from netplan files
func (pm *PolicyManager) buildPolicyInterfaceMap() map[string]string {
	interfaceMap := make(map[string]string)
	
	// Scan netplan policy files (99-elchi-policy-*.yaml)
	pm.logger.Debugf("Scanning netplan directory: %s", models.NetplanPath)
	files, err := os.ReadDir(models.NetplanPath)
	if err != nil {
		pm.logger.Warnf("Failed to read netplan directory: %v", err)
		return interfaceMap
	}
	pm.logger.Debugf("Found %d files in netplan directory", len(files))
	
	for _, file := range files {
		if !strings.HasPrefix(file.Name(), "99-elchi-policy-") || !strings.HasSuffix(file.Name(), ".yaml") {
			pm.logger.Debugf("Skipping non-policy file: %s", file.Name())
			continue
		}
		
		// Extract interface name from filename: 99-elchi-policy-eth0.yaml -> eth0
		interfaceName := strings.TrimPrefix(file.Name(), "99-elchi-policy-")
		interfaceName = strings.TrimSuffix(interfaceName, ".yaml")
		pm.logger.Debugf("Processing policy file for interface: %s", interfaceName)
		
		filePath := filepath.Join(models.NetplanPath, file.Name())
		pm.parsePolicyFile(filePath, interfaceName, interfaceMap)
	}
	
	pm.logger.Debugf("Built interface map with %d entries", len(interfaceMap))
	return interfaceMap
}

// parsePolicyFile parses a netplan policy file and adds policies to interface map
func (pm *PolicyManager) parsePolicyFile(filePath, interfaceName string, interfaceMap map[string]string) {
	pm.logger.Debugf("Parsing policy file: %s", filePath)
	data, err := os.ReadFile(filePath)
	if err != nil {
		pm.logger.Warnf("Failed to read policy file %s: %v", filePath, err)
		return
	}
	
	var config NetplanPolicyConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		pm.logger.Warnf("Failed to parse policy file %s: %v", filePath, err)
		return
	}
	
	// Extract policies for this interface
	if ifConfig, exists := config.Network.Ethernets[interfaceName]; exists {
		pm.logger.Debugf("Found %d policies for interface %s", len(ifConfig.RoutingPolicy), interfaceName)
		for _, policy := range ifConfig.RoutingPolicy {
			// Create a unique key for this policy (from+to+table+priority)
			policyKey := pm.createPolicyKey(policy.From, policy.To, policy.Table, policy.Priority)
			interfaceMap[policyKey] = interfaceName
			pm.logger.Debugf("Added policy key %s -> interface %s", policyKey, interfaceName)
		}
	} else {
		pm.logger.Debugf("No interface config found for %s in file %s", interfaceName, filePath)
	}
}

// findInterfaceForPolicy finds the interface name for a given policy
func (pm *PolicyManager) findInterfaceForPolicy(policy *client.RoutingPolicy, interfaceMap map[string]string) string {
	policyKey := pm.createPolicyKey(policy.From, policy.To, int(policy.Table), int(policy.Priority))
	return interfaceMap[policyKey]
}

// createPolicyKey creates a unique key for a policy
func (pm *PolicyManager) createPolicyKey(from, to string, table, priority int) string {
	return fmt.Sprintf("%s|%s|%d|%d", from, to, table, priority)
}