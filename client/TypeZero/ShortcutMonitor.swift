import AppKit
import ApplicationServices

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
    private var fnEventTap: CFMachPort?
    private var fnEventTapSource: CFRunLoopSource?
    private var functionIsDown = false

    func start() {
        stop()
        globalKeyMonitor = NSEvent.addGlobalMonitorForEvents(matching: .keyDown) { [weak self] event in
            Task { @MainActor in self?.handleKeyDown(event) }
        }
        startFnEventTap()
    }

    func stop() {
        if let m = globalKeyMonitor { NSEvent.removeMonitor(m) }
        if let m = globalFlagsMonitor { NSEvent.removeMonitor(m) }
        if let source = fnEventTapSource {
            CFRunLoopRemoveSource(CFRunLoopGetMain(), source, .commonModes)
        }
        if let tap = fnEventTap {
            CFMachPortInvalidate(tap)
        }
        globalKeyMonitor = nil
        globalFlagsMonitor = nil
        fnEventTapSource = nil
        fnEventTap = nil
        functionIsDown = false
    }

    private func startFnEventTap() {
        let eventMask = CGEventMask(1) << CGEventType.flagsChanged.rawValue
        guard let tap = CGEvent.tapCreate(
            tap: .cgSessionEventTap,
            place: .headInsertEventTap,
            options: .listenOnly,
            eventsOfInterest: eventMask,
            callback: Self.fnEventTapCallback,
            userInfo: Unmanaged.passUnretained(self).toOpaque()
        ) else {
            startGlobalFnMonitorFallback()
            return
        }

        let source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0)
        fnEventTap = tap
        fnEventTapSource = source
        CFRunLoopAddSource(CFRunLoopGetMain(), source, .commonModes)
        CGEvent.tapEnable(tap: tap, enable: true)
    }

    private func startGlobalFnMonitorFallback() {
        globalFlagsMonitor = NSEvent.addGlobalMonitorForEvents(matching: .flagsChanged) { [weak self] event in
            Task { @MainActor in self?.handleFnFlagsChanged(event.modifierFlags.contains(.function)) }
        }
    }

    private func handleFnFlagsChanged(_ isDown: Bool) {
        defer { functionIsDown = isDown }
        guard choice == .function, isDown, !functionIsDown else { return }
        onTrigger?()
    }

    private func reenableFnEventTap() {
        if let tap = fnEventTap {
            CGEvent.tapEnable(tap: tap, enable: true)
        }
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

    private static let fnEventTapCallback: CGEventTapCallBack = { _, type, event, userInfo in
        guard let userInfo else { return Unmanaged.passUnretained(event) }
        let monitor = Unmanaged<ShortcutMonitor>.fromOpaque(userInfo).takeUnretainedValue()

        switch type {
        case .flagsChanged:
            let isDown = event.flags.contains(.maskSecondaryFn)
            Task { @MainActor [weak monitor] in
                monitor?.handleFnFlagsChanged(isDown)
            }
        case .tapDisabledByTimeout:
            Task { @MainActor [weak monitor] in
                monitor?.reenableFnEventTap()
            }
        default:
            break
        }
        return Unmanaged.passUnretained(event)
    }
}
