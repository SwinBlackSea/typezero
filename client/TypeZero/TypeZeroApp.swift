import AppKit
import SwiftUI

struct StatusBarIconView: View {
    var model: AppModel
    var onTap: () -> Void

    var body: some View {
        Image(systemName: "mic.circle")
            .font(.system(size: 18))
            .onTapGesture { onTap() }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var popover: NSPopover!
    private var settingsWindow: NSWindow?
    private var model: AppModel!

    func applicationDidFinishLaunching(_ notification: Notification) {
        print("AppDelegate.applicationDidFinishLaunching")
        model = AppModel()
        setupStatusBar()
        setupPopover()
    }

    private func setupStatusBar() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)

        let iconView = StatusBarIconView(model: model, onTap: { [weak self] in
            self?.togglePopover()
        })
        let hosting = NSHostingView(rootView: iconView)
        hosting.frame = NSRect(x: 0, y: 0, width: 30, height: 22)
        statusItem.view = hosting
        print("Status bar with custom view set up")
    }

    private func setupPopover() {
        popover = NSPopover()
        popover.contentSize = NSSize(width: 350, height: 340)
        popover.behavior = .transient
        popover.contentViewController = NSHostingController(
            rootView: MenuContentView(model: model)
        )
        print("Popover set up")
    }

    private func togglePopover() {
        print("togglePopover called, isShown=\(popover?.isShown ?? false)")
        guard let view = statusItem.view else { return }
        if popover.isShown {
            popover.performClose(nil)
        } else {
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
            contentRect: NSRect(x: 0, y: 0, width: 540, height: 420),
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
