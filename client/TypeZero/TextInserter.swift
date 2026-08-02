import AppKit
import ApplicationServices

enum InsertionResult {
    case inserted
    case copied
}

struct TextInserter {
    func insert(_ text: String) -> InsertionResult {
        let systemWide = AXUIElementCreateSystemWide()
        var focusedValue: CFTypeRef?
        let focusedStatus = AXUIElementCopyAttributeValue(
            systemWide,
            kAXFocusedUIElementAttribute as CFString,
            &focusedValue
        )

        if focusedStatus == .success, let focusedValue {
            let focused = focusedValue as! AXUIElement
            let setStatus = AXUIElementSetAttributeValue(
                focused,
                kAXSelectedTextAttribute as CFString,
                text as CFTypeRef
            )
            if setStatus == .success {
                return .inserted
            }
        }

        copyToPasteboard(text)
        simulatePasteIfTrusted()
        return .copied
    }

    private func copyToPasteboard(_ text: String) {
        let pasteboard = NSPasteboard.general
        pasteboard.clearContents()
        pasteboard.setString(text, forType: .string)
    }

    private func simulatePasteIfTrusted() {
        guard AXIsProcessTrusted(),
              let keyDown = CGEvent(keyboardEventSource: nil, virtualKey: 9, keyDown: true),
              let keyUp = CGEvent(keyboardEventSource: nil, virtualKey: 9, keyDown: false) else { return }
        keyDown.flags = .maskCommand
        keyUp.flags = .maskCommand
        keyDown.post(tap: .cghidEventTap)
        keyUp.post(tap: .cghidEventTap)
    }
}
