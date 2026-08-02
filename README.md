# TypeZero

TypeZero 是一个 macOS 菜单栏语音输入工具。当前仓库包含：

- `cmd/server`：Go 单进程后端，串行调用 Qwen3-ASR-Flash 和 DeepSeek。
- `client/TypeZero`：SwiftUI/AppKit macOS 客户端，支持录音、上传、全局快捷键和文字插入。
- `internal`：音频校验、HTTP 接口、限流和供应商适配器。

## 启动后端

需要 Go 1.23+、阿里云百炼 API Key 和 DeepSeek API Key：

```bash
cp .env.example .env
# 编辑 .env 后加载环境变量
set -a
source .env
set +a
go run ./cmd/server
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

听写接口接收不超过 10 MB、5 分钟的 M4A/MP4(AAC) 或 WAV：

```bash
curl -X POST http://127.0.0.1:8080/v1/dictations \
  -F audio=@recording.m4a \
  -F duration_ms=12000 \
  -F output_mode=polished
```

润色成功时返回 `raw_text` 和 `final_text`。润色失败仍返回 HTTP 200、`raw_text` 和 `warning`；识别失败返回 502，超时返回 504。服务端日志不会记录音频、识别文本或 API Key。

## 构建 macOS 客户端

客户端最低支持 macOS 12。`project.yml` 使用 XcodeGen 生成工程：

```bash
cd client
xcodegen generate
open TypeZero.xcodeproj
```

在 Xcode 中选择开发团队并运行。首次使用时需要授予麦克风、输入监控和辅助功能权限：输入监控用于接收其他应用中的全局快捷键，辅助功能用于将文字插入其他应用。授权对象必须是当前构建出的 `TypeZero.app`；存在旧 Typeless/TypeZero 项时应删除后重新添加。默认连接 `http://127.0.0.1:8080`；开发期远程 HTTP 可用，生产环境必须使用 HTTPS。

用户自带的 DashScope/DeepSeek Key 可在客户端设置中选填，只保存在 macOS Keychain。上传时 Key 仅用于当前供应商请求；后端不记录、不回传。

默认快捷键为 `Control + Option + Space`；Fn 单键受具体 Mac、键盘和系统设置影响，仅作实验性选项。完成听写后，客户端会先将文字复制到剪贴板，再尝试粘贴到当前输入框。

## 测试

```bash
go test ./...
go test -race ./...
```

供应商适配器使用官方 HTTP 格式，默认模型可通过环境变量替换。正式发布前应把模型名固定为经过回归测试的快照。
