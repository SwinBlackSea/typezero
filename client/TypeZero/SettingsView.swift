import SwiftUI

struct SettingsView: View {
    @ObservedObject var model: AppModel
    @State private var permissionRefresh = UUID()

    var body: some View {
        Form {
            Section("服务") {
                TextField("服务地址", text: $model.serverURLText)
                    .textFieldStyle(.roundedBorder)
                Text("本机开发可使用 HTTP；远程地址必须使用 HTTPS。")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("模型 API Key（可选）") {
                SecureField("DashScope API Key", text: $model.dashscopeAPIKey)
                    .textFieldStyle(.roundedBorder)
                SecureField("DeepSeek API Key", text: $model.deepSeekAPIKey)
                    .textFieldStyle(.roundedBorder)
                Text("留空时使用服务端配置。填写后仅保存到本机 Keychain，并随当前听写请求发送。")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("快捷键") {
                Picker("开始/停止录音", selection: $model.shortcut) {
                    ForEach(ShortcutChoice.allCases) { shortcut in
                        Text(shortcut.title).tag(shortcut)
                    }
                }
                Text("推荐 Control + Option + Space。Fn 单键在部分键盘或系统设置下无法被全局监听。")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("权限") {
                HStack {
                    Text("麦克风").foregroundColor(.secondary)
                    Spacer()
                    Text(PermissionManager.microphoneStatus)
                }
                HStack {
                    Text("辅助功能").foregroundColor(.secondary)
                    Spacer()
                    Text(PermissionManager.hasAccessibility ? "已允许" : "未允许")
                }
                HStack {
                    Text("输入监控").foregroundColor(.secondary)
                    Spacer()
                    Text(PermissionManager.hasInputMonitoring ? "已允许" : "未允许")
                }
                Text("全局快捷键需要“输入监控”；向其他应用插入文字需要“辅助功能”。")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                HStack {
                    Button("麦克风设置") { model.openMicrophoneSettings() }
                    Button("输入监控设置") { model.openInputMonitoringSettings() }
                }
                HStack {
                    Button("请求辅助功能权限") { model.requestAccessibilityPermission() }
                    Button("请求输入监控权限") { model.requestInputMonitoringPermission() }
                    Button("刷新") {
                        model.refreshShortcutMonitoring()
                        permissionRefresh = UUID()
                    }
                }
            }
            .id(permissionRefresh)
        }
        .padding()
        .frame(width: 560, height: 460)
    }
}
