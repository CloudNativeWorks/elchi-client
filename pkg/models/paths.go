package models

const (
	BootstrapsPath  = "/var/lib/elchi/bootstraps"
	NetplanPath     = "/etc/netplan"
	SystemdPath     = "/usr/lib/systemd/system"
	SystemdRootPath = "/etc/systemd"
	JournalLogPath  = "/var/log/journal"
	ElchiPath       = "/etc/elchi"
	ElchiLibPath    = "/var/lib/elchi"
	// ShieldConfigPath is elchi-shield's watched config directory. The agent syncs
	// the control-plane's config bundle here; shield self-watches it (fsnotify +
	// atomic hot-reload). Must equal shield's --config-dir. ShieldFile.path values
	// in the bundle are relative to this root (e.g. "api-public.yaml", "feeds/x.json").
	ShieldConfigPath = "/etc/elchi/elchi-shield"
	// ShieldHTTPAddr is elchi-shield's loopback management HTTP endpoint (its
	// --http-addr / ELCHI_SHIELD_HTTP_ADDR default). The agent polls /configz and
	// /metrics here to confirm a config push actually loaded. Must equal shield's
	// --http-addr.
	ShieldHTTPAddr = "127.0.0.1:9001"
	EtcPath        = "/etc"
	UsrPath        = "/usr"
	DevPath        = "/dev/"
	ProcPath       = "/proc"

	MachineID         = "/etc/machine-id"
	OsRelease         = "/etc/os-release"
	ProcVersion       = "/proc/version"
	ProcUptime        = "/proc/uptime"
	ProcNetTcp        = "/proc/net/tcp"
	ProcNetUdp        = "/proc/net/udp"
	ProcMounts        = "/proc/mounts"
	ProcMemInfo       = "/proc/meminfo"
	ProcDiskStats     = "/proc/diskstats"
	ProcLoadavg       = "/proc/loadavg"
	ProcStat          = "/proc/stat"
	NetDir            = "/sys/class/net"
	CpuTemp           = "/sys/class/thermal/thermal_zone0/temp"
	InterfaceTableMap = "/etc/iproute2/rt_tables.d/elchi.conf"
)
