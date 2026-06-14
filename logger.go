package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Logger struct {
	dir string
	mu  sync.Mutex
	fd  map[string]*os.File
}

func NewLogger(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log directory failed: %w", err)
	}
	return &Logger{dir: dir, fd: make(map[string]*os.File)}, nil
}

func (l *Logger) getFile(name string) (*os.File, error) {
	date := time.Now().Format("2006-01-02")
	key := name + "_" + date
	if f, ok := l.fd[key]; ok {
		return f, nil
	}
	path := filepath.Join(l.dir, name+"_"+date+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	l.fd[key] = f
	return f, nil
}

func (l *Logger) LogHost(data *HostGPUData) {
	if data == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().Format("2006-01-02 15:04:05")
	f, err := l.getFile(data.Hostname)
	if err != nil {
		return
	}

	if data.Status != "online" {
		fmt.Fprintf(f, "[%s] ERROR: %s\n", ts, data.Error)
		_ = f.Sync()
		return
	}

	sys := data.Sys
	fmt.Fprintf(f, "[%s] CPU=%.1f%% MEM=%.0f/%.0fMB(%.1f%%) LOAD=%.2f/%.2f/%.2f DISKS=%d GPUs=%d\n",
		ts, sys.CPUUsage, sys.MemUsed, sys.MemTotal, sys.MemPercent,
		sys.Load1, sys.Load5, sys.Load15, len(data.Disks), len(data.GPUs))

	for _, disk := range data.Disks {
		fmt.Fprintf(f, "  DISK %s %.0f/%.0fMB(%.0f%%)\n", disk.Mount, disk.UsedMB, disk.SizeMB, disk.UsePercent)
	}

	for _, gpu := range data.GPUs {
		fmt.Fprintf(f, "  GPU%d %s %.0fC %.0f%% %.0f/%.0fMiB(%.0f%%) %.1f/%.1fW",
			gpu.Index, gpu.Name, gpu.Temperature, gpu.Utilization,
			gpu.MemoryUsed, gpu.MemoryTotal, gpu.MemoryPercent,
			gpu.PowerDraw, gpu.PowerLimit)
		if len(gpu.Processes) > 0 {
			fmt.Fprint(f, " procs:")
			for _, p := range gpu.Processes {
				fmt.Fprintf(f, " [%s]%s(%.0fMiB)", p.Username, p.Command, p.GPUMemoryUsage)
			}
		}
		fmt.Fprintln(f)
	}
	_ = f.Sync()
}

func (l *Logger) LogAccess(method, path, remoteAddr string, status int, duration time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().Format("2006-01-02 15:04:05")
	f, err := l.getFile("access")
	if err != nil {
		return
	}
	fmt.Fprintf(f, "[%s] %s %s %s %d %s\n", ts, method, path, remoteAddr, status, duration)
	_ = f.Sync()
}

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.fd {
		_ = f.Close()
	}
	l.fd = make(map[string]*os.File)
}
