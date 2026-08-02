import AVFoundation
import Foundation

struct Recording: Sendable {
    let url: URL
    let durationMilliseconds: Int
}

enum RecorderError: LocalizedError {
    case microphoneDenied
    case failedToStart

    var errorDescription: String? {
        switch self {
        case .microphoneDenied: return "请在系统设置中允许 TypeZero 使用麦克风"
        case .failedToStart: return "无法开始录音，请检查麦克风"
        }
    }
}

@MainActor
final class AudioRecorder: NSObject, AVAudioRecorderDelegate {
    var onElapsed: ((TimeInterval) -> Void)?
    var onAudioLevel: ((CGFloat) -> Void)?
    var onLimitReached: ((Recording) -> Void)?

    private var recorder: AVAudioRecorder?
    private var tickTask: Task<Void, Never>?
    private let maxDuration: TimeInterval = 299
    private let maxBytes: Int64 = (10 << 20) - (64 << 10)

    override init() {
        super.init()
        removeStaleRecordings()
    }

    func start() async throws {
        guard await microphonePermission() else {
            throw RecorderError.microphoneDenied
        }

        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("typezero-\(UUID().uuidString)")
            .appendingPathExtension("m4a")
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: 16_000,
            AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: 32_000,
            AVEncoderAudioQualityKey: AVAudioQuality.high.rawValue,
        ]

        let newRecorder = try AVAudioRecorder(url: url, settings: settings)
        newRecorder.delegate = self
        newRecorder.isMeteringEnabled = true
        guard newRecorder.prepareToRecord(), newRecorder.record() else {
            try? FileManager.default.removeItem(at: url)
            throw RecorderError.failedToStart
        }
        recorder = newRecorder

        tickTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.tick()
                try? await Task.sleep(nanoseconds: 80_000_000)
            }
        }
    }

    func stop() -> Recording? {
        guard let recorder else { return nil }
        let duration = recorder.currentTime
        let url = recorder.url
        recorder.stop()
        tickTask?.cancel()
        tickTask = nil
        self.recorder = nil

        guard duration > 0 else {
            try? FileManager.default.removeItem(at: url)
            return nil
        }
        return Recording(url: url, durationMilliseconds: max(1, Int(duration * 1000)))
    }

    private func tick() {
        guard let recorder else { return }
        let elapsed = recorder.currentTime
        onElapsed?(elapsed)

        recorder.updateMeters()
        let power = recorder.averagePower(forChannel: 0)
        let normalizedLevel = max(0, min(1, (power + 52) / 52))
        onAudioLevel?(CGFloat(sqrt(normalizedLevel)))

        let size = (try? recorder.url.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init) ?? 0
        if elapsed >= maxDuration || size >= maxBytes {
            if let recording = stop() {
                onLimitReached?(recording)
            }
        }
    }

    private func microphonePermission() async -> Bool {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized:
            return true
        case .notDetermined:
            return await AVCaptureDevice.requestAccess(for: .audio)
        default:
            return false
        }
    }

    private func removeStaleRecordings() {
        let directory = FileManager.default.temporaryDirectory
        guard let files = try? FileManager.default.contentsOfDirectory(
            at: directory,
            includingPropertiesForKeys: nil,
            options: [.skipsHiddenFiles]
        ) else { return }
        for file in files where file.lastPathComponent.hasPrefix("typezero-") && file.pathExtension == "m4a" {
            try? FileManager.default.removeItem(at: file)
        }
    }
}
