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

## 分块听写提速方案

`feature/chunked-dictation` 分支将长录音切成重叠分段并行上传，服务端逐段 ASR 后由 LLM 一次性去重合并并润色。实测瓶颈不在架构本身：单段（9.5 秒音频）ASR 耗时 24~73 秒，且并发越多越慢（2 段并发约 24 秒/段，4 段并发 50~73 秒/段），指向 DashScope 账号侧并发/限流排队；润色约 0.8 秒、上传约 0.25 秒/段，可忽略。另有缺陷会把"慢"变成"直接失败"：会话清理误杀正在 ASR 的活跃会话、客户端非末段超时 60 秒小于服务端 100 秒、`duration_ms` 发送总时长导致略超 5 分钟的录音整单被拒、客户端把服务端累计 ASR 耗时重复求和导致计时失真。

以下阶段按顺序实施，均不更换模型、不改 overlap 合并逻辑，不损失识别精度。

### 阶段 1：正确性修复（前置条件）

- 修复会话清理：会话记录创建时间并在分段到达时刷新，evict 按创建时间与最近到达时间的较大值判定，避免清理仍在 ASR 的活跃会话；补充回归测试。
- 限流器由固定窗口改为令牌桶，默认提高到约 60 次/分钟，保证一次会话的全部分段不被本服务限流拦截。
- 超时链整体放宽：ASR 提供商 120 秒、服务端请求 140 秒、客户端 150 秒（实测 DashScope 尾部排队可超 90 秒，原 90/100/105 链会把单次慢调用变成整单失败）；`duration_ms` 改发每段自身时长，服务端以音频解析时长为准。
- 修复客户端 ASR 计时：服务端每段响应返回的是会话累计值，客户端取最后一次的累计值而非求和，保证耗时度量可信。
- 移除开发期遗留的音频 hex 预览日志（违反日志不落音频的约束）。

### 阶段 2：减少请求数与排队

- 分块调大：步长从 8 秒提高到 24~30 秒，overlap 保持约 2 秒。5 分钟录音由 38 段降至约 10 段，重复识别的音频占比由约 19% 降至约 7%。
- 服务端增加 ASR 并发信号量（默认 2~3，环境变量可调），与账号实际并发配额匹配，避免在提供商侧排队。
- 预期：32 秒录音从约 73 秒降至 25~30 秒；长录音从几乎必然失败变为可用（约 2~3 分钟）。

### 阶段 3：录音中增量上传（核心提速）

- 录音期间每凑满一个窗口即在后台上传做 ASR；停止录音时只剩末段 ASR 与润色。
- 停止后耗时约为末段 ASR 加 1 秒润色（约 20~30 秒），与录音长度无关；5 分钟录音从分钟级降至约 30 秒。
- 分块、overlap 与合并提示词不变，精度零变化；录音中不展示识别文字，不违反"首版不做实时字幕"。
- 依赖阶段 1（会话需存活整个录音期）；客户端复杂度增加，需 macOS 12 真机验证。

### 阶段 4：提供商侧验证（并行，用数据决策）

- 从服务器实测单次 ASR 基线延迟与到 DashScope 的网络 RTT；在百炼控制台确认账号并发配额，必要时申请扩容。
- 若单请求基线也超过 20 秒，再评估 DashScope 原生协议或文件转写接口；模型抽象层已预留，不需要更换模型。

实施建议：阶段 1、2 一次完成并部署实测，再推进阶段 3；阶段 4 与前述阶段并行。

## 演进路线

1. 完成 macOS MVP，验证快捷键、识别、润色和文字插入。
2. 增加术语词典、润色风格与本地 WhisperKit 隐私模式。
3. 复用通用后端开发 Android 输入法。
4. 根据平台限制评估 Windows 和妥协形态的 iOS 独立应用。
