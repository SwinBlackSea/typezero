import AppKit
import ApplicationServices

enum InsertionResult {
    case inserted
    case copied
}

struct TextInserter {
    func insert(_ text: String) -> InsertionResult {
        copyToPasteboard(text)
        guard simulatePasteIfTrusted() else { return .copied }
        return .inserted
    }

    private func copyToPasteboard(_ text: String) {
        let pasteboard = NSPasteboard.general
        pasteboard.clearContents()
        pasteboard.setString(text, forType: .string)
    }

    private func simulatePasteIfTrusted() -> Bool {
        guard AXIsProcessTrusted(),
              let keyDown = CGEvent(keyboardEventSource: nil, virtualKey: 9, keyDown: true),
              let keyUp = CGEvent(keyboardEventSource: nil, virtualKey: 9, keyDown: false) else { return false }
        keyDown.flags = .maskCommand
        keyUp.flags = .maskCommand
        keyDown.post(tap: .cghidEventTap)
        keyUp.post(tap: .cghidEventTap)
        return true
    }
}
