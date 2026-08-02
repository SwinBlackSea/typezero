# TypeZero 技术说明

## 总体架构

```text
macOS 客户端
  录音 -> POST /v1/dictations -> 插入 final_text
                    |
                    v
轻量后端
  Qwen3-ASR-Flash -> raw_text -> DeepSeek -> final_text
```

客户端只调用一个 HTTP 接口。服务端内部串行完成语音识别和文字润色，不使用 WebSocket。

## macOS 客户端

- 技术栈：Swift + SwiftUI，必要处使用 AppKit。
- 系统要求：macOS 12 及以上；当前开发环境固定为 macOS 12.6、Xcode 14.2、Swift 5.7 和 XcodeGen 2.42。使用 XcodeGen 从 `client/project.yml` 生成 Xcode 工程。
- 录音：AVFoundation，输出 16 kHz、单声道、16-bit PCM 的 WAV 音频；客户端在接近 5 分钟或 10 MiB 时自动停止。当前 Qwen3-ASR-Flash 的兼容接口不支持 M4A/MP4 容器，不能上传 AAC 封装的 `.m4a` 文件。
- 快捷键：主线使用 `NSEvent.addGlobalMonitorForEvents` 监听全局键盘事件，只注册 global monitor，避免本地和全局 monitor 双触发。默认使用 `Control + Option + Space`，Fn 单键仅为实验性选项；全局监听需要“输入监控”权限。`experiment/fn-event-tap` 分支仅针对 Fn 使用被动 `CGEventTap` 监听 `flagsChanged` 和 `Secondary Fn` 标志，不拦截系统事件；若 Fn 被 macOS 绑定为切换输入法，应用仍不能消除该系统行为。创建失败时回退到 global monitor，必须在 macOS 12 真机验证后才可合并。
- 文字插入：先写入剪贴板，再通过 Accessibility API 模拟粘贴；模拟粘贴失败时保留剪贴板文字，文字插入需要“辅助功能”权限。
- 悬浮反馈：录音和处理中使用同规格、不抢焦点的紧凑 `NSPanel` 悬浮胶囊展示；录音时保留白色、声音驱动的细波形，结束统一使用全局快捷键或菜单栏，不得抢走目标输入框焦点。成功和失败状态立即收起胶囊，改由菜单栏呈现结果。
- 声音反馈：客户端在实际开始录音后合成并播放 `C → D`（`1 → 2`）双音，在实际停止并进入处理后播放 `C → F`（`1 → 4`）双音。使用内存生成的 WAV 交给 `NSSound` 播放，不引入音效资源；处理期间 `toggleRecording` 提前返回，因此不会更换胶囊或发声。
- 凭据：用户自带 Key 时保存到 macOS Keychain，禁止明文落盘。
- 分发：Developer ID 签名并经 Apple 公证，以 DMG/ZIP 发布；首版不走 Mac App Store 沙盒。

不选择 Tauri 的原因：当前只开发 macOS，原生 Swift 对全局快捷键、录音、辅助功能和系统权限的集成更直接。需要支持 Windows 时再评估跨平台方案。

### XcodeGen 与权限清单

- `client/project.yml` 是 Info.plist 的唯一配置源。XcodeGen 会在生成工程时重写 `info.path` 指向的文件，因此 `NSMicrophoneUsageDescription`、`LSUIElement`、ATS 等所有自定义字段必须同时声明在 `info.properties`，不能只手动编辑 `client/TypeZero/Info.plist`。
- 每次执行 `xcodegen generate` 后，构建前检查生成的 `TypeZero/Info.plist`，构建后检查 App 包内的 `Contents/Info.plist` 是否包含 `NSMicrophoneUsageDescription`；缺失时 macOS TCC 会在首次录音时直接终止进程。
- 麦克风权限、输入监控权限和辅助功能权限相互独立。授权对象必须是当前实际运行的 `TypeZero.app`；更换构建路径、签名或残留的 Typeless/旧 TypeZero 条目时，应删除旧项并重新添加当前 App，再重启客户端或刷新监听。
- 首次手动开始录音时，客户端调用 `CGRequestListenEventAccess()` 请求输入监控，并通过 `AXIsProcessTrustedWithOptions` 触发辅助功能的系统授权提示；录音器本身通过 `AVCaptureDevice.requestAccess(for: .audio)` 请求麦克风。前两项不能由应用自行授予，用户仍须在系统确认；为避免每次点击都反复打扰，这两项引导每次应用启动只触发一次。

## 后端

