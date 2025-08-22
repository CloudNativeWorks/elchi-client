# Elchi Client Network Operations - Controller Integration Guide

## 🎯 Executive Summary for Controller AI

The Elchi Client network management has been completely refactored from **per-interface file approach** to a **unified netplan-based architecture** with enhanced safety mechanisms. This guide provides everything you need to integrate with the new system.

## 📋 Quick Reference for Controller

### ✅ What Changed
- **FROM**: Individual interface files (`50-elchi-if-*.yaml`, `50-elchi-r-*.yaml`) 
- **TO**: Unified netplan config (`99-elchi-interfaces.yaml`) + dynamic operations
- **ADDED**: Connection monitoring, automatic rollback, safety mechanisms

### ✅ New API Endpoints
| Operation | SubCommand | Purpose |
|-----------|------------|---------|
| Interface Config | `SUB_NETPLAN_APPLY` | Apply complete network config |
| Route Management | `SUB_ROUTE_MANAGE` | Add/delete/modify routes |
| Policy Management | `SUB_POLICY_MANAGE` | Routing policy operations |
| State Query | `SUB_GET_NETWORK_STATE` | Get complete network state |

---

## 🚀 Integration Quick Start

### 1. **Netplan Configuration (Primary Interface Management)**

**NEW APPROACH**: Send complete YAML configuration for all interfaces

```protobuf
// Send this message via SUB_NETPLAN_APPLY
message RequestNetwork {
  NetplanConfig netplan_config = 1;
}

message NetplanConfig {
  string yaml_content = 1;                    // Complete netplan YAML
  bool test_mode = 2;                        // Enable safety testing
  uint32 test_timeout_seconds = 3;           // Test duration (default: 10)
  bool preserve_controller_connection = 4;    // Monitor connection during apply
}
```

**Example YAML Content:**
```yaml
network:
  version: 2
  ethernets:
    eth0:
      dhcp4: false
      addresses:
        - 10.0.1.100/24
      routes:
        - to: 0.0.0.0/0
          via: 10.0.1.1
      routing-policy:
        - from: 10.0.1.0/24
          table: 101
          priority: 100
    eth1:
      dhcp4: true
```

### 2. **Route Management (Independent Operations)**

**Dynamic route operations without touching interface config:**

```protobuf
// Send this via SUB_ROUTE_MANAGE
message RequestNetwork {
  repeated RouteOperation route_operations = 1;
}

message RouteOperation {
  enum Action { ADD = 0; DELETE = 1; REPLACE = 2; }
  Action action = 1;
  Route route = 2;
}

message Route {
  string to = 1;        // "10.0.0.0/24" or "0.0.0.0/0"
  string via = 2;       // Gateway IP
  string interface = 3; // Interface name
  uint32 table = 4;     // Routing table ID (0 = main)
  uint32 metric = 5;    // Route metric
  string scope = 6;     // "global", "link", "host"
}
```

### 3. **Policy Management (Independent Operations)**

**Routing policy management with priority-based rules:**

```protobuf
// Send this via SUB_POLICY_MANAGE  
message RequestNetwork {
  repeated RoutingPolicyOperation policy_operations = 1;
}

message RoutingPolicyOperation {
  enum Action { ADD = 0; DELETE = 1; REPLACE = 2; }
  Action action = 1;
  RoutingPolicy policy = 2;
}

message RoutingPolicy {
  string from = 1;      // Source network
  string to = 2;        // Destination network  
  uint32 table = 3;     // Routing table ID
  uint32 priority = 4;  // Rule priority (100-999 for Elchi)
  string iif = 5;       // Input interface
  string oif = 6;       // Output interface
}
```

### 4. **Network State Query**

**Get complete network state for monitoring:**

```protobuf
// Send SUB_GET_NETWORK_STATE (no parameters needed)
// Returns:
message ResponseNetwork {
  bool success = 1;
  string message = 2;
  NetworkState network_state = 3;
  string current_yaml = 4;  // Current netplan config
}

message NetworkState {
  repeated InterfaceState interfaces = 1;
  repeated Route routes = 2;
  repeated RoutingPolicy policies = 3;
  repeated RoutingTableDefinition routing_tables = 4;
  string current_netplan_yaml = 5;
}
```

---

## 🛡️ Safety Mechanisms (CRITICAL FOR CONTROLLER)

### **Connection Monitoring**
- Client monitors controller connection during network changes
- Multiple detection methods: gRPC health check, TCP port check, ICMP ping
- Automatic rollback if connection lost during apply

### **Test Mode**
```protobuf
NetplanConfig {
  test_mode: true,                    // Enable test mode
  test_timeout_seconds: 10,           // Test for 10 seconds
  preserve_controller_connection: true // Monitor connection
}
```

### **Rollback Mechanisms**
- Automatic backup before any change
- Connection loss = immediate rollback
- Test mode failure = automatic rollback
- Manual rollback via `SUB_NETPLAN_ROLLBACK`

---

## 📊 Migration Strategy for Controller

### **Phase 1: Update Message Handling**

**OLD Way (Remove this):**
```go
// Don't do this anymore
req := &RequestNetwork{
  Interfaces: []*Interface{
    {Ifname: "eth0", Dhcp4: false, Addresses: []string{"10.0.1.100/24"}}
  }
}
```

**NEW Way (Implement this):**
```go
// Use unified netplan approach
yamlContent := `
network:
  version: 2
  ethernets:
    eth0:
      dhcp4: false
      addresses: ["10.0.1.100/24"]
