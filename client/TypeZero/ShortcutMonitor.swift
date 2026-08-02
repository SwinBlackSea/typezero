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
    private var localMonitor: Any?
    private var functionIsDown = false

    func start() {
        stop()
        globalKeyMonitor = NSEvent.addGlobalMonitorForEvents(matching: .keyDown) { @MainActor [weak self] event in
            Task { @MainActor in self?.handleKeyDown(event) }
        }
        globalFlagsMonitor = NSEvent.addGlobalMonitorForEvents(matching: .flagsChanged) { @MainActor [weak self] event in
            Task { @MainActor in self?.handleFlagsChanged(event) }
        }
        localMonitor = NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .flagsChanged]) { @MainActor [weak self] event in
            Task { @MainActor in self?.handle(event) }
            return event
        }
    }

    func stop() {
        if let globalKeyMonitor { NSEvent.removeMonitor(globalKeyMonitor) }
        if let globalFlagsMonitor { NSEvent.removeMonitor(globalFlagsMonitor) }
        if let localMonitor { NSEvent.removeMonitor(localMonitor) }
        globalKeyMonitor = nil
        globalFlagsMonitor = nil
        localMonitor = nil
        functionIsDown = false
    }

    private func handle(_ event: NSEvent) {
        if event.type == .keyDown {
            handleKeyDown(event)
        } else {
            handleFlagsChanged(event)
        }
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
