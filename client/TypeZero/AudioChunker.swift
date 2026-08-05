import Foundation

/// AudioChunker splits a 16 kHz, mono, 16-bit PCM WAV recording into
/// overlapping chunks suitable for the server's chunked dictation endpoint.
///
/// Layout:
///   - step:   30.0 s (distance between chunk start times)
///   - window: 32.0 s (each chunk's audio length)
///   - overlap: 2.0 s (adjacent chunks share this much audio)
///   - final chunk may be shorter than `window` when totalDuration is not a
///     multiple of `step`.
///
/// The chunked transcript pipeline relies on this overlap so the server can
/// hand both halves of an overlap region to the LLM and ask it to merge.
///
/// The step is sized to keep a full 5-minute recording at about 10 chunks:
/// fewer requests means less per-request overhead and less provider-side
/// queueing, while ~7% duplicated audio keeps the merge prompt reliable.
///
/// Recordings from AVAudioRecorder occasionally include auxiliary RIFF
/// chunks (JUNK, FLLR, LIST) ahead of `data`, so we walk the source file's
/// chunks to locate the real PCM payload and rebuild a canonical 44-byte
/// header before slicing.
struct AudioChunker {
    static let sampleRate: Int = 16_000
    static let bytesPerSample: Int = 2
    static let channels: Int = 1
    static let headerSize: Int = 44
    static let bytesPerSecond: Int = sampleRate * bytesPerSample * channels

    /// Step (seconds) between consecutive chunk starts. The chunk window is
    /// step + overlap so each pair of neighbours shares `overlap` seconds.
    static let stepSeconds: Double = 30.0
    /// Overlap (seconds) shared between adjacent chunks.
    static let overlapSeconds: Double = 2.0
    /// Per-chunk audio length (seconds) sent to ASR.
    static var windowSeconds: Double { stepSeconds + overlapSeconds }
    /// Per-chunk audio length in milliseconds (one full window).
    static var windowMilliseconds: Int { Int((windowSeconds * 1000).rounded()) }

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
        guard let pcmRegion = findPCMRegion(in: wavData) else { return [] }
        // The (clamped) payload byte count is the source of truth for the
        // duration; the recorder-declared duration only rescues files whose
        // data chunk header carries no usable size at all.
        let payloadDurationMs = headerDurationMs(wavBytes: pcmRegion.length) ?? 0
        let totalDurationMs = payloadDurationMs > 0 ? payloadDurationMs : (declaredDurationMs ?? 0)
        guard totalDurationMs > 0 else { return [] }

        let stepMs = Int((stepSeconds * 1000).rounded())
        let windowMs = Int((windowSeconds * 1000).rounded())
        let totalMs = totalDurationMs

