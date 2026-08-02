import AppKit

final class RecordingOverlayController {
    private let panel: NSPanel
    private let overlayView: RecordingOverlayView
    private var hideWorkItem: DispatchWorkItem?

    init(onCancel: @escaping () -> Void, onStop: @escaping () -> Void) {
        overlayView = RecordingOverlayView(frame: NSRect(x: 0, y: 0, width: 236, height: 68))
        panel = NSPanel(
            contentRect: overlayView.bounds,
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        overlayView.onCancel = onCancel
        overlayView.onStop = onStop

        panel.contentView = overlayView
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.hidesOnDeactivate = false
        panel.isMovable = false
        panel.level = .statusBar
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]
    }

    func update(for phase: AppModel.Phase) {
        hideWorkItem?.cancel()
        hideWorkItem = nil

        switch phase {
        case .recording:
            show(.recording)
        case .processing:
            show(.processing)
        case .success:
            show(.success)
            hideAfterFeedback()
        case .failure:
            show(.failure)
            hideAfterFeedback()
        case .idle, .rawTextAvailable:
            panel.orderOut(nil)
        }
    }

    private func show(_ state: RecordingOverlayView.State) {
        overlayView.state = state
        positionPanel()
        panel.orderFrontRegardless()
    }

    private func hideAfterFeedback() {
        let workItem = DispatchWorkItem { [weak self] in
            self?.panel.orderOut(nil)
        }
        hideWorkItem = workItem
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.2, execute: workItem)
    }

    private func positionPanel() {
        let mouseLocation = NSEvent.mouseLocation
        let screen = NSScreen.screens.first { $0.frame.contains(mouseLocation) } ?? NSScreen.main
        guard let visibleFrame = screen?.visibleFrame else { return }
        let origin = NSPoint(
            x: visibleFrame.midX - panel.frame.width / 2,
            y: visibleFrame.minY + 72
        )
        panel.setFrameOrigin(origin)
    }
}

private final class RecordingOverlayView: NSView {
    enum State {
        case recording
        case processing
        case success
        case failure
    }

    var state: State = .recording {
        didSet { needsDisplay = true }
    }
    var onCancel: (() -> Void)?
    var onStop: (() -> Void)?

    private let sideSize: CGFloat = 42
    private let sideInset: CGFloat = 12

    override var acceptsFirstResponder: Bool { false }

    override func draw(_ dirtyRect: NSRect) {
        super.draw(dirtyRect)

        let capsule = bounds.insetBy(dx: 1, dy: 1)
        NSColor(calibratedWhite: 0.06, alpha: 0.96).setFill()
        NSBezierPath(roundedRect: capsule, xRadius: capsule.height / 2, yRadius: capsule.height / 2).fill()
        NSColor(calibratedWhite: 0.5, alpha: 0.6).setStroke()
        let border = NSBezierPath(roundedRect: capsule, xRadius: capsule.height / 2, yRadius: capsule.height / 2)
        border.lineWidth = 1
        border.stroke()

        switch state {
        case .recording:
            drawCancelButton()
            drawWaveform()
            drawStopButton()
        case .processing:
            drawProcessing()
        case .success:
            drawFeedback(symbol: "✓", color: .systemGreen)
        case .failure:
            drawFeedback(symbol: "!", color: .systemOrange)
        }
    }

    override func mouseDown(with event: NSEvent) {
        guard state == .recording else { return }
        let location = convert(event.locationInWindow, from: nil)
        if cancelRect.contains(location) {
            onCancel?()
        } else if stopRect.contains(location) {
            onStop?()
        }
    }

    private var cancelRect: NSRect {
        NSRect(x: sideInset, y: (bounds.height - sideSize) / 2, width: sideSize, height: sideSize)
    }

    private var stopRect: NSRect {
        NSRect(x: bounds.width - sideInset - sideSize, y: (bounds.height - sideSize) / 2, width: sideSize, height: sideSize)
    }

    private func drawCancelButton() {
        NSColor(calibratedWhite: 0.28, alpha: 1).setFill()
        NSBezierPath(ovalIn: cancelRect).fill()
        NSColor.white.setStroke()
        let path = NSBezierPath()
        path.lineWidth = 2.5
        path.lineCapStyle = .round
        path.move(to: NSPoint(x: cancelRect.minX + 14, y: cancelRect.minY + 14))
        path.line(to: NSPoint(x: cancelRect.maxX - 14, y: cancelRect.maxY - 14))
        path.move(to: NSPoint(x: cancelRect.maxX - 14, y: cancelRect.minY + 14))
        path.line(to: NSPoint(x: cancelRect.minX + 14, y: cancelRect.maxY - 14))
        path.stroke()
    }

    private func drawStopButton() {
        NSColor.white.setFill()
        NSBezierPath(ovalIn: stopRect).fill()
        NSColor(calibratedWhite: 0.08, alpha: 1).setStroke()
        let path = NSBezierPath()
        path.lineWidth = 2.7
        path.lineCapStyle = .round
        path.lineJoinStyle = .round
        path.move(to: NSPoint(x: stopRect.minX + 12, y: stopRect.midY))
        path.line(to: NSPoint(x: stopRect.midX - 2, y: stopRect.minY + 13))
        path.line(to: NSPoint(x: stopRect.maxX - 11, y: stopRect.maxY - 12))
        path.stroke()
    }

    private func drawWaveform() {
        let heights: [CGFloat] = [13, 22, 34, 24, 43, 32, 19, 28, 15]
        let lineSpacing: CGFloat = 8
        let totalWidth = CGFloat(heights.count - 1) * lineSpacing
        let startX = bounds.midX - totalWidth / 2
        NSColor.white.setStroke()

        for (index, height) in heights.enumerated() {
            let path = NSBezierPath()
            path.lineWidth = 3.2
            path.lineCapStyle = .round
            let x = startX + CGFloat(index) * lineSpacing
            path.move(to: NSPoint(x: x, y: bounds.midY - height / 2))
            path.line(to: NSPoint(x: x, y: bounds.midY + height / 2))
            path.stroke()
        }
    }

    private func drawProcessing() {
        let radius: CGFloat = 5
        let spacing: CGFloat = 18
        for index in 0..<3 {
            let x = bounds.midX + CGFloat(index - 1) * spacing
            let alpha: CGFloat = index == 1 ? 1 : 0.45
            NSColor.white.withAlphaComponent(alpha).setFill()
            NSBezierPath(ovalIn: NSRect(x: x - radius, y: bounds.midY - radius, width: radius * 2, height: radius * 2)).fill()
        }
    }

    private func drawFeedback(symbol: String, color: NSColor) {
        let circle = NSRect(x: bounds.midX - 21, y: bounds.midY - 21, width: 42, height: 42)
        color.setFill()
        NSBezierPath(ovalIn: circle).fill()
        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 28, weight: .bold),
            .foregroundColor: NSColor.white,
        ]
        let size = (symbol as NSString).size(withAttributes: attributes)
        (symbol as NSString).draw(
            at: NSPoint(x: bounds.midX - size.width / 2, y: bounds.midY - size.height / 2 - 2),
            withAttributes: attributes
        )
    }
}
