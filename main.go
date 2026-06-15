package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const monitorCmd = `printf "===SYSINFO===\n"; echo "CPU:$(top -bn1 2>/dev/null | grep 'Cpu(s)' | awk '{print $2+$4}')"; echo "MEM:$(awk '/MemTotal/{t=$2}/MemAvailable/{a=$2}END{u=t-a;if(t>0)printf "%.0f/%.0f/%.0f",u/1024,t/1024,u/t*100}' /proc/meminfo 2>/dev/null)"; echo "LOAD:$(awk '{print $1,$2,$3}' /proc/loadavg 2>/dev/null)"; printf "===DISKS===\n"; df -Pm -x tmpfs -x devtmpfs 2>/dev/null | awk 'NR>1 {print $6","$2","$3","$5}'; printf "===NET===\n"; awk -F'[: ]+' 'NR>2 {rx+=$3; tx+=$11} END{printf "RX:%.0f\nTX:%.0f\n",rx,tx}' /proc/net/dev 2>/dev/null; printf "===GPU===\n"; if command -v nvidia-smi >/dev/null 2>&1; then nvidia-smi --query-gpu=index,gpu_bus_id,name,temperature.gpu,utilization.gpu,memory.used,memory.total,utilization.memory,power.draw,enforced.power.limit --format=csv,noheader,nounits 2>/dev/null || true; fi; printf "===PROCESSES===\n"; if command -v nvidia-smi >/dev/null 2>&1; then nvidia-smi --query-compute-apps=gpu_bus_id,pid,process_name,used_gpu_memory --format=csv,noheader 2>/dev/null || true; fi; printf "===USERS===\n"; ps -eo user:30,pid --no-headers 2>/dev/null || true`

func getExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func fetchHost(ctx context.Context, pool *SSHPool, hostCfg HostConfig, idx int, wg *sync.WaitGroup, results chan<- *HostGPUData) {
	defer wg.Done()
	start := time.Now()
	output, err := pool.ExecuteCommand(hostCfg.Host, hostCfg.Port, hostCfg.Username, hostCfg.Password, monitorCmd)
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		log.Printf("[refresh] %s (%s) failed after %v: %s", hostCfg.Name, hostCfg.Host, elapsed, err)
		results <- &HostGPUData{
			Hostname: hostCfg.Name,
			Host:     hostCfg.Host,
			Provider: hostCfg.Provider,
			Region:   hostCfg.Region,
			Notes:    hostCfg.Notes,
			Status:   "error",
			Error:    err.Error(),
			GPUs:     []GPUInfo{},
			Disks:    []DiskInfo{},
			Order:    idx,
		}
		return
	}
	data := ParseGPUData(output, hostCfg.Name, hostCfg.Host)
	data.Provider = hostCfg.Provider
	data.Region = hostCfg.Region
	data.Notes = hostCfg.Notes
	data.Order = idx
	log.Printf("[refresh] %s (%s) ok after %v, %d GPUs", hostCfg.Name, hostCfg.Host, elapsed, len(data.GPUs))
	results <- data
}

func backgroundRefresh(ctx context.Context, store *ConfigStore, logger *Logger, pool *SSHPool, activity *ActivityLog, refreshNow <-chan struct{}) {
	hostStatus := make(map[string]string)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cfg := store.Snapshot()
		start := time.Now()
		var wg sync.WaitGroup
		results := make(chan *HostGPUData, len(cfg.Hosts))

		for i, hostCfg := range cfg.Hosts {
			wg.Add(1)
			go fetchHost(ctx, pool, hostCfg, i, &wg, results)
		}

		wg.Wait()
		close(results)

		if ctx.Err() != nil {
			return
		}

		allData := make([]*HostGPUData, len(cfg.Hosts))
		for data := range results {
			if cfg.Server.Privacy {
				AnonymizeHostData(data)
			}
			allData[data.Order] = data
		}

		online, failed := 0, 0
		for _, d := range allData {
			if d == nil {
				continue
			}
			if d.Status == "online" {
				online++
			} else {
				failed++
			}
			if previous, ok := hostStatus[d.Hostname]; !ok || previous != d.Status {
				level := "info"
				message := fmt.Sprintf("%s is %s", d.Hostname, d.Status)
				if d.Status != "online" {
					level = "error"
					message = fmt.Sprintf("%s failed: %s", d.Hostname, d.Error)
				}
				if activity != nil {
					activity.Add(level, "host_status", d.Hostname, message, map[string]string{
						"host":   d.Host,
						"status": d.Status,
					})
				}
				hostStatus[d.Hostname] = d.Status
			}
			if logger != nil {
				logger.LogHost(d)
			}
		}

		cache.Set("gpustats", map[string]interface{}{
			"gpustats":    allData,
			"update_time": time.Now().Format("2006-01-02 15:04:05"),
		})

		log.Printf("[refresh] done in %v: %d online, %d failed", time.Since(start), online, failed)

		select {
		case <-ctx.Done():
			return
		case <-refreshNow:
		case <-time.After(time.Duration(cfg.Server.Refresh) * time.Second):
		}
	}
}

