import AppKit

final class RecordingOverlayController {
    private let panel: NSPanel
    private let overlayView: RecordingOverlayView
    private var hideWorkItem: DispatchWorkItem?

    init(onCancel: @escaping () -> Void, onStop: @escaping () -> Void) {
        overlayView = RecordingOverlayView(frame: NSRect(x: 0, y: 0, width: 202, height: 54))
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
            overlayView.stopAnimating()
            panel.orderOut(nil)
        }
    }

    func updateAudioLevel(_ level: CGFloat) {
        overlayView.audioLevel = level
    }

    private func show(_ state: RecordingOverlayView.State) {
        let size = overlayView.preferredSize(for: state)
        overlayView.frame = NSRect(origin: .zero, size: size)
        panel.setContentSize(size)
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
    enum State: Equatable {
        case recording
        case processing
        case success
        case failure
    }

    var state: State = .recording {
        didSet {
            if state != .recording {
                audioLevel = 0
            }
            startAnimatingIfNeeded()
            needsDisplay = true
        }
    }
    var audioLevel: CGFloat = 0 {
        didSet {
            let clamped = max(0, min(1, audioLevel))
            if clamped != audioLevel {
                audioLevel = clamped
                return
            }
            needsDisplay = true
        }
    }
    var onCancel: (() -> Void)?
    var onStop: (() -> Void)?

    private let sideSize: CGFloat = 32
    private let sideInset: CGFloat = 10
    private var animationTimer: Timer?
    private var animationPhase: CGFloat = 0

    deinit {
        animationTimer?.invalidate()
    }

    override var acceptsFirstResponder: Bool { false }

    func preferredSize(for state: State) -> NSSize {
        switch state {
        case .recording:
            return NSSize(width: 202, height: 54)
        case .processing:
            return NSSize(width: 154, height: 44)
        case .success, .failure:
            return NSSize(width: 64, height: 44)
        }
    }

    func stopAnimating() {
        animationTimer?.invalidate()
        animationTimer = nil
    }

    override func draw(_ dirtyRect: NSRect) {
        super.draw(dirtyRect)

        let capsule = bounds.insetBy(dx: 0.5, dy: 0.5)
        NSColor(calibratedWhite: 0.075, alpha: 0.97).setFill()
        NSBezierPath(roundedRect: capsule, xRadius: capsule.height / 2, yRadius: capsule.height / 2).fill()
        NSColor(calibratedWhite: 0.72, alpha: 0.22).setStroke()
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
        NSColor(calibratedWhite: 0.22, alpha: 1).setFill()
        NSBezierPath(ovalIn: cancelRect).fill()
        NSColor.white.setStroke()
        let path = NSBezierPath()
        path.lineWidth = 2
        path.lineCapStyle = .round
        path.move(to: NSPoint(x: cancelRect.minX + 10, y: cancelRect.minY + 10))
        path.line(to: NSPoint(x: cancelRect.maxX - 10, y: cancelRect.maxY - 10))
        path.move(to: NSPoint(x: cancelRect.maxX - 10, y: cancelRect.minY + 10))
        path.line(to: NSPoint(x: cancelRect.minX + 10, y: cancelRect.maxY - 10))
        path.stroke()
    }

    private func drawStopButton() {
        NSColor.white.setFill()
        NSBezierPath(ovalIn: stopRect).fill()
        NSColor(calibratedWhite: 0.08, alpha: 1).setStroke()
        let path = NSBezierPath()
        path.lineWidth = 2.2
        path.lineCapStyle = .round
        path.lineJoinStyle = .round
        path.move(to: NSPoint(x: stopRect.minX + 9, y: stopRect.midY))
        path.line(to: NSPoint(x: stopRect.midX - 1, y: stopRect.minY + 10))
        path.line(to: NSPoint(x: stopRect.maxX - 9, y: stopRect.maxY - 10))
        path.stroke()
    }

    private func drawWaveform() {
        let envelopes: [CGFloat] = [0.32, 0.5, 0.72, 0.94, 0.68, 0.86, 0.56, 0.38]
        let lineSpacing: CGFloat = 7
        let totalWidth = CGFloat(envelopes.count - 1) * lineSpacing
        let startX = bounds.midX - totalWidth / 2
        NSColor.white.withAlphaComponent(0.95).setStroke()

        for (index, envelope) in envelopes.enumerated() {
            let ripple = (sin(animationPhase * 1.9 + CGFloat(index) * 0.92) + 1) / 2
            let idleHeight: CGFloat = 6 + ripple * 3
            let voiceHeight = 10 + (audioLevel * 28 * envelope) + (ripple * 5)
            let height = max(idleHeight, voiceHeight)
            let path = NSBezierPath()
            path.lineWidth = 2.8
            path.lineCapStyle = .round
            let x = startX + CGFloat(index) * lineSpacing
            path.move(to: NSPoint(x: x, y: bounds.midY - height / 2))
            path.line(to: NSPoint(x: x, y: bounds.midY + height / 2))
            path.stroke()
        }
    }

    private func drawProcessing() {
        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 12, weight: .medium),
            .foregroundColor: NSColor.white.withAlphaComponent(0.88),
        ]
        let title = "正在整理" as NSString
        let titleSize = title.size(withAttributes: attributes)
        let titleX = bounds.midX - 8 - titleSize.width / 2
        title.draw(
            at: NSPoint(x: titleX, y: bounds.midY - titleSize.height / 2),
            withAttributes: attributes
        )

        for index in 0..<3 {
            let pulse = (sin(animationPhase * 2.2 - CGFloat(index) * 1.25) + 1) / 2
            let radius: CGFloat = 2.2 + pulse * 1.5
            let x = titleX + titleSize.width + 11 + CGFloat(index) * 8
            NSColor.systemBlue.withAlphaComponent(0.48 + pulse * 0.52).setFill()
            NSBezierPath(ovalIn: NSRect(x: x - radius, y: bounds.midY - radius, width: radius * 2, height: radius * 2)).fill()
        }
    }

    private func drawFeedback(symbol: String, color: NSColor) {
        let circle = NSRect(x: bounds.midX - 15, y: bounds.midY - 15, width: 30, height: 30)
        color.setFill()
        NSBezierPath(ovalIn: circle).fill()
        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 20, weight: .bold),
            .foregroundColor: NSColor.white,
        ]
        let size = (symbol as NSString).size(withAttributes: attributes)
        (symbol as NSString).draw(
            at: NSPoint(x: bounds.midX - size.width / 2, y: bounds.midY - size.height / 2 - 1),
            withAttributes: attributes
        )
    }

    private func startAnimatingIfNeeded() {
        guard state == .recording || state == .processing else {
            stopAnimating()
            return
        }
        guard animationTimer == nil else { return }
        let timer = Timer(timeInterval: 1.0 / 30.0, repeats: true) { [weak self] _ in
            guard let self else { return }
            self.animationPhase += 0.16
            if self.animationPhase > .pi * 2 {
                self.animationPhase -= .pi * 2
            }
            self.needsDisplay = true
        }
        animationTimer = timer
        RunLoop.main.add(timer, forMode: .common)
    }
}
