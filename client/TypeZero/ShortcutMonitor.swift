import AppKit

enum ShortcutChoice: String, CaseIterable, Identifiable {
    case function
    case controlOptionSpace
    case commandShiftSpace

    var id: String { rawValue }

    var title: String {
        switch self {
        case .function: return "Fn 单键（实验性）"
        case .controlOptionSpace: return "Control + Option + Space"
        case .commandShiftSpace: return "Command + Shift + Space"
        }
    }
}

@MainActor
final class ShortcutMonitor {
    var choice: ShortcutChoice = .function
    var onTrigger: (() -> Void)?

    private var globalKeyMonitor: Any?
    private var globalFlagsMonitor: Any?
    private var functionIsDown = false

    func start() {
        stop()
        globalKeyMonitor = NSEvent.addGlobalMonitorForEvents(matching: .keyDown) { [weak self] event in
            Task { @MainActor in self?.handleKeyDown(event) }
        }
        globalFlagsMonitor = NSEvent.addGlobalMonitorForEvents(matching: .flagsChanged) { [weak self] event in
            Task { @MainActor in self?.handleFlagsChanged(event) }
        }
    }

    func stop() {
        if let m = globalKeyMonitor { NSEvent.removeMonitor(m) }
        if let m = globalFlagsMonitor { NSEvent.removeMonitor(m) }
        globalKeyMonitor = nil
        globalFlagsMonitor = nil
        functionIsDown = false
    }

    private func handleFlagsChanged(_ event: NSEvent) {
        let isDown = event.modifierFlags.contains(.function)
        defer { functionIsDown = isDown }
        guard choice == .function, isDown, !functionIsDown else { return }
        onTrigger?()
    }

    private func handleKeyDown(_ event: NSEvent) {
        guard !event.isARepeat, event.keyCode == 49 else { return }
        let modifiers = event.modifierFlags.intersection(.deviceIndependentFlagsMask)
        switch choice {
        case .function:
            return
        case .controlOptionSpace:
            guard modifiers.contains([.control, .option]),
                  !modifiers.contains(.command),
                  !modifiers.contains(.shift) else { return }
        case .commandShiftSpace:
            guard modifiers.contains([.command, .shift]),
                  !modifiers.contains(.control),
                  !modifiers.contains(.option) else { return }
        }
        onTrigger?()
    }
}