        var chunks: [Chunk] = []
        var startMs = 0
        var index = 0
        while startMs < totalMs {
            let endMs = min(startMs + windowMs, totalMs)
            let pcmStart = pcmRegion.payloadOffset + bytes(for: startMs)
            let pcmEnd = pcmRegion.payloadOffset + bytes(for: endMs)
            guard pcmEnd > pcmStart, pcmEnd <= wavData.count else { break }

            // Build a self-describing canonical WAV: RIFF + WAVE + fmt + data
            // + PCM slice. RIFF/data sizes are written in explicit little-endian
            // so the output is stable across architectures and does not depend
            // on the source file's auxiliary chunk layout.
            let pcmCount = pcmEnd - pcmStart
            var header = [UInt8]()
            header.reserveCapacity(headerSize)
            header.append(contentsOf: [0x52, 0x49, 0x46, 0x46]) // "RIFF"
            appendU32LE(&header, UInt32(headerSize + pcmCount - 8)) // riff size
            header.append(contentsOf: [0x57, 0x41, 0x56, 0x45]) // "WAVE"
            header.append(contentsOf: [0x66, 0x6d, 0x74, 0x20]) // "fmt "
            appendU32LE(&header, 16)                            // fmt chunk size
            appendU16LE(&header, 1)                             // PCM
            appendU16LE(&header, UInt16(channels))              // channels
            appendU32LE(&header, UInt32(sampleRate))             // sample rate
            appendU32LE(&header, UInt32(bytesPerSecond))         // byte rate
            appendU16LE(&header, UInt16(channels * bytesPerSample)) // block align
            appendU16LE(&header, UInt16(bytesPerSample * 8))     // bits per sample
            header.append(contentsOf: [0x64, 0x61, 0x74, 0x61]) // "data"
            appendU32LE(&header, UInt32(pcmCount))               // data chunk size

            var bytes = header
            bytes.reserveCapacity(headerSize + pcmCount)
            bytes.append(contentsOf: wavData.subdata(in: pcmStart..<pcmEnd))

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

    // MARK: - Helpers

    private struct PCMRegion {
        let payloadOffset: Int  // byte offset of the first PCM sample
        let length: Int         // total PCM byte count
    }

    /// Walk RIFF chunks to find the `data` chunk. Returns nil if the file
    /// isn't a RIFF/WAVE stream or has no `data` chunk.
    private static func findPCMRegion(in wavData: Data) -> PCMRegion? {
        guard wavData.count >= 12,
              asciiEqual(wavData, 0, "RIFF"),
              asciiEqual(wavData, 8, "WAVE") else {
            return nil
        }

        var fmtBytes: Data?
        var dataOffset: Int?
        var dataSize: Int?

        var offset = 12
        while offset + 8 <= wavData.count {
            let tag = ascii(wavData, at: offset, length: 4)
            let size = readU32LE(wavData, at: offset + 4)
            let bodyStart = offset + 8
            let paddedSize = Int(size) + (Int(size) & 1)
            switch tag {
            case "fmt ":
                if fmtBytes == nil, bodyStart + Int(size) <= wavData.count {
                    fmtBytes = wavData.subdata(in: bodyStart..<(bodyStart + Int(size)))
                }
            case "data":
                dataOffset = bodyStart
                // Clamp the declared size to the bytes actually present in
                // the file. AVAudioRecorder finalizes header sizes on stop,
                // but a stale or oversized data size would push every slice
                // past EOF and drop all chunks; a zero size (header never
                // finalized) falls back to the available payload instead.
                let availableBytes = wavData.count - bodyStart
                let declaredBytes = Int(size)
                dataSize = declaredBytes > 0 ? min(declaredBytes, availableBytes) : availableBytes
                break
            default:
                break
            }
            offset = bodyStart + paddedSize
        }

        guard let dataOffset, let dataSize else {
            // Sequential walk missed the data chunk. This happens when an
            // auxiliary chunk (JUNK/LIST/FLLR) carries a torn or stale size
            // while the file is still being written, which pushes the walker
            // past the real payload. The data tag always sits in the first
            // bytes of a RIFF/WAVE file, so scan the header region for it.
            return findDataChunkByScan(in: wavData)
        }
        // fmt is not strictly required for chunk construction (we hardcode
        // the 16 kHz mono 16-bit PCM params), but we keep walking to make
        // the parser robust against unexpected layouts.
        _ = fmtBytes
        return PCMRegion(payloadOffset: dataOffset, length: dataSize)
    }

    /// Recovers the PCM payload when the sequential chunk walk fails to find
    /// the `data` chunk. This happens when an auxiliary chunk (JUNK/LIST/
    /// FLLR) carries a torn or stale size while the file is still being
    /// written, so the walker skips past the real payload. The real data tag
    /// is the first occurrence of "data" in a RIFF/WAVE file (PCM bytes only
    /// follow it), so scanning the whole file and taking the first match is
    /// safe. A zero declared size (header not yet finalized) falls back to
    /// the bytes actually present in the file.
    private static func findDataChunkByScan(in wavData: Data) -> PCMRegion? {
        guard wavData.count > 12 else { return nil }
        var offset = 12
        while offset + 8 <= wavData.count {
            if asciiEqual(wavData, offset, "data") {
                let size = readU32LE(wavData, at: offset + 4)
                let bodyStart = offset + 8
                let availableBytes = wavData.count - bodyStart
                guard availableBytes > 0 else { break }
                let payload = size > 0 ? min(Int(size), availableBytes) : availableBytes
                if payload > 0 {
                    return PCMRegion(payloadOffset: bodyStart, length: payload)
                }
            }
            offset += 1
        }
        return nil
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

    // MARK: - Byte reading helpers

    private static func ascii(_ data: Data, at offset: Int, length: Int) -> String {
        let end = min(offset + length, data.count)
        guard offset >= 0, end <= data.count else { return "" }
        let bytes = [UInt8](data.subdata(in: offset..<end))
        return String(bytes: bytes, encoding: .ascii) ?? ""
    }

    private static func asciiEqual(_ data: Data, _ offset: Int, _ literal: String) -> Bool {
        ascii(data, at: offset, length: literal.utf8.count) == literal
    }

    private static func readU32LE(_ data: Data, at offset: Int) -> UInt32 {
        guard offset + 4 <= data.count else { return 0 }
        let bytes = data.subdata(in: offset..<(offset + 4))
        return bytes.withUnsafeBytes { raw -> UInt32 in
            guard let base = raw.baseAddress else { return 0 }
            return base.load(as: UInt32.self).littleEndian
        }
    }

    private static func appendU32LE(_ array: inout [UInt8], _ value: UInt32) {
        let le = value.littleEndian
        array.append(UInt8(truncatingIfNeeded: le))
        array.append(UInt8(truncatingIfNeeded: le >> 8))
        array.append(UInt8(truncatingIfNeeded: le >> 16))
        array.append(UInt8(truncatingIfNeeded: le >> 24))
    }

    private static func appendU16LE(_ array: inout [UInt8], _ value: UInt16) {
        let le = value.littleEndian
        array.append(UInt8(truncatingIfNeeded: le))
        array.append(UInt8(truncatingIfNeeded: le >> 8))
    }
}
