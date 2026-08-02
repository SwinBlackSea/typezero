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
                    NSApp.sendAction(Selector(("showSettingsWindow:")), to: nil, from: nil)
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

    @ViewBuilder
    private var statusIcon: some View {
        switch model.phase {
        case .recording:
            Image(systemName: "waveform.circle.fill").foregroundStyle(.red)
        case .processing:
            ProgressView().controlSize(.small)
        case .failure:
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
        case .success:
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .rawTextAvailable:
            Image(systemName: "exclamationmark.bubble.fill").foregroundStyle(.orange)
        case .idle:
            Image(systemName: "mic.circle.fill").foregroundStyle(.secondary)
        }
    }
}
