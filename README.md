# Elchi Client

**Elchi Client** is an enterprise-level network management client that connects to remote servers via gRPC protocol to manage FRR routing, network configuration, service deployment, and monitoring operations.

## 🚀 Key Features

- **Secure gRPC Communication**: TLS-enabled client-server communication
- **FRR (Free Range Routing) Management**: BGP neighbor and route policy management
- **Network Configuration**: Netplan interface and route management
- **Service Deployment**: Systemd service control and monitoring
- **Circuit Breaker Pattern**: Automatic reconnection and failure recovery
- **Performance Optimized**: Load balancer and proxy workload optimizations

## 📋 Requirements

- **Operating System**: Ubuntu 24.04 LTS (minimum)
- **Architecture**: `linux/amd64`. The published installer (mirrored through
  `CloudNativeWorks/elchi-archive`) ships amd64 binaries only, matching the rest
  of the elchi stack; `arm64` must be built from source.
- **Go**: 1.21 or higher (for development)
- **System**: Root privileges for installation
- **Network**: Internet access for dependencies

## 🔧 Installation

### Quick Installation (Recommended)

```bash
# Download and run bootstrap script
curl -fsSL https://raw.githubusercontent.com/CloudNativeWorks/elchi-client/main/elchi-install.sh | sudo bash

# For BGP/FRR support
curl -fsSL https://raw.githubusercontent.com/CloudNativeWorks/elchi-client/main/elchi-install.sh | sudo bash -s -- --enable-bgp
```

> **Bundled elchi-shield sidecar.** The installer also installs **elchi-shield**
> (the Envoy `ext_proc` API-security / WAF sidecar) on the same edge host, in the
> same run — it lives next to the client on the data plane, never on the control
> plane. The shield binary is fetched from the **public elchi-archive mirror**
> (the same release as the installer), not from a private source repo. Skip it
> with `--no-shield`. Two separate configs, both editable on the host:
>
> - **Sink config — ClickHouse + OTel addresses:** `/etc/elchi/elchi-shield/config.yaml`
>   (shield's `--config-file`; mode `0600`, holds the DSN). Seed it at install with
>   `--shield-audit-dsn=` / `--shield-metrics-otlp=` (both off by default), or just
>   edit the file afterwards and `systemctl restart elchi-shield`. Re-running the
>   installer preserves your edits. Shape:
>   ```yaml
>   audit:
>     exporter: clickhouse                 # none | clickhouse | otel
>     clickhouse_dsn: "clickhouse://user:pass@CH-HOST:9000/elchi"
>     clickhouse_ttl_days: 7
>   metrics:
>     otlp_endpoint: "OTEL-HOST:4317"      # empty = /metrics scrape only
>     otlp_insecure: false
>   ```
> - **Security policies:** `/etc/elchi/elchi-shield/conf.d/*.yaml` (watched,
>   hot-reloaded). Pushed by the control plane via the agent; or drop a policy
>   file there yourself (see the elchi-shield repo's `configs/examples/`). The
>   control plane (elchi-backend) is what wires the Envoy `ext_proc` filter.

## ⚙️ Configuration

### Configuration File (`/etc/elchi/config.yaml`)

```yaml
server:
  host: "backend.elchi.io"
  port: 443
  tls: true
  insecure_skip_verify: true # Set to false when using trusted TLS certificates
  token: "your-authentication-token"
  timeout: "30s"

logging:
  level: "info"
  format: "json"
  modules:
    client: "info"
    grpc: "info"
    frr: "info"
    network: "debug"

client:
  name: "production-client-01"
  cloud: "my-cloud"
  bgp: false
```

## 🚀 Usage

### Start Client Service

```bash
# Start as systemd service (recommended)
sudo systemctl start elchi-client
sudo systemctl enable elchi-client

# Check status
sudo systemctl status elchi-client

# View logs
sudo journalctl -u elchi-client -f
```

## 🐛 Troubleshooting

### Common Issues

**Connection Failed**
```bash
# Check network connectivity
ping backend.elchi.io
telnet backend.elchi.io 443

# Verify TLS configuration
openssl s_client -connect backend.elchi.io:443
```

### Log Analysis

Enable debug logging to investigate issues:

```bash
# Edit config.yaml
logging:
  level: "debug"
  modules:
    client: "debug"
    grpc: "debug"

# Or use environment variable
ELCHI_LOGGING_LEVEL=debug systemctl restart elchi-client
```

## 📞 Support

- **Issues**: [GitHub Issues](https://github.com/CloudNativeWorks/elchi-client/issues)
- **License**: MIT License

## 🔄 Updates

The installer downloads the client (and the bundled shield sidecar) from the
public `CloudNativeWorks/elchi-archive` mirror during bootstrap. To update,
re-run the installer — it pulls the current mirrored release:

```bash
curl -fsSL https://raw.githubusercontent.com/CloudNativeWorks/elchi-client/main/elchi-install.sh | sudo bash
```

The latest mirrored versions are listed in the archive's `index.json`
(`elchi_client_releases` / `elchi_shield_releases`). 
