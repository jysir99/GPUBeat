# GPUBeat

基于 SSH 的多服务器 GPU 实时监控看板，通过 `nvidia-smi` 采集数据，Go 单二进制部署。

## 功能

- **多服务器监控**：通过 SSH 连接远程服务器，执行 `nvidia-smi` 获取 GPU 信息
- **实时数据展示**：GPU 温度、显存、利用率、功耗、进程及用户信息
- **系统信息**：CPU 使用率、内存、系统负载
- **可视化图表**：ECharts 饼图、柱状图、折线趋势
- **按值着色**：温度/显存占比自动变色
- **用户追踪**：显示每个 GPU 上运行进程的用户名及显存占用
- **连接池**：SSH 连接复用，断线自动重连
- **日志系统**：按服务器、按日期存储历史数据
- **单文件部署**：前端通过 `go:embed` 嵌入二进制

## 截图

<!-- 在此处添加截图 -->
<!--
![dashboard](docs/screenshot-dashboard.png)
![detail](docs/screenshot-detail.png)
-->

## 快速开始

### 编译

```bash
make build
```

输出二进制到 `dist/gpubeat`。

### 配置

```bash
cp config.example.yaml config.yaml
```

编辑 `config.yaml`：

```yaml
server:
  host: "0.0.0.0"
  port: 9988
  refresh: 3

hosts:
  - name: "gpu-server-1"
    host: "192.168.1.100"
    port: 22
    username: "root"
    password: "your-password"

  - name: "gpu-server-2"
    host: "192.168.1.101"
    port: 22
    username: "user"
    password: "password"
```

将 `config.yaml` 放到与 `gpubeat` 二进制同目录，或通过 `-c` 指定：

```bash
./gpubeat                  # 读同目录 config.yaml
./gpubeat -c /path/to.yaml # 指定路径
```

### 运行

```bash
make run   # 编译并运行
# 或
./dist/gpubeat
```

访问 `http://localhost:9988`。

## 项目结构

```
├── main.go              # 入口，后台刷新
├── config.go            # 配置加载
├── ssh.go               # SSH 连接池
├── gpu.go               # nvidia-smi 输出解析
├── handler.go           # HTTP 路由，模板嵌入
├── logger.go            # 按服务器按日期日志
├── web/
│   └── index.html       # 前端页面 (Vue 2 + ECharts)
├── config.example.yaml  # 配置示例
├── Makefile
└── README.md
```

## 架构

```
用户浏览器 ──HTTP──▶ Go 服务 ◀──缓存──▶ 内存
                      │
                 backgroundRefresh（每 N 秒）
                      │
              SSH 连接池（每服务器一个持久连接）
                      │
              nvidia-smi（GPU + 进程 + 系统信息）
```

多用户访问只读取内存缓存，不会产生额外 SSH 请求。

## 开源许可

MIT License
