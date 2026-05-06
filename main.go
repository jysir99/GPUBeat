package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

const nvidiaSMICmd = `nvidia-smi --query-gpu=index,gpu_bus_id,name,temperature.gpu,utilization.gpu,memory.used,memory.total,utilization.memory,power.draw,enforced.power.limit --format=csv,noheader,nounits && echo "===PROCESSES===" && (nvidia-smi --query-compute-apps=gpu_bus_id,pid,process_name,used_gpu_memory --format=csv,noheader 2>/dev/null || echo "") && echo "===USERS===" && ps -eo user,pid --no-headers 2>/dev/null`

func getExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func fetchHost(hostCfg HostConfig, wg *sync.WaitGroup, results chan<- *HostGPUData) {
	defer wg.Done()
	output, err := ExecuteCommand(hostCfg.Host, hostCfg.Port, hostCfg.Username, hostCfg.Password, nvidiaSMICmd)
	if err != nil {
		results <- &HostGPUData{
			Hostname: hostCfg.Name,
			Host:     hostCfg.Host,
			Status:   "error",
			Error:    err.Error(),
			GPUs:     []GPUInfo{},
		}
		return
	}
	results <- ParseGPUData(output, hostCfg.Name, hostCfg.Host)
}

func backgroundRefresh(cfg *Config) {
	for {
		var wg sync.WaitGroup
		results := make(chan *HostGPUData, len(cfg.Hosts))

		for _, hostCfg := range cfg.Hosts {
			wg.Add(1)
			go fetchHost(hostCfg, &wg, results)
		}

		wg.Wait()
		close(results)

		var allData []*HostGPUData
		for data := range results {
			allData = append(allData, data)
		}
		sort.Slice(allData, func(i, j int) bool {
			return allData[i].Hostname < allData[j].Hostname
		})

		cache.Set("gpustats", map[string]interface{}{
			"gpustats":    allData,
			"update_time": time.Now().Format("2006-01-02 15:04:05"),
		})

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

	go backgroundRefresh(cfg)

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/api/gpustats", handleGPUStats)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("GPU Dashboard 启动: http://%s", addr)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	<-quit
	log.Println("服务已停止")
}
