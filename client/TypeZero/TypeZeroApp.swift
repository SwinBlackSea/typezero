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
    }

    private func setupStatusBar() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        guard let button = statusItem.button else {
            print("ERROR: statusItem.button is nil")
            return
        }
        button.image = NSImage(systemSymbolName: "mic.circle", accessibilityDescription: "TypeZero")
        button.action = #selector(togglePopover)
        button.target = self
        button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        print("Button configured: \(button.frame)")

        let contentView = MenuContentView(model: model)
        popover = NSPopover()
        popover.contentSize = NSSize(width: 350, height: 300)
        popover.behavior = .transient
        popover.contentViewController = NSHostingController(rootView: contentView)
        print("Popover setup complete")
    }

    @objc private func togglePopover() {
        print("togglePopover called, isShown=\(popover?.isShown ?? false)")
        guard let button = statusItem?.button else { return }
        if popover.isShown {
            popover.performClose(nil)
        } else {
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
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
