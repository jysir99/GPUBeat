package main

import (
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

const nvidiaSMICmd = `nvidia-smi --query-gpu=index,gpu_bus_id,name,temperature.gpu,utilization.gpu,memory.used,memory.total,utilization.memory,power.draw,enforced.power.limit --format=csv,noheader,nounits && echo "===PROCESSES===" && (nvidia-smi --query-compute-apps=gpu_bus_id,pid,process_name,used_gpu_memory --format=csv,noheader 2>/dev/null || echo "") && echo "===USERS===" && ps -eo user:30,pid --no-headers 2>/dev/null && echo "===SYSINFO===" && echo "CPU:$(top -bn1 | grep 'Cpu(s)' | awk '{print $2+$4}')" && echo "MEM:$(awk '/MemTotal/{t=$2}/MemAvailable/{a=$2}END{u=t-a;if(t>0)printf "%.0f/%.0f/%.0f",u/1024,t/1024,u/t*100}' /proc/meminfo)" && echo "LOAD:$(awk '{print $1,$2,$3}' /proc/loadavg)"`

func getExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func fetchHost(pool *SSHPool, hostCfg HostConfig, idx int, wg *sync.WaitGroup, results chan<- *HostGPUData) {
	defer wg.Done()
	start := time.Now()
	output, err := pool.ExecuteCommand(hostCfg.Host, hostCfg.Port, hostCfg.Username, hostCfg.Password, nvidiaSMICmd)
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("[刷新] %s (%s) 失败 %v: %s", hostCfg.Name, hostCfg.Host, elapsed, err)
		results <- &HostGPUData{
			Hostname: hostCfg.Name,
			Host:     hostCfg.Host,
			Status:   "error",
			Error:    err.Error(),
			GPUs:     []GPUInfo{},
			Order:    idx,
		}
		return
	}
	log.Printf("[刷新] %s (%s) 成功 %v, %d GPUs", hostCfg.Name, hostCfg.Host, elapsed, len(ParseGPUData(output, hostCfg.Name, hostCfg.Host).GPUs))
	data := ParseGPUData(output, hostCfg.Name, hostCfg.Host)
	data.Order = idx
	results <- data
}

func backgroundRefresh(cfg *Config, logger *Logger, pool *SSHPool) {
	for {
		start := time.Now()
		var wg sync.WaitGroup
		results := make(chan *HostGPUData, len(cfg.Hosts))

		for i, hostCfg := range cfg.Hosts {
			wg.Add(1)
			go fetchHost(pool, hostCfg, i, &wg, results)
		}

		wg.Wait()
		close(results)

		allData := make([]*HostGPUData, len(cfg.Hosts))
		for data := range results {
			allData[data.Order] = data
		}

		online, failed := 0, 0
		for _, d := range allData {
			if d.Status == "online" {
				online++
			} else {
				failed++
			}
			if logger != nil {
				logger.LogHost(d)
			}
		}

		cache.Set("gpustats", map[string]interface{}{
			"gpustats":    allData,
			"update_time": time.Now().Format("2006-01-02 15:04:05"),
		})

		log.Printf("[刷新] 完成 %v: %d 正常, %d 异常", time.Since(start), online, failed)
		time.Sleep(time.Duration(cfg.Server.Refresh) * time.Second)
	}
}

func main() {
	configPath := filepath.Join(getExeDir(), "config.yaml")
	if len(os.Args) > 1 && os.Args[1] == "-c" && len(os.Args) > 2 {
		configPath = os.Args[2]
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	log.Printf("配置加载成功: %d 台服务器, 端口 %d, 刷新间隔 %ds",
		len(cfg.Hosts), cfg.Server.Port, cfg.Server.Refresh)

	logger, err := NewLogger(filepath.Join(getExeDir(), "log", "host"))
	if err != nil {
		log.Printf("主机日志初始化失败: %v, 继续运行", err)
		logger = nil
	}

	accessLogger, err := NewLogger(filepath.Join(getExeDir(), "log", "access"))
	if err != nil {
		log.Printf("访问日志初始化失败: %v, 继续运行", err)
		accessLogger = nil
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

	go backgroundRefresh(cfg, logger, pool)

	mux := http.NewServeMux()
	mux.HandleFunc("/", loggingMiddleware(handleIndex, accessLogger))
	mux.HandleFunc("/api/gpustats", loggingMiddleware(handleGPUStats, accessLogger))

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("GPU Dashboard 启动: http://%s", addr)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	<-quit
	log.Println("服务已停止")
}