`

req := &RequestNetwork{
  NetplanConfig: &NetplanConfig{
    YamlContent: yamlContent,
    TestMode: true,
    PreserveControllerConnection: true,
  }
}
```

### **Phase 2: Implement Safety Checks**

**Connection Monitoring:**
```go
// Always use safety mode for production changes
config := &NetplanConfig{
  YamlContent: yamlContent,
  TestMode: true,                        // Enable testing
  TestTimeoutSeconds: 10,                // 10 second test window
  PreserveControllerConnection: true,     // Monitor our connection
}
```

**Error Handling:**
```go
response := client.NetworkService(cmd)
if !response.Success {
  // Check if rollback was triggered
  if strings.Contains(response.Error, "rollback") {
    log.Error("Network change failed, automatic rollback performed")
    // Implement fallback strategy
  }
}
```

### **Phase 3: Route/Policy Operations**

**Dynamic Route Management:**
```go
// Add a route without touching interface config
routeOps := []*RouteOperation{
  {
    Action: RouteOperation_ADD,
    Route: &Route{
      To: "192.168.0.0/24",
      Via: "10.0.1.1", 
      Interface: "eth0",
      Table: 101,
    },
  },
}

req := &RequestNetwork{RouteOperations: routeOps}
```

---

## ⚠️ Critical Controller Considerations

### **1. Connection Safety**
```yaml
# ALWAYS use these settings for production:
netplan_config:
  test_mode: true
  preserve_controller_connection: true
  test_timeout_seconds: 10
```

### **2. Single Interface Scenarios**
- Client handles single interface networks safely
- Controller connection interface is automatically preserved
- No special handling needed from controller side

### **3. Bootstrap Changes**
- Bootstrap no longer splits interfaces
- Routing table management moved to controller
- Legacy `define_routing_tables()` removed

### **4. File Management**
- No more per-interface files (`50-elchi-if-*.yaml`)
- Single unified config file (`99-elchi-interfaces.yaml`)
- Controller doesn't need to manage individual files

---

## 🔧 Testing & Validation

### **Test Network Changes Safely:**

```go
// 1. Test configuration first
testConfig := &NetplanConfig{
  YamlContent: newConfig,
  TestMode: true,
  TestTimeoutSeconds: 5,
  PreserveControllerConnection: true,
}

response := client.NetworkService(&Command{
  SubType: SUB_NETPLAN_APPLY,
  Network: &RequestNetwork{NetplanConfig: testConfig},
})

// 2. If test passes, apply permanently
if response.Success {
  prodConfig := &NetplanConfig{
    YamlContent: newConfig,
    TestMode: false,  // Permanent application
    PreserveControllerConnection: true,
  }
  // Apply permanent config
}
```

### **Query Current State:**
```go
// Get complete network state
response := client.NetworkService(&Command{
  SubType: SUB_GET_NETWORK_STATE,
})

networkState := response.GetNetwork().GetNetworkState()
// Access interfaces, routes, policies, current YAML
```

---

## 🚨 Breaking Changes Alert

### **Removed Functionality:**
- ❌ Per-interface file management
- ❌ `SUB_ADD_INTERFACE_ROUTE` (use `SUB_ROUTE_MANAGE`)  
- ❌ `SUB_ADD_INTERFACE_POLICY` (use `SUB_POLICY_MANAGE`)
- ❌ Individual interface update commands

### **Proto Field Changes:**
- ⚠️ `Route.table` and `Route.metric` are now `uint32` (not pointers)
- ⚠️ Zero values instead of nil for optional fields

### **New Required Fields:**
- ✅ `NetplanConfig.yaml_content` is required for netplan operations
- ✅ Use safety flags for production environments

---

## 📞 Controller Implementation Checklist

### **Phase 1: Basic Integration**
- [ ] Update proto message handling for new structure
- [ ] Implement unified YAML generation
- [ ] Replace per-interface calls with `SUB_NETPLAN_APPLY`

### **Phase 2: Safety Implementation** 
- [ ] Always use `test_mode: true` for production changes
- [ ] Enable `preserve_controller_connection: true`
- [ ] Implement rollback detection in error handling

### **Phase 3: Advanced Operations**
- [ ] Use `SUB_ROUTE_MANAGE` for dynamic route changes
- [ ] Use `SUB_POLICY_MANAGE` for routing policies  
- [ ] Implement `SUB_GET_NETWORK_STATE` for monitoring

### **Phase 4: Testing & Validation**
- [ ] Test single interface scenarios
- [ ] Test connection loss during apply
- [ ] Verify automatic rollback functionality
- [ ] Test route/policy operations independently

---

## 🎯 Success Criteria

Your controller integration is successful when:

✅ **Network changes don't break controller connection**
✅ **Failed changes automatically rollback** 
✅ **Single interface networks work correctly**
✅ **Route/policy changes work independently**
✅ **Complete network state can be queried**

---

## 📧 Support

This new architecture is production-ready and includes comprehensive safety mechanisms. The client handles all edge cases and provides detailed error messages for troubleshooting.

**Key Benefits for Controller:**
- 🛡️ **Safe**: Connection monitoring prevents network loss
- 🔄 **Reliable**: Automatic rollback on failures  
- 🎯 **Simple**: Unified API, fewer edge cases
- 📊 **Observable**: Complete state visibility
- ⚡ **Fast**: Dynamic operations without file management

The client is ready for immediate integration with these new patterns.