- 技术栈：Go，无状态 HTTP 单进程服务。
- 核心接口：`POST /v1/dictations`，接收音频和输出模式，返回 `raw_text`、`final_text` 及错误信息。
- 语音识别：开发期默认 `qwen3-asr-flash`，适合非实时中文听写；客户端限制单次录音不超过 5 分钟、10 MiB。
- 文字处理：开发期默认 `deepseek-v4-flash` 并关闭思考模式，负责纠错、去除口头语和重复、补充标点、分段及轻度润色，必须保持原意。原文有明确多事项、步骤或待办信号时使用 `1. 2. 3.` 编号；普通聊天和单一陈述不强行列表化，也不凭空添加标题或事项。
- 模型抽象：定义 `SpeechProvider` 和 `TextProvider`，以后可替换为 OpenAI Transcribe、Groq Whisper、本地 WhisperKit或其他模型。
- 开发期使用模型最新别名，正式发布时固定模型快照，避免输出随供应商升级而漂移。

### HTTP 接口契约

`GET /healthz` 用于健康检查。`POST /v1/dictations` 使用 `multipart/form-data`，字段如下：

- `audio`：必填，WAV 文件；当前默认 Qwen3-ASR-Flash 仅接受此格式，以避免模型对 M4A/MP4 容器的静默误识别。
- `duration_ms`：必填，客户端测得的正整数毫秒数；服务端同时解析音频本身的时长。
- `output_mode`：可选，`polished`（默认）或 `raw`；`raw` 跳过文字润色。

用户自带 Key 时，客户端分别通过 `X-TypeZero-DashScope-Key` 和 `X-TypeZero-DeepSeek-Key` 请求头传递。服务端只在当前请求中使用，不保存、不回传、不写入日志；请求头未提供时使用服务端环境变量中的 Key。

成功响应：

```json
{
  "request_id": "7e6f4c45b0e147b2a7f26f57",
  "raw_text": "原始识别文字",
  "final_text": "整理润色后的文字"
}
```

润色失败仍返回 HTTP 200，保留 `raw_text`、将 `final_text` 留空，并附带结构化 `warning`，由客户端让用户确认是否插入原文。识别失败返回 502，请求处理超时返回 504；参数、格式、大小、时长和频率限制均返回结构化错误。

## 请求与容错

1. 校验音频格式、大小和时长，不合法时立即返回明确错误。
2. 调用 ASR 得到 `raw_text`；失败则终止，不进入润色。
3. 调用 DeepSeek 得到 `final_text`。
4. 润色失败时仍返回 `raw_text`，客户端让用户选择是否插入。
5. 插入失败时自动复制结果到剪贴板，避免内容丢失。

开发期若客户端显示 HTTP 503，先检查客户端服务地址、反向代理和 Go 服务健康检查；当前 Go 听写接口本身不返回 503，供应商识别失败会返回 502，处理超时返回 504。

## 安全与性能

- 正式服务的供应商 API Key 只放在服务端环境变量或密钥服务中；用户自带 Key 时不记录、不回传、不写日志。
- 全链路 HTTPS；处理完成后立即删除音频，默认不保存录音或文字历史。
- 客户端将音频转为单声道并压缩后上传，以降低带宽和延迟。
- 设置请求大小、时长、超时和按客户端 IP 的频率限制；仅在配置可信代理网段后采信 `X-Forwarded-For`。供应商异常时快速失败。
- 日志仅记录请求 ID、总耗时、接收校验/识别/润色三个阶段的耗时、供应商结果和错误码，不记录音频、录音时长、完整文字或 API Key。客户端仅在当前界面展示本次请求的构造与 HTTP 往返时间，以及服务端通过 `Server-Timing` 返回的阶段耗时，不持久化这些指标。
- 输入监控只判定配置的快捷键，Fn tap 为只读监听；辅助功能仅发送模拟 `Command + V`，不得读取键盘、剪贴板或其他应用 UI 内容。客户端仅将识别所需音频和结果文字发送到配置的服务地址。

## 模型结论

- 默认组合：Qwen3-ASR-Flash + DeepSeek。
- Qwen 适合国内调用，中文与方言覆盖较好，成本低，且符合录完后一次处理的模式。
- ChatGPT/Codex 订阅不包含 API 调用额度，也不是产品运行依赖；模型 API 需要单独申请和计费。
- 后续用真实录音建立小型测试集，对比 Qwen、OpenAI、Groq Whisper 和本地模型的准确率、延迟与成本。

## 演进路线

1. 完成 macOS MVP，验证快捷键、识别、润色和文字插入。
2. 增加术语词典、润色风格与本地 WhisperKit 隐私模式。
3. 复用通用后端开发 Android 输入法。
4. 根据平台限制评估 Windows 和妥协形态的 iOS 独立应用。
