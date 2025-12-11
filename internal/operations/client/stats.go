package client

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	proto "github.com/CloudNativeWorks/elchi-proto/client"
)

var (
	lastTotalCPU float64
	lastIdleCPU  float64
)

// CollectSystemStats collects all system statistics
func CollectSystemStats() (*proto.ResponseClientStats, error) {
	stats := &proto.ResponseClientStats{}
	var err error

	// Collect CPU stats
	stats.Cpu, err = collectCPUStats()
	if err != nil {
		return nil, fmt.Errorf("CPU stats collection failed: %v", err)
	}

	// Collect Memory stats
	stats.Memory, err = collectMemoryStats()
	if err != nil {
		return nil, fmt.Errorf("memory stats collection failed: %v", err)
	}

	// Collect Disk stats
	stats.Disk, err = collectDiskStats()
	if err != nil {
		return nil, fmt.Errorf("disk stats collection failed: %v", err)
	}

	// Collect Network stats
	stats.Network, err = collectNetworkStats()
	if err != nil {
		return nil, fmt.Errorf("network stats collection failed: %v", err)
	}

	// Collect System info
	stats.System, err = collectSystemInfo()
	if err != nil {
		return nil, fmt.Errorf("system info collection failed: %v", err)
	}

	return stats, nil
}

func collectCPUStats() (*proto.CPUStats, error) {
	stats := &proto.CPUStats{
		CoreStats: make(map[string]float64),
	}

	file, err := os.Open(models.ProcStat)
	if err != nil {
		return stats, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 0 {
			if fields[0] == "cpu" {
				total := 0.0
				idle := 0.0
				for i := 1; i < len(fields); i++ {
					val, _ := strconv.ParseFloat(fields[i], 64)
					total += val
					if i == 4 {
						idle = val
					}
				}

				if lastTotalCPU > 0 {
					totalDiff := total - lastTotalCPU
					idleDiff := idle - lastIdleCPU
					if totalDiff > 0 {
						idlePercent := (idleDiff / totalDiff) * 100
						stats.UsagePercent = 100 - idlePercent
					}
				}

				lastTotalCPU = total
				lastIdleCPU = idle
			} else if strings.HasPrefix(fields[0], "cpu") {
				total := 0.0
				idle := 0.0
				for i := 1; i < len(fields); i++ {
					val, _ := strconv.ParseFloat(fields[i], 64)
					total += val
					if i == 4 {
						idle = val
					}
				}
				idlePercent := (idle / total) * 100
				stats.CoreStats[fields[0]] = 100 - idlePercent
			}
		}
	}

	// Read load averages
	loadavg, err := os.ReadFile(models.ProcLoadavg)
	if err == nil {
		fields := strings.Fields(string(loadavg))
		if len(fields) >= 3 {
			stats.LoadAvg_1, _ = strconv.ParseFloat(fields[0], 64)
			stats.LoadAvg_5, _ = strconv.ParseFloat(fields[1], 64)
			stats.LoadAvg_15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}

	// Read CPU temperature
	temp, err := os.ReadFile(models.CpuTemp)
	if err == nil {
		if tempVal, err := strconv.ParseFloat(strings.TrimSpace(string(temp)), 64); err == nil {
			stats.Temperature = tempVal / 1000 // Convert from millidegrees to degrees
		}
	}

	// Read process and thread count
	if dirs, err := os.ReadDir(models.ProcPath); err == nil {
		for _, dir := range dirs {
			if pid, err := strconv.Atoi(dir.Name()); err == nil {
				stats.ProcessCount++
				if threads, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid)); err == nil {
					stats.ThreadCount += int32(len(threads))
				}
			}
		}
	}

	return stats, nil
}

func collectMemoryStats() (*proto.MemoryStats, error) {
	stats := &proto.MemoryStats{}

	file, err := os.Open(models.ProcMemInfo)
	if err != nil {
		return stats, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	memInfo := make(map[string]uint64)

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}

		key := strings.TrimRight(fields[0], ":")
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}

		memInfo[key] = value * 1024 // Convert from KB to bytes
	}

	stats.Total = memInfo["MemTotal"]
	stats.Free = memInfo["MemFree"]
	stats.Cached = memInfo["Cached"]
	stats.Buffers = memInfo["Buffers"]
	
	// Available memory calculate
	available := memInfo["MemAvailable"]
	if available == 0 {
		// Old kernels don't have MemAvailable, calculate manually
		available = stats.Free + stats.Cached + stats.Buffers
	}
	
	// Real used memory = Total - Available
	stats.Used = stats.Total - available
	if stats.Total > 0 {
		stats.UsagePercent = float64(stats.Used) / float64(stats.Total) * 100
	}
	
	stats.SwapTotal = memInfo["SwapTotal"]
	stats.SwapFree = memInfo["SwapFree"]
	stats.SwapUsed = stats.SwapTotal - stats.SwapFree

	return stats, nil
}

