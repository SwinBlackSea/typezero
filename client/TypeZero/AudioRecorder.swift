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
    var onLimitReached: ((Recording) -> Void)?

    private var recorder: AVAudioRecorder?
    private var timer: Timer?
    // Leave headroom for the 250 ms timer interval and container metadata written on stop.
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
        timer = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { [weak self] _ in
            DispatchQueue.main.async { self?.tick() }
        }
    }

    func stop() -> Recording? {
        guard let recorder else { return nil }
        let duration = recorder.currentTime
        let url = recorder.url
        recorder.stop()
        timer?.invalidate()
        timer = nil
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
