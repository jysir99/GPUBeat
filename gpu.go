package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type GPUInfo struct {
	Index         int           `json:"index"`
	Name          string        `json:"name"`
	Temperature   float64       `json:"temperature.gpu"`
	Utilization   float64       `json:"utilization.gpu"`
	MemoryUsed    float64       `json:"memory.used"`
	MemoryTotal   float64       `json:"memory.total"`
	MemoryPercent float64       `json:"memory"`
	PowerDraw     float64       `json:"power.draw"`
	PowerLimit    float64       `json:"enforced.power.limit"`
	Processes     []ProcessInfo `json:"processes"`
	UserProcesses string        `json:"user_processes"`
}

type ProcessInfo struct {
	PID            string  `json:"pid"`
	Command        string  `json:"command"`
	GPUMemoryUsage float64 `json:"gpu_memory_usage"`
	Username       string  `json:"username"`
}

type SysInfo struct {
	CPUUsage   float64 `json:"cpu_usage"`
	MemUsed    float64 `json:"mem_used"`
	MemTotal   float64 `json:"mem_total"`
	MemPercent float64 `json:"mem_percent"`
	Load1      float64 `json:"load_1"`
	Load5      float64 `json:"load_5"`
	Load15     float64 `json:"load_15"`
}

type DiskInfo struct {
	Mount      string  `json:"mount"`
	SizeMB     float64 `json:"size_mb"`
	UsedMB     float64 `json:"used_mb"`
	UsePercent float64 `json:"use_percent"`
}

type NetInfo struct {
	RXBytes float64 `json:"rx_bytes"`
	TXBytes float64 `json:"tx_bytes"`
}

type HostGPUData struct {
	Hostname string     `json:"hostname"`
	Host     string     `json:"host"`
	Provider string     `json:"provider,omitempty"`
	Region   string     `json:"region,omitempty"`
	Notes    string     `json:"notes,omitempty"`
	Status   string     `json:"status"`
	Error    string     `json:"error,omitempty"`
	GPUs     []GPUInfo  `json:"gpus"`
	Sys      SysInfo    `json:"sys"`
	Disks    []DiskInfo `json:"disks"`
	Net      NetInfo    `json:"net"`
	Order    int        `json:"-"`
}

func ParseGPUData(rawOutput, hostname, host string) *HostGPUData {
	result := &HostGPUData{
		Hostname: hostname,
		Host:     host,
		Status:   "online",
		GPUs:     []GPUInfo{},
		Disks:    []DiskInfo{},
	}

	sections := splitMonitorSections(rawOutput)
	gpuSection := sections["GPU"]
	if gpuSection == "" {
		gpuSection = sections[""]
	}

	userMap := parseUserSection(sections["USERS"])
	busMap := make(map[string]int)
	result.GPUs = parseGPUSection(gpuSection, busMap)
	assignProcesses(sections["PROCESSES"], busMap, userMap, result.GPUs)
	result.Sys = parseSysSection(sections["SYSINFO"])
	result.Disks = parseDiskSection(sections["DISKS"])
	result.Net = parseNetSection(sections["NET"])

	return result
}

func splitMonitorSections(raw string) map[string]string {
	sections := make(map[string]string)
	current := "GPU"
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "===") && strings.HasSuffix(trimmed, "===") {
			current = strings.ToUpper(strings.TrimSpace(strings.Trim(trimmed, "=")))
			if _, ok := sections[current]; !ok {
				sections[current] = ""
			}
			continue
		}
		sections[current] += line + "\n"
	}
	return sections
}

func parseGPUSection(section string, busMap map[string]int) []GPUInfo {
	var gpus []GPUInfo
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 10 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		idx, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		busID := fields[1]
		busMap[busID] = idx

		memTotal := safeFloat(fields[6])
		memUsed := safeFloat(fields[5])
		var memPercent float64
		if memTotal > 0 {
			memPercent = roundToOne(memUsed / memTotal * 100)
		}

		gpus = append(gpus, GPUInfo{
			Index:         idx,
			Name:          fields[2],
			Temperature:   safeFloat(fields[3]),
			Utilization:   safeFloat(fields[4]),
			MemoryUsed:    memUsed,
			MemoryTotal:   memTotal,
			MemoryPercent: memPercent,
			PowerDraw:     safeFloat(fields[8]),
			PowerLimit:    safeFloat(fields[9]),
			Processes:     []ProcessInfo{},
			UserProcesses: "-",
		})
	}
	return gpus
}

