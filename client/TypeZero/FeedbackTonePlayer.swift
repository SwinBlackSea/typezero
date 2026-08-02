import AppKit
import Foundation

@MainActor
final class FeedbackTonePlayer {
    private let startSound = FeedbackTonePlayer.makeSound(frequencies: [523.25, 587.33])
    private let stopSound = FeedbackTonePlayer.makeSound(frequencies: [523.25, 698.46])

    func playStart() {
        play(startSound)
    }

    func playStop() {
        play(stopSound)
    }

    private func play(_ sound: NSSound?) {
        sound?.stop()
        sound?.play()
    }

    private static func makeSound(frequencies: [Double]) -> NSSound? {
        let sampleRate = 44_100
        let toneDuration = 0.075
        let pauseDuration = 0.03
        let toneSamples = Int(Double(sampleRate) * toneDuration)
        let pauseSamples = Int(Double(sampleRate) * pauseDuration)
        var samples = [Int16]()
        samples.reserveCapacity(frequencies.count * (toneSamples + pauseSamples))

        for (toneIndex, frequency) in frequencies.enumerated() {
            for sampleIndex in 0..<toneSamples {
                let progress = Double(sampleIndex) / Double(toneSamples)
                let fade = min(1, min(progress / 0.12, (1 - progress) / 0.12))
                let radians = 2 * Double.pi * frequency * Double(sampleIndex) / Double(sampleRate)
                let amplitude = sin(radians) * fade * 0.2 * Double(Int16.max)
                samples.append(Int16(amplitude))
            }
            if toneIndex < frequencies.count - 1 {
                samples.append(contentsOf: repeatElement(Int16(0), count: pauseSamples))
            }
        }

        var data = Data()
        let dataByteCount = UInt32(samples.count * MemoryLayout<Int16>.size)
        data.appendASCII("RIFF")
        data.appendUInt32(36 + dataByteCount)
        data.appendASCII("WAVEfmt ")
        data.appendUInt32(16)
        data.appendUInt16(1)
        data.appendUInt16(1)
        data.appendUInt32(UInt32(sampleRate))
        data.appendUInt32(UInt32(sampleRate * MemoryLayout<Int16>.size))
        data.appendUInt16(UInt16(MemoryLayout<Int16>.size))
        data.appendUInt16(16)
        data.appendASCII("data")
        data.appendUInt32(dataByteCount)
        for sample in samples {
            data.appendUInt16(UInt16(bitPattern: sample))
        }

        let sound = NSSound(data: data)
        sound?.volume = 0.45
        return sound
    }
}

private extension Data {
    mutating func appendASCII(_ value: String) {
        append(value.data(using: .ascii)!)
    }

    mutating func appendUInt16(_ value: UInt16) {
        var littleEndian = value.littleEndian
        Swift.withUnsafeBytes(of: &littleEndian) { append(contentsOf: $0) }
    }

    mutating func appendUInt32(_ value: UInt32) {
        var littleEndian = value.littleEndian
        Swift.withUnsafeBytes(of: &littleEndian) { append(contentsOf: $0) }
    }
}
