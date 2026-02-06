package network

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

const (
	ElchiTableFile  = "/var/lib/elchi/rt_tables.conf"        // Elchi storage location
	KernelTableLink = "/etc/iproute2/rt_tables.d/elchi.conf" // Kernel reads from here
	MinTableID      = 100                                    // Elchi-managed tables start from 100
	MaxTableID      = 999                                    // Elchi-managed tables end at 999
)

type TableManager struct {
	logger    *logger.Logger
	tablePath string
}

func NewTableManager(logger *logger.Logger) *TableManager {
	return &TableManager{
		logger:    logger,
		tablePath: ElchiTableFile,
	}
}

// ManageRoutingTables processes routing table definitions (bulk update)
func (tm *TableManager) ManageRoutingTables(tables []*client.RoutingTableDefinition) error {
	tm.logger.Info("Managing routing table definitions (bulk update)")

	// Filter only Elchi-managed tables (ID range 100-999)
	var elchiTables []*client.RoutingTableDefinition
	for _, table := range tables {
		if table.Id >= MinTableID && table.Id <= MaxTableID {
			elchiTables = append(elchiTables, table)
		} else {
			tm.logger.Warnf("Skipping table %d (%s): outside Elchi management range", table.Id, table.Name)
		}
	}

	if len(elchiTables) == 0 {
		tm.logger.Info("No Elchi-managed tables to process")
		return nil
	}

	// Write tables to rt_tables.d/elchi.conf
	if err := tm.writeTableDefinitions(elchiTables); err != nil {
		return fmt.Errorf("failed to write table definitions: %w", err)
	}

	tm.logger.Infof("Successfully processed %d routing table definitions", len(elchiTables))
	return nil
}

// ManageTableOperations processes individual table operations (add/delete/replace)
func (tm *TableManager) ManageTableOperations(operations []*client.TableOperation) error {
	tm.logger.Info("Managing table operations")

	for _, op := range operations {
		switch op.Action {
		case client.TableOperation_ADD:
			if err := tm.addTable(op.Table); err != nil {
				return fmt.Errorf("failed to add table: %w", err)
			}
		case client.TableOperation_DELETE:
			if err := tm.deleteTable(op.Table); err != nil {
				return fmt.Errorf("failed to delete table: %w", err)
			}
		case client.TableOperation_REPLACE:
			if err := tm.replaceTable(op.Table); err != nil {
				return fmt.Errorf("failed to replace table: %w", err)
			}
		}
	}

	return nil
}

// GetCurrentTables returns current routing table definitions
func (tm *TableManager) GetCurrentTables() ([]*client.RoutingTableDefinition, error) {
	// Read system default tables
	defaultTables := []*client.RoutingTableDefinition{
		{Id: 0, Name: "unspec"},
		{Id: 253, Name: "default"},
		{Id: 254, Name: "main"},
		{Id: 255, Name: "local"},
	}

	// Read Elchi-managed tables from file
	elchiTables, err := tm.readTableDefinitions()
	if err != nil {
		tm.logger.Warnf("Failed to read Elchi table definitions: %v", err)
		return defaultTables, nil // Return at least default tables
	}

	// Combine default and Elchi tables
	allTables := append(defaultTables, elchiTables...)
	return allTables, nil
}

// writeTableDefinitions writes table definitions to /var/lib/elchi/rt_tables.conf
func (tm *TableManager) writeTableDefinitions(tables []*client.RoutingTableDefinition) error {
	// Ensure directory exists
	dir := filepath.Dir(tm.tablePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create tables directory: %w", err)
	}

	// Build file content
	var content strings.Builder
	content.WriteString("# Elchi-managed routing tables\n")
	content.WriteString("# Generated automatically - do not edit manually\n\n")

	for _, table := range tables {
		// Validate table name (no spaces, alphanumeric + underscore/hyphen)
		if !isValidTableName(table.Name) {
			return fmt.Errorf("invalid table name '%s': must contain only letters, numbers, underscore, and hyphen", table.Name)
		}

		content.WriteString(fmt.Sprintf("%d\t%s\n", table.Id, table.Name))
	}

	// Write to file
	if err := os.WriteFile(tm.tablePath, []byte(content.String()), 0644); err != nil {
		return fmt.Errorf("failed to write table file: %w", err)
	}

	// Create symbolic link for kernel access, or fallback to dual write
	if err := tm.createKernelLink(); err != nil {
		tm.logger.Warnf("Failed to create kernel symlink: %v", err)
		// Fallback: write directly to kernel location
		if err := tm.writeKernelFile(content.String()); err != nil {
			tm.logger.Warnf("Failed to write kernel file: %v", err)
			// Don't fail the operation, our storage file is written successfully
		}
	}

	tm.logger.Infof("Written %d table definitions to %s", len(tables), tm.tablePath)
	return nil
}

