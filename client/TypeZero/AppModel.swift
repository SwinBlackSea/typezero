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
        case rawTextAvailable(text: String, title: String)
    }

    @Published private(set) var phase: Phase = .idle
    @Published private(set) var elapsedSeconds: Int = 0
    @Published private(set) var permissionRevision = UUID()
    @Published private(set) var lastProcessingTiming: ProcessingTiming?
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
    private let feedbackTonePlayer = FeedbackTonePlayer()
    private let shortcutMonitor = ShortcutMonitor()
    private var processingTask: Task<Void, Never>?
    private var hasRequestedSystemPermissions = false
    private var incrementalSessionID: String?
    private var uploadedChunkIndexes = Set<Int>()
    private var incrementalTask: Task<Void, Never>?
    private var incrementalFailed = false
    var onStatusChanged: ((Phase) -> Void)?
    var onAudioLevelChanged: ((CGFloat) -> Void)?

    private func setPhase(_ newPhase: Phase) {
        phase = newPhase
        onStatusChanged?(newPhase)
    }

    init() {
        serverURLText = UserDefaults.standard.string(forKey: Self.serverURLKey) ?? "http://127.0.0.1:8080"
        let savedShortcut = UserDefaults.standard.string(forKey: Self.shortcutKey)
        shortcut = ShortcutChoice(rawValue: savedShortcut ?? "") ?? .controlOptionSpace
        dashscopeAPIKey = KeychainStore.string(for: "dashscope-api-key")
        deepSeekAPIKey = KeychainStore.string(for: "deepseek-api-key")

        shortcutMonitor.choice = shortcut
        shortcutMonitor.onTrigger = { @MainActor [weak self] in
            Task { @MainActor in await self?.toggleRecording() }
        }
        shortcutMonitor.start()

        recorder.onElapsed = { @MainActor [weak self] elapsed in
            self?.elapsedSeconds = Int(elapsed)
        }
        recorder.onAudioLevel = { @MainActor [weak self] level in
            self?.onAudioLevelChanged?(level)
        }
        recorder.onLimitReached = { @MainActor [weak self] recording in
            guard let self else { return }
            let sessionID = self.incrementalSessionID
            let uploaded = self.uploadedChunkIndexes
            let failed = self.incrementalFailed
            self.teardownIncrementalUpload()
            self.setPhase(.processing)
            self.process(
                recording,
                incrementalSessionID: sessionID,
                alreadyUploaded: uploaded,
                incrementalFailed: failed
            )
        }
    }

    deinit {
        processingTask?.cancel()
    }

    var isRecording: Bool { phase == .recording }
    var isProcessing: Bool { phase == .processing }

    var menuBarIcon: String {
        switch phase {
        case .recording: return "waveform.circle.fill"
        case .processing: return "ellipsis.circle"
        case .failure: return "exclamationmark.circle"
        default: return "mic.circle"
        }
    }

    var statusTitle: String {
        switch phase {
        case .idle: return "就绪"
        case .recording: return "正在录音 \(formattedElapsed)"
        case .processing: return "正在识别和整理…"
        case .success(let message): return message
        case .failure(let message): return message
        case .rawTextAvailable(_, let title): return title
        }
    }

    var formattedElapsed: String {
        String(format: "%d:%02d", elapsedSeconds / 60, elapsedSeconds % 60)
    }

    var rawText: String? {
        guard case .rawTextAvailable(let text, _) = phase else { return nil }
        return text
    }

    func toggleRecording() async {
        guard !isProcessing else { return }
        if isRecording {
            let sessionID = incrementalSessionID
            let uploaded = uploadedChunkIndexes
            let failed = incrementalFailed
            teardownIncrementalUpload()
            guard let recording = recorder.stop() else {
                setPhase(.failure("录音停止失败"))
                return
            }
            setPhase(.processing)
            feedbackTonePlayer.playStop()
            process(
                recording,
                incrementalSessionID: sessionID,
                alreadyUploaded: uploaded,
                incrementalFailed: failed
            )
            return
        }

        requestSystemPermissionsOnFirstRecording()
        do {
            try await recorder.start()
            elapsedSeconds = 0
            lastProcessingTiming = nil
            beginIncrementalUpload()
            setPhase(.recording)
            feedbackTonePlayer.playStart()
        } catch {
            setPhase(.failure(error.localizedDescription))
        }
    }

    func insertRawText() {
        guard let rawText else { return }
        finishInsertion(rawText)
    }

    func dismissRawText() {
        setPhase(.idle)
    }

    func resetStatus() {
        guard !isRecording && !isProcessing else { return }
        setPhase(.idle)
    }

    func cancelRecording() {
        guard isRecording else { return }
        teardownIncrementalUpload()
        if let recording = recorder.stop() {
            try? FileManager.default.removeItem(at: recording.url)
        }
        setPhase(.idle)
    }

    func openMicrophoneSettings() {
        PermissionManager.openMicrophoneSettings()
    }

    func openInputMonitoringSettings() {
        PermissionManager.openInputMonitoringSettings()
    }

    func openAccessibilitySettings() {
        PermissionManager.openAccessibilitySettings()
    }

    func refreshShortcutMonitoring() {
        shortcutMonitor.start()
        permissionRevision = UUID()
    }

    private func requestSystemPermissionsOnFirstRecording() {
        guard !hasRequestedSystemPermissions else { return }
        hasRequestedSystemPermissions = true

        PermissionManager.requestInputMonitoringIfNeeded()
        PermissionManager.requestAccessibilityIfNeeded()
        refreshShortcutMonitoring()
    }

    private func process(
        _ recording: Recording,
        incrementalSessionID: String?,
        alreadyUploaded: Set<Int>,
        incrementalFailed: Bool
    ) {
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
                let sessionID = (incrementalSessionID != nil && !incrementalFailed && !alreadyUploaded.isEmpty)
                    ? incrementalSessionID
                    : nil
                let result = try await Task.detached(priority: .userInitiated) {
                    if let sessionID {
                        return try await client.finishChunkedUpload(
                            recording: recording,
                            sessionID: sessionID,
                            alreadyUploaded: alreadyUploaded
                        )
                    }
                    return try await client.uploadChunked(recording: recording)
                }.value
                try Task.checkCancellation()
                let response = result.response
                self.lastProcessingTiming = result.timing

                if response.warning != nil || (response.finalText?.isEmpty ?? true) {
                    let title = response.warning?.code == "no_speech" ? "未检测到语音" : "润色失败"
                    self.setPhase(.rawTextAvailable(text: response.rawText, title: title))
                    return
                }
                self.finishInsertion(response.finalText ?? response.rawText)
            } catch is CancellationError {
                self.setPhase(.idle)
            } catch {
                self.setPhase(.failure(error.localizedDescription))
            }
        }
    }

    /// Starts the incremental pipeline: while recording, every completed
    /// chunk is transcribed on the server in the background so that when the
    /// user stops, only the tail chunk's ASR and the merge remain.
    private func beginIncrementalUpload() {
        incrementalSessionID = UUID().uuidString
        uploadedChunkIndexes = []
        incrementalFailed = false
        incrementalTask?.cancel()
        incrementalTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.flushCompletedChunks()
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    private func teardownIncrementalUpload() {
        incrementalTask?.cancel()
        incrementalTask = nil
        incrementalSessionID = nil
        uploadedChunkIndexes = []
        incrementalFailed = false
    }

    /// Reads the in-progress recording, chunks it, and uploads every chunk
    /// that is complete (not the still-growing tail) and not yet uploaded.
    /// The server keeps per-session transcripts, so a chunk is uploaded once.
    private func flushCompletedChunks() async {
        guard !incrementalFailed,
              let sessionID = incrementalSessionID,
              let url = recorder.recordingURL,
              let audio = try? Data(contentsOf: url, options: .mappedIfSafe),
              !audio.isEmpty else { return }
        let chunks = AudioChunker.chunk(wavData: audio)
        guard chunks.count > 1 else { return }
        let toUpload = chunks.dropLast().filter { !uploadedChunkIndexes.contains($0.chunkIndex) }
        guard !toUpload.isEmpty else { return }

        let endpoint: URL
        do {
            endpoint = try validatedEndpoint()
        } catch {
            incrementalFailed = true
            incrementalTask?.cancel()
            return
        }
        let client = DictationClient(
            endpoint: endpoint,
            dashscopeAPIKey: dashscopeAPIKey,
            deepSeekAPIKey: deepSeekAPIKey
        )

        for chunk in toUpload {
            // Mark optimistically so the 2s poller does not re-upload a chunk
            // whose ASR is still in flight; on failure unmark and fall back
            // to a full upload when the recording stops.
            uploadedChunkIndexes.insert(chunk.chunkIndex)
        }
        await withTaskGroup(of: (Int, Bool).self) { group in
            for chunk in toUpload {
                group.addTask { [client, sessionID, chunk] in
                    do {
                        try await client.uploadChunk(sessionID: sessionID, chunk: chunk, chunkTotal: 0)
                        return (chunk.chunkIndex, true)
                    } catch {
                        return (chunk.chunkIndex, false)
                    }
                }
            }
            for await (index, succeeded) in group {
                if !succeeded {
                    uploadedChunkIndexes.remove(index)
                    incrementalFailed = true
                }
            }
        }
        if incrementalFailed {
            incrementalTask?.cancel()
        }
    }

    private func finishInsertion(_ text: String) {
        let result = inserter.insert(text)
        switch result {
        case .inserted:
            setPhase(.success("已插入"))
        case .copied:
            setPhase(.success("无法直接插入，文字已复制"))
        }
    }

    private func validatedEndpoint() throws -> URL {
        guard var components = URLComponents(string: serverURLText.trimmingCharacters(in: .whitespacesAndNewlines)),
              let scheme = components.scheme?.lowercased(),
              scheme == "https" || scheme == "http",
              components.host != nil else {
            throw ClientError.configuration("服务地址无效")

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
