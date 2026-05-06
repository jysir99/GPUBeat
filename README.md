# GPU Dashboard

基于 SSH 的远程 GPU 监控看板，通过 `nvidia-smi` 实时获取多台服务器的 GPU 状态。

参考 [gpuview-flask](https://github.com/jysir99/gpuview-flask) 视觉风格，使用 Go 实现，编译为单二进制文件。

## 功能

- 通过 SSH 连接远程服务器，执行 `nvidia-smi` 获取 GPU 信息
- 实时显示 GPU 温度、显存、利用率、功耗、进程用户
- 卡片视图 + 详细表格 + ECharts 图表
- 按温度/显存占比自动着色
- 服务器不可达时显示错误提示卡片

## 快速开始

### 编译

```bash
make build
```

输出二进制到 `dist/gpuview`。

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
```

将 `config.yaml` 放到与 `gpuview` 二进制同目录，或通过 `-c` 指定：

```bash
./gpuview                  # 读同目录 config.yaml
./gpuview -c /path/to.yaml # 指定路径
```

### 运行

```bash
make run   # 编译并运行
# 或
./dist/gpuview
```

访问 `http://localhost:9988`。

## 项目结构

```
├── main.go              # 入口，后台刷新
├── config.go            # 配置加载
├── ssh.go               # SSH 远程命令执行
├── gpu.go               # nvidia-smi 输出解析
├── handler.go           # HTTP 路由，模板嵌入
├── web/
│   └── index.html       # 前端页面 (Vue.js + ECharts)
├── config.example.yaml  # 配置示例
├── Makefile
└── README.md
```

前端模板通过 `go:embed` 嵌入二进制，部署时只需一个文件 + config.yaml。
