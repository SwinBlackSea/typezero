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
- **打开 App**: 编译产物在 `~/Library/Developer/Xcode/DerivedData/.../Release/TypeZero.app`

## Swift 5.7 兼容规则（必须遵守，否则编译失败）

### 规则 1：switch 必须显式 return

Swift 5.7 不支持 switch 表达式的隐式返回。**每个 case 的每个分支必须写 `return`**：

```swift
// ❌ 编译失败 — Swift 5.7 要求显式 return
var title: String {
    switch self {
    case .a: "hello"
    case .b: "world"
    }
}

// ✅ 正确
var title: String {
    switch self {
    case .a: return "hello"
    case .b: return "world"
    }
}
```

涉及模式匹配的 case 同理：

```swift
// ❌
case .success(let msg): msg

// ✅
case .success(let msg): return msg
```

### 规则 2：禁止使用 macOS 13+ API

以下 API 在 Monterey 上**编译通过但运行崩溃**（kLSIncompatibleSystemVersionErr），**绝对禁止**：

| 禁止 | 替代方案 |
|------|---------|
| `MenuBarExtra` | `NSStatusBar.system.statusItem` + `NSStatusItem.view` + 自定义 `NSView` 子类重写 `mouseDown` |
| `LabeledContent("标签", value: ...)` | `HStack { Text("标签").foregroundColor(.secondary); Spacer(); Text(...) }` |
| `.formStyle(.grouped)` | 去掉该 modifier，macOS 12 上 Form 默认即 grouped |
| `.menuBarExtraStyle(.window)` | 不需要，改用 `NSPopover` |

### 规则 3：@MainActor 并发

Timer 闭包内捕获 `@MainActor` 类的 `self` 会触发并发错误。**Timer 闭包必须标注 `@MainActor`**：

```swift
// ❌ 编译失败
timer = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { [weak self] _ in
    Task { @MainActor in self?.tick() }
}

// ✅ 正确
timer = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { @MainActor [weak self] _ in
    self?.tick()
}
```

### 规则 4：project.yml 固定配置

`client/project.yml` 必须包含以下设置，**不得修改**：

```yaml
deploymentTarget:
  macOS: "12.0"
settings:
  base:
    SWIFT_VERSION: "5.0"
    SWIFT_STRICT_CONCURRENCY: minimal
```

### 规则 5：菜单栏点击实现

macOS 12 上 `NSStatusBar.button.action` / `NSStatusBar.button.target` **不触发**。必须用自定义 NSView：

```swift
// 自定义 NSView 子类 — 重写 mouseDown 捕获点击
class StatusBarItemView: NSView {
    var onMouseUp: (() -> Void)?
    override func mouseDown(with event: NSEvent) { onMouseUp?() }
}

// 使用：
let containerView = StatusBarItemView()
containerView.frame = NSRect(x: 0, y: 0, width: 30, height: 22)
containerView.onMouseUp = { [weak self] in self?.togglePopover() }
statusItem.view = containerView
```

不要用 `NSHostingView` 包裹 SwiftUI View 放到 statusItem.view 里（`onTapGesture` 不响应）。

## 后端环境

- **服务器**: 150.109.246.151（Ubuntu）
- **Go**: 1.23.4，路径 `~/go/go/bin/go`
- **项目路径**: `/home/ubuntu/codex/project/typezero`
- **编译**: `go build ./...`
- **启动**: 复制 `.env.example` 为 `.env`，填入 API Key，`go run ./cmd/server`

## 禁止事项

- ❌ 使用 Swift 5.8+ 语法（if/switch 表达式隐式返回）
- ❌ 使用任何 macOS 13+ 专属 API
- ❌ 修改 `deploymentTarget` 高于 12.0
- ❌ 使用 `LabeledContent`、`MenuBarExtra`、`.formStyle(.grouped)`
- ❌ NSStatusBar.button.action（macOS 12 不触发）
