import AppKit
import SwiftUI

final class StatusBarItemView: NSView {
    var onMouseUp: (() -> Void)?

    override func mouseDown(with event: NSEvent) {
        onMouseUp?()
    }

    override func rightMouseDown(with event: NSEvent) {
        onMouseUp?()
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var popover: NSPopover!
    private var popoverContent: NSHostingController<MenuContentView>!
    private var settingsWindow: NSWindow?
    private var model: AppModel!
    private var imageView: NSImageView!
    private var recordingOverlay: RecordingOverlayController!

    func applicationDidFinishLaunching(_ notification: Notification) {
        model = AppModel()
        recordingOverlay = RecordingOverlayController()

        // Callback receives phase value directly (no @MainActor access needed)
        model.onStatusChanged = { [weak self] phase in
            self?.updateIcon(for: phase)
            self?.recordingOverlay.update(for: phase)
            self?.updatePopoverSize(for: phase)
        }
        model.onAudioLevelChanged = { [weak self] level in
            self?.recordingOverlay.updateAudioLevel(level)
        }

        setupStatusBar()
        setupPopover()
    }

    func applicationDidBecomeActive(_ notification: Notification) {
        model?.refreshShortcutMonitoring()
    }

    private func updateIcon(for phase: AppModel.Phase) {
        let iconName: String
        switch phase {
        case .recording: iconName = "waveform.circle.fill"
        case .processing: iconName = "ellipsis.circle"
        case .failure: iconName = "exclamationmark.circle"
        default: iconName = "mic.circle"
        }
        imageView?.image = NSImage(systemSymbolName: iconName, accessibilityDescription: nil)
    }

    private func setupStatusBar() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)

        imageView = NSImageView(image: NSImage(systemSymbolName: "mic.circle", accessibilityDescription: nil)!)
        imageView.frame = NSRect(x: 0, y: 0, width: 24, height: 22)

        let containerView = StatusBarItemView()
        containerView.frame = NSRect(x: 0, y: 0, width: 30, height: 22)
        containerView.addSubview(imageView)
        containerView.onMouseUp = { [weak self] in
            self?.togglePopover()
        }

        statusItem.view = containerView
    }

    private func setupPopover() {
        popover = NSPopover()
        popover.contentSize = NSSize(width: 350, height: 340)
        popover.behavior = .transient
        var menuContent = MenuContentView(model: model)
        menuContent.onOpenSettings = { [weak self] in
            self?.openSettings()
        }
        let hosting = NSHostingController(rootView: menuContent)
        popoverContent = hosting
        popover.contentViewController = hosting
    }

    /// The popover must grow when the raw-text fallback panel is shown
    /// (success/failure states hide it, and the fixed 340pt height would clip
    /// the buttons below the GroupBox on the real device).
    private func updatePopoverSize(for phase: AppModel.Phase) {
        guard popover != nil, popoverContent != nil else { return }
        let size: NSSize
        switch phase {
        case .rawTextAvailable:
            size = NSSize(width: 350, height: 460)
        default:
            size = NSSize(width: 350, height: 340)
        }
        popover.contentSize = size
        popoverContent.preferredContentSize = size
    }

    private func togglePopover() {
        guard let view = statusItem.view else { return }
        if popover.isShown {
            popover.performClose(nil)
        } else {
            // Menu-bar apps (LSUIElement) on macOS 12 do not reliably show a
            // transient NSPopover until the app is active; without activating
            // first, clicking the status item appears to do nothing.
            NSApp.activate(ignoringOtherApps: true)
            popover.show(relativeTo: view.bounds, of: view, preferredEdge: .minY)
            popover.contentViewController?.view.window?.makeKey()
        }
    }

    @objc func openSettings() {
        if let window = settingsWindow, window.isVisible {
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            return
        }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 560, height: 540),
            styleMask: [.titled, .closable, .miniaturizable],
            backing: .buffered,
            defer: false
        )
        window.title = "TypeZero 设置"
        window.contentView = NSHostingView(rootView: SettingsView(model: model))
        window.center()
        window.isReleasedWhenClosed = false
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        settingsWindow = window
    }
}

@main
struct TypeZeroApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        Settings {
            EmptyView()
        }
    }
}