func parseUserSection(section string) map[string]string {
	userMap := make(map[string]string)
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			userMap[parts[1]] = parts[0]
		}
	}
	return userMap
}

func assignProcesses(section string, busMap map[string]int, userMap map[string]string, gpus []GPUInfo) {
	gpuProcs := make(map[int][]ProcessInfo)
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "No running") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 4 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		busID := fields[0]
		pid := fields[1]
		cmd := fields[2]
		memStr := strings.ReplaceAll(fields[3], "MiB", "")
		memStr = strings.ReplaceAll(memStr, "[N/A]", "0")
		mem := safeFloat(memStr)
		username := userMap[pid]
		if username == "" {
			username = "unknown"
		}

		if gpuIdx, ok := busMap[busID]; ok {
			gpuProcs[gpuIdx] = append(gpuProcs[gpuIdx], ProcessInfo{
				PID:            pid,
				Command:        cmd,
				GPUMemoryUsage: mem,
				Username:       username,
			})
		}
	}

	for i := range gpus {
		procs := gpuProcs[gpus[i].Index]
		gpus[i].Processes = procs
		if len(procs) > 0 {
			userSet := make(map[string]bool)
			for _, p := range procs {
				if p.Username != "unknown" {
					userSet[p.Username] = true
				}
			}
			users := make([]string, 0, len(userSet))
			for u := range userSet {
				users = append(users, u)
			}
			if len(users) > 0 {
				gpus[i].UserProcesses = strings.Join(users, ", ")
			}
		}
	}
}

func parseSysSection(section string) SysInfo {
	var sys SysInfo
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CPU:") {
			sys.CPUUsage = safeFloat(strings.TrimPrefix(line, "CPU:"))
		} else if strings.HasPrefix(line, "MEM:") {
			parts := strings.Split(strings.TrimPrefix(line, "MEM:"), "/")
			if len(parts) == 3 {
				sys.MemUsed = safeFloat(parts[0])
				sys.MemTotal = safeFloat(parts[1])
				sys.MemPercent = safeFloat(parts[2])
			}
		} else if strings.HasPrefix(line, "LOAD:") {
			parts := strings.Fields(strings.TrimPrefix(line, "LOAD:"))
			if len(parts) >= 3 {
				sys.Load1 = safeFloat(parts[0])
				sys.Load5 = safeFloat(parts[1])
				sys.Load15 = safeFloat(parts[2])
			}
		}
	}
	return sys
}

func parseDiskSection(section string) []DiskInfo {
	disks := []DiskInfo{}
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 4 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		disks = append(disks, DiskInfo{
			Mount:      fields[0],
			SizeMB:     safeFloat(fields[1]),
			UsedMB:     safeFloat(fields[2]),
			UsePercent: safeFloat(strings.TrimSuffix(fields[3], "%")),
		})
	}
	return disks
}

func parseNetSection(section string) NetInfo {
	var net NetInfo
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "RX:") || strings.HasPrefix(upper, "RX_BYTES:") {
			net.RXBytes = safeFloat(line[strings.Index(line, ":")+1:])
		} else if strings.HasPrefix(upper, "TX:") || strings.HasPrefix(upper, "TX_BYTES:") {
			net.TXBytes = safeFloat(line[strings.Index(line, ":")+1:])
		}
	}
	return net
}

func safeFloat(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "[N/A]", "0")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func roundToOne(v float64) float64 {
	return float64(int(v+0.5)) / 1
}

var (
	usernameMap     = make(map[string]string)
	usernameCounter int
	usernameMu      sync.Mutex
)

func anonymizeUsername(name string) string {
	if name == "" || name == "unknown" || name == "-" {
		return name
	}
	usernameMu.Lock()
	defer usernameMu.Unlock()
	if mapped, ok := usernameMap[name]; ok {
		return mapped
	}
	usernameCounter++
	mapped := fmt.Sprintf("user%d", usernameCounter)
	usernameMap[name] = mapped
	return mapped
}

func AnonymizeHostData(data *HostGPUData) {
	for i := range data.GPUs {
		gpu := &data.GPUs[i]
		for j := range gpu.Processes {
			gpu.Processes[j].Username = anonymizeUsername(gpu.Processes[j].Username)
		}
		if gpu.UserProcesses != "-" && gpu.UserProcesses != "" {
			users := strings.Split(gpu.UserProcesses, ", ")
			for k, u := range users {
				users[k] = anonymizeUsername(strings.TrimSpace(u))
			}
			gpu.UserProcesses = strings.Join(users, ", ")
		}
	}
}