func main() {
	configPath := filepath.Join(getExeDir(), "config.yaml")
	cliPrivacy := false
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-c" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
			i++
		} else if os.Args[i] == "-privacy" {
			cliPrivacy = true
		} else if os.Args[i] == "-h" || os.Args[i] == "--help" {
			fmt.Println("GPUBeat - GPU server cluster dashboard")
			fmt.Println()
			fmt.Println("Usage: gpubeat [options]")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  -c <path>     specify config file path (default: config.yaml beside the executable)")
			fmt.Println("  -privacy      replace real process usernames with user1, user2, ...")
			fmt.Println("  -h, --help    show help")
			os.Exit(0)
		}
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("load config failed: %v", err)
	}

	if cliPrivacy {
		cfg.Server.Privacy = true
	}
	store := NewConfigStore(configPath, cfg)

	log.Printf("config loaded: %d hosts, port %d, refresh %ds, privacy=%v, terminal=%v",
		len(cfg.Hosts), cfg.Server.Port, cfg.Server.Refresh, cfg.Server.Privacy, cfg.Server.Terminal.Enabled)

	logger, err := NewLogger(filepath.Join(getExeDir(), "log", "host"))
	if err != nil {
		log.Printf("host logger init failed: %v, continuing", err)
		logger = nil
	}

	accessLogger, err := NewLogger(filepath.Join(getExeDir(), "log", "access"))
	if err != nil {
		log.Printf("access logger init failed: %v, continuing", err)
		accessLogger = nil
	}
	activityLog, err := NewActivityLog(filepath.Join(getExeDir(), "log", "activity"), 1000)
	if err != nil {
		log.Printf("activity logger init failed: %v, continuing", err)
		activityLog = nil
	} else {
		activityLog.Add("info", "app_start", "", "GPUBeat started", map[string]string{
			"config": configPath,
		})
	}

	defer func() {
		if logger != nil {
			logger.Close()
		}
		if accessLogger != nil {
			accessLogger.Close()
		}
	}()

	pool := NewSSHPool()
	defer pool.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	refreshNow := make(chan struct{}, 1)
	triggerRefresh := func() {
		select {
		case refreshNow <- struct{}{}:
		default:
		}
	}
	go backgroundRefresh(ctx, store, logger, pool, activityLog, refreshNow)

	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", loggingMiddleware(serveWebAsset("web/favicon.ico"), accessLogger))
	mux.HandleFunc("/favicon-32.png", loggingMiddleware(serveWebAsset("web/favicon-32.png"), accessLogger))
	mux.HandleFunc("/apple-touch-icon.png", loggingMiddleware(serveWebAsset("web/apple-touch-icon.png"), accessLogger))
	mux.HandleFunc("/", loggingMiddleware(handleIndex, accessLogger))
	mux.HandleFunc("/api/gpustats", loggingMiddleware(handleGPUStats, accessLogger))
	configHandler := NewConfigHandler(store, activityLog, triggerRefresh)
	mux.Handle("/api/config", loggingMiddleware(configHandler.ServeHTTP, accessLogger))
	mux.Handle("/api/config/", loggingMiddleware(configHandler.ServeHTTP, accessLogger))
	mux.Handle("/api/activity", loggingMiddleware(NewActivityHandler(activityLog).ServeHTTP, accessLogger))
	terminalHandler := NewTerminalHandler(store, pool, activityLog)
	defer terminalHandler.Close()
	mux.Handle("/api/terminal", loggingMiddleware(terminalHandler.ServeHTTP, accessLogger))
	mux.Handle("/api/terminal/", loggingMiddleware(terminalHandler.ServeHTTP, accessLogger))

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("GPU dashboard started: http://%s", addr)

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server start failed: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel()
	srvCtx, srvCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer srvCancel()
	_ = srv.Shutdown(srvCtx)
	log.Println("server stopped")
}