func collectDiskStats() ([]*proto.DiskStats, error) {
	var stats []*proto.DiskStats

	// Skip system directories
	skipPaths := map[string]bool{
		models.NetplanPath:     true,
		models.ElchiPath:       true,
		models.ElchiLibPath:    true,
		models.SystemdPath:     true,
		models.SystemdRootPath: true,
		models.EtcPath:         true,
		models.UsrPath:         true,
	}

	// Read mount points
	file, err := os.Open(models.ProcMounts)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}

		device := fields[0]
		mountPoint := fields[1]
		fsType := fields[2]

		// Skip virtual filesystems and system directories
		if !strings.HasPrefix(device, models.DevPath) || skipPaths[mountPoint] {
			continue
		}

		var stat syscall.Statfs_t
		if err := syscall.Statfs(mountPoint, &stat); err != nil {
			continue
		}

		diskStat := &proto.DiskStats{
			Device:     device,
			MountPoint: mountPoint,
			FsType:     fsType,
			Total:      uint64(stat.Blocks) * uint64(stat.Bsize),
			Free:       uint64(stat.Bfree) * uint64(stat.Bsize),
		}
		diskStat.Used = diskStat.Total - diskStat.Free
		if diskStat.Total > 0 {
			diskStat.UsagePercent = float64(diskStat.Used) / float64(diskStat.Total) * 100
		}

		// Read IO statistics
		device = filepath.Base(device)
		if diskstatsData, err := os.ReadFile(models.ProcDiskStats); err == nil {
			for _, line := range strings.Split(string(diskstatsData), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 14 {
					continue
				}
				if fields[2] == device {
					diskStat.IoReadOps, _ = strconv.ParseUint(fields[3], 10, 64)
					diskStat.IoReadBytes, _ = strconv.ParseUint(fields[5], 10, 64)
					diskStat.IoWriteOps, _ = strconv.ParseUint(fields[7], 10, 64)
					diskStat.IoWriteBytes, _ = strconv.ParseUint(fields[9], 10, 64)
					break
				}
			}
		}

		stats = append(stats, diskStat)
	}

	return stats, nil
}

func collectNetworkStats() (*proto.NetworkStats, error) {
	stats := &proto.NetworkStats{
		Interfaces: make(map[string]*proto.InterfaceStats),
	}

	entries, err := os.ReadDir(models.NetDir)
	if err != nil {
		return stats, err
	}

	for _, entry := range entries {
		iface := entry.Name()
		if iface == "lo" || strings.HasPrefix(iface, "elchi-") {
			continue
		}

		statsPath := filepath.Join(models.NetDir, iface, "statistics")
		stats.Interfaces[iface] = &proto.InterfaceStats{}

		if rxBytes, err := os.ReadFile(filepath.Join(statsPath, "rx_bytes")); err == nil {
			stats.Interfaces[iface].BytesReceived, _ = strconv.ParseUint(strings.TrimSpace(string(rxBytes)), 10, 64)
		}
		if rxPackets, err := os.ReadFile(filepath.Join(statsPath, "rx_packets")); err == nil {
			stats.Interfaces[iface].PacketsReceived, _ = strconv.ParseUint(strings.TrimSpace(string(rxPackets)), 10, 64)
		}

		if txBytes, err := os.ReadFile(filepath.Join(statsPath, "tx_bytes")); err == nil {
			stats.Interfaces[iface].BytesSent, _ = strconv.ParseUint(strings.TrimSpace(string(txBytes)), 10, 64)
		}
		if txPackets, err := os.ReadFile(filepath.Join(statsPath, "tx_packets")); err == nil {
			stats.Interfaces[iface].PacketsSent, _ = strconv.ParseUint(strings.TrimSpace(string(txPackets)), 10, 64)
		}

		if rxDropped, err := os.ReadFile(filepath.Join(statsPath, "rx_dropped")); err == nil {
			dropped, _ := strconv.ParseUint(strings.TrimSpace(string(rxDropped)), 10, 64)
			stats.Interfaces[iface].Dropped = dropped
		}
		if rxErrors, err := os.ReadFile(filepath.Join(statsPath, "rx_errors")); err == nil {
			errors, _ := strconv.ParseUint(strings.TrimSpace(string(rxErrors)), 10, 64)
			stats.Interfaces[iface].Errors = errors
		}
	}

	if tcpData, err := os.ReadFile(models.ProcNetTcp); err == nil {
		stats.TcpConnections = int32(len(strings.Split(string(tcpData), "\n")) - 1)
	}
	if udpData, err := os.ReadFile(models.ProcNetUdp); err == nil {
		stats.UdpConnections = int32(len(strings.Split(string(udpData), "\n")) - 1)
	}
	stats.Connections = stats.TcpConnections + stats.UdpConnections

	return stats, nil
}

func collectSystemInfo() (*proto.SystemInfo, error) {
	info := &proto.SystemInfo{}

	if hostname, err := os.Hostname(); err == nil {
		info.Hostname = hostname
	}

	// Get OS information
	if osRelease, err := os.ReadFile(models.OsRelease); err == nil {
		for _, line := range strings.Split(string(osRelease), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				info.Os = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				break
			}
		}
	}

	// Get kernel version
	if kernel, err := os.ReadFile(models.ProcVersion); err == nil {
		info.KernelVersion = strings.Fields(string(kernel))[2]
	}

	// Get uptime
	if uptime, err := os.ReadFile(models.ProcUptime); err == nil {
		fields := strings.Fields(string(uptime))
		if len(fields) > 0 {
			if uptimeSeconds, err := strconv.ParseFloat(fields[0], 64); err == nil {
				info.Uptime = uptimeSeconds
			}
		}
	}

	return info, nil
}
