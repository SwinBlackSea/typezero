import Foundation

/// AudioChunker splits a 16 kHz, mono, 16-bit PCM WAV recording into
/// overlapping chunks suitable for the server's chunked dictation endpoint.
///
/// Layout:
///   - header: canonical 44-byte PCM WAV header (RIFF/WAVE/fmt/data)
///   - step:   8.0 s  (distance between chunk start times)
///   - window: 9.5 s  (each chunk's audio length)
///   - overlap: 1.5 s (adjacent chunks share this much audio)
///   - final chunk may be shorter than `window` when totalDuration is not a
///     multiple of `step`.
///
/// The chunked transcript pipeline relies on this overlap so the server can
/// hand both halves of an overlap region to the LLM and ask it to merge.
struct AudioChunker {
    static let sampleRate: Int = 16_000
    static let bytesPerSample: Int = 2
    static let channels: Int = 1
    static let headerSize: Int = 44
    static let bytesPerSecond: Int = sampleRate * bytesPerSample * channels

    /// Step (seconds) between consecutive chunk starts. The chunk window is
    /// step + overlap so each pair of neighbours shares `overlap` seconds.
    static let stepSeconds: Double = 8.0
    /// Overlap (seconds) shared between adjacent chunks.
    static let overlapSeconds: Double = 1.5
    /// Per-chunk audio length (seconds) sent to ASR.
    static var windowSeconds: Double { stepSeconds + overlapSeconds }

    /// A chunk of the original recording.
    struct Chunk {
        let data: Data                  // full WAV file (header + PCM slice)
        let chunkIndex: Int
        let chunkStartMilliseconds: Int  // start position in original audio
        let chunkEndMilliseconds: Int    // end position in original audio
    }

    /// Split a full recording into chunks. `wavData` is expected to be a WAV
    /// file produced by AVAudioRecorder (16 kHz mono 16-bit PCM). Total
    /// duration is read from the WAV header; callers may override via
    /// `declaredDurationMs` when the header is unreliable.
    static func chunk(
        wavData: Data,
        declaredDurationMs: Int? = nil
    ) -> [Chunk] {
        let pcmLength = max(0, wavData.count - headerSize)
        guard pcmLength > 0 else { return [] }

        let totalDurationMs = headerDurationMs(wavBytes: pcmLength)
            ?? declaredDurationMs
            ?? 0
        guard totalDurationMs > 0 else { return [] }

        let stepMs = Int((stepSeconds * 1000).rounded())
        let windowMs = Int((windowSeconds * 1000).rounded())
        let totalMs = totalDurationMs

        var chunks: [Chunk] = []
        var startMs = 0
        var index = 0
        while startMs < totalMs {
            let endMs = min(startMs + windowMs, totalMs)
            let startByte = headerSize + bytes(for: startMs)
            let endByte = headerSize + bytes(for: endMs)
            guard endByte > startByte, endByte <= wavData.count else { break }

            // Build the chunk as a flat [UInt8] buffer so we never depend on
            // the lifecycle of Data's internal storage. The header bytes are
            // copied verbatim from the source recording; the PCM slice is
            // copied from the same range; then the RIFF/data sub-chunk sizes
            // are rewritten in little-endian so the server's parser sees a
            // self-describing WAV. Using explicit little-endian writes keeps
            // the output stable across architectures.
            let pcmCount = endByte - startByte
            var bytes = [UInt8]()
            bytes.reserveCapacity(headerSize + pcmCount)
            bytes.append(contentsOf: wavData.subdata(in: 0..<headerSize))
            bytes.append(contentsOf: wavData.subdata(in: startByte..<endByte))

            let pcmSize = UInt32(pcmCount)
            let riffSize = UInt32(headerSize + pcmCount - 8)
            bytes[4] = UInt8(truncatingIfNeeded: riffSize)
            bytes[5] = UInt8(truncatingIfNeeded: riffSize >> 8)
            bytes[6] = UInt8(truncatingIfNeeded: riffSize >> 16)
            bytes[7] = UInt8(truncatingIfNeeded: riffSize >> 24)
            bytes[40] = UInt8(truncatingIfNeeded: pcmSize)
            bytes[41] = UInt8(truncatingIfNeeded: pcmSize >> 8)
            bytes[42] = UInt8(truncatingIfNeeded: pcmSize >> 16)
            bytes[43] = UInt8(truncatingIfNeeded: pcmSize >> 24)

            chunks.append(Chunk(
                data: Data(bytes),
                chunkIndex: index,
                chunkStartMilliseconds: startMs,
                chunkEndMilliseconds: endMs
            ))
            index += 1
            startMs += stepMs
            if endMs >= totalMs {
                break
            }
        }
        return chunks
    }

    /// Convert a millisecond offset into a byte offset within the PCM payload.
    private static func bytes(for milliseconds: Int) -> Int {
        let samples = max(0, milliseconds) * sampleRate / 1000
        return samples * bytesPerSample * channels
    }

    /// Try to derive the recording duration from the WAV data chunk size.
    /// Returns nil if the chunk size is missing or zero.
    private static func headerDurationMs(wavBytes: Int) -> Int? {
        guard wavBytes > 0 else { return nil }
        let seconds = Double(wavBytes) / Double(bytesPerSecond)
        return Int((seconds * 1000).rounded())
    }
}