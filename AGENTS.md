# AGENTS.md

开始任何工作前，必须先完整阅读并理解以下两个文件：

1. `PRD.md` — 产品需求文档
2. `TECH.md` — 技术方案文档

不要跳过，不要在未理解这两个文件的情况下开始写代码。

---

## 客户端编译环境（sw_01 的 Mac）

- **macOS**: 12.6.2 Monterey（**不是 13+，不是 14+**）
- **Xcode**: 14.2（最高版本，无法升级）
- **芯片**: Intel x64
- **Swift**: 5.7（Xcode 14.2 自带，**不是 5.10**）
- **xcodegen**: 2.42.0（兼容 Xcode 14.x；2.46+ 需要 Xcode 15.3，不能用）
- **编译命令**: `cd client && xcodegen generate && xcodebuild -project TypeZero.xcodeproj -scheme TypeZero -configuration Release build`

## Swift 5.7 兼容规则（必须遵守，否则编译失败）

### 规则 1：switch 必须显式 return

Swift 5.7 不支持 switch 表达式的隐式返回。每个 case 必须写 `return`：

```swift
// ❌
case .a: "hello"
case .success(let msg): msg

// ✅
case .a: return "hello"
case .success(let msg): return msg
```

### 规则 2：禁止使用 macOS 13+ API

| 禁止 | 替代方案 |
|------|---------|
| `MenuBarExtra` | `NSStatusBar` + 自定义 `NSView` 子类重写 `mouseDown` |
| `LabeledContent("标签", value: ...)` | `HStack { Text("标签").foregroundColor(.secondary); Spacer(); Text(...) }` |
| `.formStyle(.grouped)` | 去掉 |
| `.menuBarExtraStyle(.window)` | `NSPopover` |

### 规则 3：@MainActor 类中的 Timer 闭包

```swift
// ❌ 编译失败
timer = Timer.scheduledTimer(...) { @MainActor [weak self] _ in
    self?.tick()
}

// ✅
tickTask = Task { [weak self] in
    while !Task.isCancelled {
        await self?.tick()
        try? await Task.sleep(nanoseconds: 250_000_000)
    }
}
```

### 规则 4：project.yml 固定配置

```yaml
deploymentTarget:
  macOS: "12.0"
settings:
  base:
    SWIFT_VERSION: "5.0"
    SWIFT_STRICT_CONCURRENCY: minimal
```

### 规则 5：菜单栏点击用 NSView.mouseDown，不用 button.action

```swift
class StatusBarItemView: NSView {
    var onMouseUp: (() -> Void)?
    override func mouseDown(with event: NSEvent) { onMouseUp?() }
}
statusItem.view = containerView
```

不要用 `NSHostingView` 包裹 SwiftUI View（`onTapGesture` 不响应），不要用 `statusItem.button.action`（macOS 12 不触发）。

### 规则 6：非 @MainActor 类不能通过 Combine 观察 AppModel

AppModel 是 `@MainActor`。非 MainActor 上下文（如 AppDelegate）不能调用 `model.$phase.sink`。**必须用回调传值**：

```swift
// ❌ 编译失败 — 跨 actor 隔离访问
model.$phase.sink { [weak self] phase in ... }

// ✅ AppModel 里定义回调，传递值而非引用
var onStatusChanged: ((Phase) -> Void)?
private func setPhase(_ newPhase: Phase) {
    phase = newPhase
    onStatusChanged?(newPhase)  // 把值传出去，接收方不需要访问 model
}

// ✅ AppDelegate 只接收值，不碰 model
model.onStatusChanged = { phase in self.updateIcon(for: phase) }
```

### 规则 7：ShortcutMonitor 只用 globalMonitor

有了输入监控权限后，`addLocalMonitorForEvents` 和 `addGlobalMonitorForEvents` 会同时触发同一按键事件，导致录音状态冲突崩溃。**只用 globalMonitor**。

`experiment/fn-event-tap` 分支是唯一例外：仅为验证 Fn 被系统映射时的兼容性，可用被动 `CGEventTap` 替换（不能叠加）Fn 的 global flags monitor。该 tap 必须使用 `listenOnly`，不得拦截或修改事件，创建失败时回退 global monitor；因此无法消除 macOS 已绑定的 Fn 输入法切换。未经 macOS 12 真机验证不得合并到 `main`。

### 规则 8：AppDelegate 里观察 model 状态变化，用回调不用 Combine

见规则 6。涉及异步状态回调统一用 closure，不要用 `$phase.sink` / `objectWillChange` / `.receive(on:)` 等 Combine 管道。

### 规则 9：开发期放宽 HTTP 校验

`validatedEndpoint()` 中的 HTTPS 强制校验在开发期需注释掉：

```swift
// if scheme == "http", host != "127.0.0.1", host != "localhost" {
//     throw ClientError.configuration("远程服务必须使用 HTTPS")
// }
```

生产环境再加回。当前 150 机器后端只支持 HTTP。

### 规则 10：Info.plist 必须由 XcodeGen 配置保留

`xcodegen generate` 会重写 `client/project.yml` 中 `info.path` 指向的 Info.plist。权限描述和菜单栏配置必须写在 `info.properties`，不能只手动编辑 `TypeZero/Info.plist`。每次生成后都要确认：

```bash
plutil -p TypeZero/Info.plist | grep NSMicrophoneUsageDescription
```

缺少该字段时，macOS 会在访问麦克风时以 TCC 隐私违规直接终止应用。

### 规则 11：三种 macOS 权限职责分离

- 麦克风：录音，要求 App 包内有 `NSMicrophoneUsageDescription`。
- 输入监控：`addGlobalMonitorForEvents` 接收其他应用前台时的组合键。
- 辅助功能：通过模拟 `Command + V` 向其他应用粘贴文字。

权限与当前 `TypeZero.app` 的路径和签名绑定。出现 Typeless 或旧 TypeZero 残留项时，删除旧项后重新添加当前构建出的 App；授权后重启客户端或刷新快捷键监听。

### 规则 12：权限最小化与数据边界

- 输入监控只可判断配置的快捷键；不得记录、持久化或上传其他按键内容。
- 辅助功能只可模拟粘贴；不得读取剪贴板或查询、抓取其他应用的 UI 内容。
- 录音与识别文字会发送到配置的服务；生产环境必须使用 HTTPS，日志不得记录音频、完整文字或 API Key。

## 后端环境

- **服务器**: 150.109.246.151（Ubuntu）
- **Go**: 1.23.4，路径 `~/go/go/bin/go`
- **项目路径**: `/home/ubuntu/codex/project/typezero`
- **启动**: `cp .env.example .env` → 填入 API Key → `set -a && source .env && set +a` → `go run ./cmd/server`

## 禁止事项

- ❌ Swift 5.8+ 语法
- ❌ macOS 13+ API（MenuBarExtra、LabeledContent、formStyle）
- ❌ deploymentTarget > 12.0
- ❌ statusItem.button.action
- ❌ Combine 管道观察 @MainActor 属性
- ❌ localMonitor（double-firing）
- ❌ `Task.sleep(for:)` / `.milliseconds(...)`（macOS 13+ API）
