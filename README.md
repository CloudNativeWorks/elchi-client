# Elchi Client

A professional Go client that connects to a remote server via GRPC and executes commands.

## Features

- GRPC connectivity to a remote server
- Configurable settings (YAML, environment variables)
- Command execution
- Health check
- Automatic reconnection

## Requirements

- Go 1.19 or higher
- Protocol Buffers (protoc)

## Installation

### Dependencies

```bash
go mod tidy
```

### Building

```bash
go build -o bin/elchi-client ./cmd/client
```

## Usage

### Configuration

Configuration can be specified in the following ways (in order of precedence):

1. Command line arguments
2. Environment variables
3. Configuration file

#### Configuration File

```yaml
server:
  host: "0.0.0.0"
  port: 50051
  timeout: "30s"

logging:
  level: "info"
  format: "json"
  output_path: "stdout"

client:
  id: ""  # Will be auto-generated as UUID if empty
  version: "1.0.0"

metadata:
  environment: "development"
  region: "eu-west-1"
  role: "client"
```

#### Environment Variables

All configuration values can be specified as environment variables with the `ELCHI_` prefix. For example:

```
ELCHI_CLIENT_CONNECT_ADDRESS=10.0.0.1
ELCHI_CLIENT_CONNECT_PORT=50052
ELCHI_LOGGING_LEVEL=debug
```

### Running

```bash
# Run with default configuration
./bin/elchi-client

# Run with a specific configuration file
./bin/elchi-client -config config.yaml

# Specify connection address
./bin/elchi-client -address 10.0.0.1 -port 50052

# Specify log level
./bin/elchi-client -log-level debug
```

## Development

### Generating Go code from Proto

```bash
protoc --go_out=internal/client/ --go-grpc_out=internal/client/ api/proto/client.proto
```

## License

This project is licensed under the MIT License - see the LICENSE file for details. 
