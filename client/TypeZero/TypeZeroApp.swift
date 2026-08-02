import AppKit
import SwiftUI

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var popover: NSPopover!
    private var settingsWindow: NSWindow?
    private var model: AppModel!

    func applicationDidFinishLaunching(_ notification: Notification) {
        print("AppDelegate.applicationDidFinishLaunching")
        model = AppModel()
        print("AppModel created")
        setupStatusBar()
        print("StatusBar setup complete")
        setupPopover()
        print("Popover setup complete")
    }

    private func setupStatusBar() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        guard let button = statusItem.button else {
            print("ERROR: statusItem.button is nil")
            return
        }
        button.image = NSImage(systemSymbolName: "mic.circle", accessibilityDescription: "TypeZero")
        button.target = self
        button.action = #selector(statusBarClicked)
        button.sendAction(on: [.leftMouseDown, .leftMouseUp, .rightMouseDown])
        print("Button configured")
    }

    private func setupPopover() {
        popover = NSPopover()
        popover.contentSize = NSSize(width: 350, height: 320)
        popover.behavior = .transient
        popover.contentViewController = NSHostingController(
            rootView: MenuContentView(model: model)
        )
    }

    @objc private func statusBarClicked(_ sender: NSStatusBarButton) {
        print("statusBarClicked, isShown=\(popover?.isShown ?? false)")
        guard let event = NSApp.currentEvent else { return }
        if popover.isShown {
            popover.performClose(sender)
        } else {
            popover.show(relativeTo: sender.bounds, of: sender, preferredEdge: .minY)
        }
    }

    @objc func openSettings() {
        if let window = settingsWindow, window.isVisible {
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            return
        }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 540, height: 400),
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
