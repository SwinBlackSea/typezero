import SwiftUI

struct SettingsView: View {
    @ObservedObject var model: AppModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                VStack(alignment: .leading, spacing: 5) {
                    Text("TypeZero 设置")
                        .font(.system(size: 22, weight: .semibold))
                    Text("配置听写服务、快捷键和系统权限。")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }

                SettingsSection("服务") {
                    VStack(alignment: .leading, spacing: 9) {
                        Text("服务地址")
                            .font(.subheadline)
                        TextField("http://127.0.0.1:8080", text: $model.serverURLText)
                            .textFieldStyle(.roundedBorder)
                        Text("开发阶段可使用 HTTP；正式发布时请使用 HTTPS。")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }

                SettingsSection("快捷键") {
                    HStack(spacing: 16) {
                        VStack(alignment: .leading, spacing: 3) {
                            Text("开始 / 停止录音")
                                .font(.subheadline)
                            Text("推荐 Control + Option + Space。")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        Spacer(minLength: 12)
                        Picker("开始 / 停止录音", selection: $model.shortcut) {
                            ForEach(ShortcutChoice.allCases) { shortcut in
                                Text(shortcut.title).tag(shortcut)
                            }
                        }
                        .labelsHidden()
                        .pickerStyle(.menu)
                        .frame(width: 220)
                    }
                }

                SettingsSection("模型密钥（可选）") {
                    VStack(alignment: .leading, spacing: 10) {
                        SecureField("DashScope API Key", text: $model.dashscopeAPIKey)
                            .textFieldStyle(.roundedBorder)
                        SecureField("DeepSeek API Key", text: $model.deepSeekAPIKey)
                            .textFieldStyle(.roundedBorder)
                        Text("留空时使用服务端配置。填写后仅存储在本机 Keychain。")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }

                SettingsSection("系统权限") {
                    VStack(spacing: 0) {
                        PermissionRow(
                            title: "麦克风",
                            detail: "用于录制语音",
                            status: PermissionManager.microphoneStatus,
                            isAllowed: PermissionManager.microphoneStatus == "已允许",
                            action: model.openMicrophoneSettings
                        )
                        Divider()
                        PermissionRow(
                            title: "输入监控",
                            detail: "用于在其他应用中响应快捷键",
                            status: PermissionManager.hasInputMonitoring ? "已允许" : "未允许",
                            isAllowed: PermissionManager.hasInputMonitoring,
                            action: model.openInputMonitoringSettings
                        )
                        Divider()
                        PermissionRow(
                            title: "辅助功能",
                            detail: "用于向当前光标位置粘贴文字",
                            status: PermissionManager.hasAccessibility ? "已允许" : "未允许",
                            isAllowed: PermissionManager.hasAccessibility,
                            action: model.openAccessibilitySettings
                        )
                    }
                }
                .id(model.permissionRevision)

                Text("修改权限后，回到 TypeZero 即可自动重新加载快捷键。")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(24)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .frame(width: 560, height: 540)
    }
}

private struct SettingsSection<Content: View>: View {
    let title: String
    let content: Content

    init(_ title: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            Text(title)
                .font(.headline)
            content
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(nsColor: .controlBackgroundColor))
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
    }
}

private struct PermissionRow: View {
    let title: String
    let detail: String
    let status: String
    let isAllowed: Bool
    let action: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                    .font(.subheadline)
                Text(detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 12)
            Text(status)
                .font(.caption)
                .foregroundColor(isAllowed ? .green : .orange)
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
                .background((isAllowed ? Color.green : Color.orange).opacity(0.12))
                .clipShape(Capsule())
            Button("打开设置", action: action)
                .buttonStyle(.borderless)
                .foregroundColor(.accentColor)
        }
        .padding(.vertical, 9)
    }
}
