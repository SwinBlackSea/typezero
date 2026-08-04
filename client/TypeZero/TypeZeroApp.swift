import AppKit
import SwiftUI

final class StatusBarItemView: NSView {
    var onClick: (() -> Void)?

    override func mouseDown(with event: NSEvent) {
        // Deliberately ignore mouseDown: showing a transient NSPopover here
        // lets the same click's mouseUp (which lands outside the popover
        // window) dismiss it instantly on macOS 12. Act on mouseUp instead.
    }

    override func mouseUp(with event: NSEvent) {
        onClick?()
    }

    override func rightMouseDown(with event: NSEvent) {
        onClick?()
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
        containerView.onClick = { [weak self] in
            NSLog("TypeZero: status item clicked")
            self?.togglePopover()
        }

        statusItem.view = containerView
    }

    private func setupPopover() {
        popover = NSPopover()
        popover.behavior = .transient
        var menuContent = MenuContentView(model: model)
        menuContent.onOpenSettings = { [weak self] in
            self?.openSettings()
        }
        let hosting = NSHostingController(rootView: menuContent)
        // macOS 12 sizes the popover from the content controller's
        // preferredContentSize; without an explicit size the SwiftUI hosting
        // controller can report a zero fitting size and the panel shows up
        // blank or not at all.
        hosting.preferredContentSize = NSSize(width: 350, height: 340)
        popover.contentSize = hosting.preferredContentSize
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
        guard let statusItem = statusItem, let view = statusItem.view else {
            NSLog("TypeZero: togglePopover without status view")
            return
        }
        if popover.isShown {
            NSLog("TypeZero: closing popover")
            popover.performClose(nil)
            return
        }
        NSLog("TypeZero: showing popover")
        // Menu-bar apps (LSUIElement) on macOS 12 need the app active before a
        // transient NSPopover will display, and showing it in the same event
        // as the activation can still get swallowed. Defer the show to the
        // next runloop tick so the click reliably opens the panel.
        NSApp.activate(ignoringOtherApps: true)
        DispatchQueue.main.async { [weak self] in
            guard let self,
                  let statusItem = self.statusItem,
                  let view = statusItem.view else { return }
            self.popover.show(relativeTo: view.bounds, of: view, preferredEdge: .minY)
            self.popover.contentViewController?.view.window?.makeKey()
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
