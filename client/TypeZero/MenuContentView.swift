import AppKit
import SwiftUI

struct MenuContentView: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 10) {
                statusIcon
                VStack(alignment: .leading, spacing: 2) {
                    Text(model.statusTitle)
                        .font(.headline)
                    Text(model.shortcut.title)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
            }

            if let rawText = model.rawText {
                GroupBox("原始识别文字") {
                    ScrollView {
                        Text(rawText)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .textSelection(.enabled)
                    }
                    .frame(maxHeight: 130)
                }
                HStack {
                    Button("放弃") { model.dismissRawText() }
                    Spacer()
                    Button("插入原始文字") { model.insertRawText() }
                        .keyboardShortcut(.defaultAction)
                }
            } else {
                Button {
                    Task { await model.toggleRecording() }
                } label: {
                    Label(model.isRecording ? "停止录音" : "开始录音", systemImage: model.isRecording ? "stop.fill" : "mic.fill")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .tint(model.isRecording ? .red : .accentColor)
                .disabled(model.isProcessing)
                .controlSize(.large)
            }

            Divider()

            HStack {
                Button("设置…") {
                    openSettingsWindow()
                }
                Spacer()
                Button("退出") { NSApp.terminate(nil) }
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
        }
        .padding(16)
        .frame(width: 330)
    }

    private var statusIcon: AnyView {
        switch model.phase {
        case .recording:
            return AnyView(Image(systemName: "waveform.circle.fill").foregroundStyle(.red))
        case .processing:
            return AnyView(ProgressView().controlSize(.small))
        case .failure:
            return AnyView(Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange))
        case .success:
            return AnyView(Image(systemName: "checkmark.circle.fill").foregroundStyle(.green))
        case .rawTextAvailable:
            return AnyView(Image(systemName: "exclamationmark.bubble.fill").foregroundStyle(.orange))
        case .idle:
            return AnyView(Image(systemName: "mic.circle.fill").foregroundStyle(.secondary))
        }
    }

    private func openSettingsWindow() {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 560, height: 500),
            styleMask: [.titled, .closable, .miniaturizable],
            backing: .buffered,
            defer: false
        )
        window.title = "TypeZero 设置"
        window.contentView = NSHostingView(rootView: SettingsView(model: model))
        window.center()
        window.isReleasedWhenClosed = false
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }
}
