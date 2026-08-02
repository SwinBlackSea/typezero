import AppKit
import Foundation

@MainActor
final class AppModel: ObservableObject {
    enum Phase: Equatable {
        case idle
        case recording
        case processing
        case success(String)
        case failure(String)
        case rawTextAvailable(String)
    }

    @Published private(set) var phase: Phase = .idle
    @Published private(set) var elapsedSeconds: Int = 0
    @Published var serverURLText: String {
        didSet { UserDefaults.standard.set(serverURLText, forKey: Self.serverURLKey) }
    }
    @Published var shortcut: ShortcutChoice {
        didSet {
            UserDefaults.standard.set(shortcut.rawValue, forKey: Self.shortcutKey)
            shortcutMonitor.choice = shortcut
        }
    }
    @Published var dashscopeAPIKey: String {
        didSet { KeychainStore.set(dashscopeAPIKey, for: "dashscope-api-key") }
    }
    @Published var deepSeekAPIKey: String {
        didSet { KeychainStore.set(deepSeekAPIKey, for: "deepseek-api-key") }
    }

    private static let serverURLKey = "serverURL"
    private static let shortcutKey = "shortcutChoice"

    private let recorder = AudioRecorder()
    private let inserter = TextInserter()
    private let shortcutMonitor = ShortcutMonitor()
    private var processingTask: Task<Void, Never>?

    init() {
        serverURLText = UserDefaults.standard.string(forKey: Self.serverURLKey) ?? "http://127.0.0.1:8080"
        let savedShortcut = UserDefaults.standard.string(forKey: Self.shortcutKey)
        shortcut = ShortcutChoice(rawValue: savedShortcut ?? "") ?? .function
        dashscopeAPIKey = KeychainStore.string(for: "dashscope-api-key")
        deepSeekAPIKey = KeychainStore.string(for: "deepseek-api-key")

        shortcutMonitor.choice = shortcut
        shortcutMonitor.onTrigger = { [weak self] in
            Task { @MainActor in await self?.toggleRecording() }
        }
        shortcutMonitor.start()

        recorder.onElapsed = { [weak self] elapsed in
            self?.elapsedSeconds = Int(elapsed)
        }
        recorder.onLimitReached = { [weak self] recording in
            self?.phase = .processing
            self?.process(recording)
        }
    }

    deinit {
        processingTask?.cancel()
    }

    var isRecording: Bool { phase == .recording }
    var isProcessing: Bool { phase == .processing }

    var menuBarIcon: String {
        switch phase {
        case .recording: "waveform.circle.fill"
        case .processing: "ellipsis.circle"
        case .failure: "exclamationmark.circle"
        default: "mic.circle"
        }
    }

    var statusTitle: String {
        switch phase {
        case .idle: "就绪"
        case .recording: "正在录音 \(formattedElapsed)"
        case .processing: "正在识别和整理…"
        case .success(let message): message
        case .failure(let message): message
        case .rawTextAvailable: "润色失败"
        }
    }

    var formattedElapsed: String {
        String(format: "%d:%02d", elapsedSeconds / 60, elapsedSeconds % 60)
    }

    var rawText: String? {
        guard case .rawTextAvailable(let text) = phase else { return nil }
        return text
    }

    func toggleRecording() async {
        guard !isProcessing else { return }
        if isRecording {
            guard let recording = recorder.stop() else {
                phase = .failure("录音停止失败")
                return
            }
            phase = .processing
            process(recording)
            return
        }

        do {
            try await recorder.start()
            elapsedSeconds = 0
            phase = .recording
        } catch {
            phase = .failure(error.localizedDescription)
        }
    }

    func insertRawText() {
        guard let rawText else { return }
        finishInsertion(rawText)
    }

    func dismissRawText() {
        phase = .idle
    }

    func resetStatus() {
        guard !isRecording && !isProcessing else { return }
        phase = .idle
    }

    func requestAccessibilityPermission() {
        PermissionManager.requestAccessibility(prompt: true)
    }

    func openMicrophoneSettings() {
        PermissionManager.openMicrophoneSettings()
    }

    private func process(_ recording: Recording) {
        processingTask?.cancel()
        processingTask = Task { [weak self] in
            guard let self else { return }
            defer { try? FileManager.default.removeItem(at: recording.url) }

            do {
                let endpoint = try self.validatedEndpoint()
                let client = DictationClient(
                    endpoint: endpoint,
                    dashscopeAPIKey: self.dashscopeAPIKey,
                    deepSeekAPIKey: self.deepSeekAPIKey
                )
                let response = try await Task.detached(priority: .userInitiated) {
                    try await client.upload(recording: recording)
                }.value
                try Task.checkCancellation()

                if response.warning != nil || response.finalText.isEmpty {
                    self.phase = .rawTextAvailable(response.rawText)
                    return
                }
                self.finishInsertion(response.finalText)
            } catch is CancellationError {
                self.phase = .idle
            } catch {
                self.phase = .failure(error.localizedDescription)
            }
        }
    }

    private func finishInsertion(_ text: String) {
        let result = inserter.insert(text)
        switch result {
        case .inserted:
            phase = .success("已插入")
        case .copied:
            phase = .success("无法直接插入，文字已复制")
        }
    }

    private func validatedEndpoint() throws -> URL {
        guard var components = URLComponents(string: serverURLText.trimmingCharacters(in: .whitespacesAndNewlines)),
              let scheme = components.scheme?.lowercased(),
              scheme == "https" || scheme == "http",
              components.host != nil else {
            throw ClientError.configuration("服务地址无效")
        }
        if scheme == "http", components.host != "127.0.0.1", components.host != "localhost" {
            throw ClientError.configuration("远程服务必须使用 HTTPS")
        }
        components.path = "/v1/dictations"
        components.query = nil
        components.fragment = nil
        guard let endpoint = components.url else {
            throw ClientError.configuration("服务地址无效")
        }
        return endpoint
    }
}
