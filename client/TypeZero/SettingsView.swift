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
                Text("Fn 单键在部分键盘或系统设置下无法被全局监听，此时请选择组合键。")
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
                    Button("麦克风设置") { model.openMicrophoneSettings() }
                    Button("请求辅助功能权限") { model.requestAccessibilityPermission() }
                    Button("刷新") { permissionRefresh = UUID() }
                }
            }
            .id(permissionRefresh)
        }
        .padding()
        .frame(width: 520, height: 380)
    }
}