// readTableDefinitions reads table definitions from rt_tables.d/elchi.conf
func (tm *TableManager) readTableDefinitions() ([]*client.RoutingTableDefinition, error) {
	data, err := os.ReadFile(tm.tablePath)
	if os.IsNotExist(err) {
		return []*client.RoutingTableDefinition{}, nil // Empty list if file doesn't exist
	}
	if err != nil {
		return nil, err
	}

	var tables []*client.RoutingTableDefinition
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}

		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue // Skip malformed lines
		}

		tableID, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			continue // Skip lines with invalid table ID
		}

		tables = append(tables, &client.RoutingTableDefinition{
			Id:   uint32(tableID),
			Name: parts[1],
		})
	}

	return tables, nil
}

// isValidTableName checks if table name is valid (alphanumeric + underscore/hyphen)
func isValidTableName(name string) bool {
	if name == "" {
		return false
	}

	for _, r := range name {
		if !(r >= 'a' && r <= 'z') &&
			!(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') &&
			r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// addTable adds a single table to the configuration
func (tm *TableManager) addTable(table *client.RoutingTableDefinition) error {
	if table == nil {
		return fmt.Errorf("table definition is nil")
	}

	// Validate table ID range
	if table.Id < MinTableID || table.Id > MaxTableID {
		return fmt.Errorf("table ID %d outside Elchi management range (%d-%d)", table.Id, MinTableID, MaxTableID)
	}

	// Validate table name
	if !isValidTableName(table.Name) {
		return fmt.Errorf("invalid table name '%s': must contain only letters, numbers, underscore, and hyphen", table.Name)
	}

	tm.logger.Infof("Adding table: ID=%d, Name=%s", table.Id, table.Name)

	// Read existing tables
	existingTables, err := tm.readTableDefinitions()
	if err != nil {
		return fmt.Errorf("failed to read existing tables: %w", err)
	}

	// Check if table already exists
	for _, existing := range existingTables {
		if existing.Id == table.Id {
			return fmt.Errorf("table with ID %d already exists", table.Id)
		}
		if existing.Name == table.Name {
			return fmt.Errorf("table with name '%s' already exists", table.Name)
		}
	}

	// Add new table
	existingTables = append(existingTables, table)

	// Write updated tables
	if err := tm.writeTableDefinitions(existingTables); err != nil {
		return fmt.Errorf("failed to write updated tables: %w", err)
	}

	tm.logger.Infof("Successfully added table %d (%s)", table.Id, table.Name)
	return nil
}

// deleteTable removes a single table from the configuration
func (tm *TableManager) deleteTable(table *client.RoutingTableDefinition) error {
	if table == nil {
		return fmt.Errorf("table definition is nil")
	}

	tm.logger.Infof("Deleting table: ID=%d, Name=%s", table.Id, table.Name)

	// Read existing tables
	existingTables, err := tm.readTableDefinitions()
	if err != nil {
		return fmt.Errorf("failed to read existing tables: %w", err)
	}

	// Filter out the table to delete
	var updatedTables []*client.RoutingTableDefinition
	found := false
	for _, existing := range existingTables {
		if existing.Id == table.Id {
			found = true
			continue // Skip this table (delete it)
		}
		updatedTables = append(updatedTables, existing)
	}

	if !found {
		tm.logger.Warnf("Table %d not found, nothing to delete", table.Id)
		return nil // Not an error, idempotent operation
	}

	// Write updated tables (or remove file if empty)
	if len(updatedTables) == 0 {
		// Remove the file if no tables left
		if err := os.Remove(tm.tablePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove empty table file: %w", err)
		}
		// Remove symbolic link too
		os.Remove(KernelTableLink)
		tm.logger.Info("Removed last table, deleted configuration file and symlink")
	} else {
		// Write remaining tables
		if err := tm.writeTableDefinitions(updatedTables); err != nil {
			return fmt.Errorf("failed to write updated tables: %w", err)
		}
	}

	tm.logger.Infof("Successfully deleted table %d (%s)", table.Id, table.Name)
	return nil
}

// replaceTable replaces an existing table definition
func (tm *TableManager) replaceTable(table *client.RoutingTableDefinition) error {
	if table == nil {
		return fmt.Errorf("table definition is nil")
	}

	// Validate table ID range
	if table.Id < MinTableID || table.Id > MaxTableID {
		return fmt.Errorf("table ID %d outside Elchi management range (%d-%d)", table.Id, MinTableID, MaxTableID)
	}

	// Validate table name
	if !isValidTableName(table.Name) {
		return fmt.Errorf("invalid table name '%s': must contain only letters, numbers, underscore, and hyphen", table.Name)
	}

	tm.logger.Infof("Replacing table: ID=%d, Name=%s", table.Id, table.Name)

	// Read existing tables
	existingTables, err := tm.readTableDefinitions()
	if err != nil {
		return fmt.Errorf("failed to read existing tables: %w", err)
	}

	// Replace the table with matching ID
	found := false
	for i, existing := range existingTables {
		if existing.Id == table.Id {
			existingTables[i] = table
			found = true
			break
		}
	}

	if !found {
		// If not found, add it (replace acts as add if not exists)
		existingTables = append(existingTables, table)
		tm.logger.Infof("Table %d not found, adding as new", table.Id)
	}

	// Write updated tables
	if err := tm.writeTableDefinitions(existingTables); err != nil {
		return fmt.Errorf("failed to write updated tables: %w", err)
	}

	tm.logger.Infof("Successfully replaced table %d (%s)", table.Id, table.Name)
	return nil
}

// createKernelLink creates symbolic link for kernel access
func (tm *TableManager) createKernelLink() error {
	// Remove existing link/file
	os.Remove(KernelTableLink)

	// Create symbolic link from kernel location to our storage location
	if err := os.Symlink(tm.tablePath, KernelTableLink); err != nil {
		return fmt.Errorf("failed to create symbolic link: %w", err)
	}

	tm.logger.Debugf("Created symbolic link: %s -> %s", KernelTableLink, tm.tablePath)
	return nil
}

// writeKernelFile writes directly to kernel location as fallback
func (tm *TableManager) writeKernelFile(_ string) error {
	// This is a fallback when symlink creation fails
	// We need sudo to write to /etc/iproute2/rt_tables.d/
	// For now, skip this and rely on symlink or bootstrap setup
	tm.logger.Debug("Kernel file write skipped - requires sudo privileges, use bootstrap symlink instead")
	return fmt.Errorf("kernel file write requires elevated privileges - run bootstrap to setup symlink")
}

// Command handlers for SUB_TABLE_* operations

// TableManage handles SUB_TABLE_MANAGE command
func TableManage(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if networkReq == nil {
		return helper.NewErrorResponse(cmd, "network request is nil")
	}

	// Check for table_operations field
	if len(networkReq.GetTableOperations()) == 0 {
		return helper.NewErrorResponse(cmd, "no table operations specified")
	}

	manager := NewTableManager(logger)

	if err := manager.ManageTableOperations(networkReq.GetTableOperations()); err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("table management failed: %v", err))
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
	}
}

// TableList handles SUB_TABLE_LIST command
func TableList(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	manager := NewTableManager(logger)

	tables, err := manager.GetCurrentTables()
	if err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to list tables: %v", err))
	}

	logger.Infof("Listed %d routing tables", len(tables))

	// Create network state with tables
	networkState := &client.NetworkState{
		RoutingTables: tables,
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
		Result: &client.CommandResponse_Network{
			Network: &client.ResponseNetwork{
				Success:      true,
				Message:      "Routing tables listed successfully",
				NetworkState: networkState,
			},
		},
	}
}
