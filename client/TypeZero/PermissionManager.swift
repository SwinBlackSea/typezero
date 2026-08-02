import AVFoundation
import AppKit
import ApplicationServices

enum PermissionManager {
    static var hasAccessibility: Bool { AXIsProcessTrusted() }
    static var hasInputMonitoring: Bool { CGPreflightListenEventAccess() }

    static var microphoneStatus: String {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized: return "已允许"
        case .denied, .restricted: return "未允许"
        case .notDetermined: return "尚未请求"
        @unknown default: return "未知"
        }
    }

    static func openMicrophoneSettings() {
        guard let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone") else { return }
        NSWorkspace.shared.open(url)
    }

    static func openInputMonitoringSettings() {
        guard let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent") else { return }
        NSWorkspace.shared.open(url)
    }

    static func openAccessibilitySettings() {
        guard let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility") else { return }
        NSWorkspace.shared.open(url)
    }
}
