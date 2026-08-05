import Foundation

struct DictationResponse: Decodable, Sendable {
    struct Warning: Decodable, Sendable {
        let code: String
        let message: String
    }

    let requestID: String
    let sessionID: String?
    let chunkIndex: Int?
    let chunkCount: Int?
    let status: String?
    let rawText: String
    let finalText: String?
    let warning: Warning?

    enum CodingKeys: String, CodingKey {
        case requestID = "request_id"
        case sessionID = "session_id"
        case chunkIndex = "chunk_index"
        case chunkCount = "chunk_count"
        case status
        case rawText = "raw_text"
        case finalText = "final_text"
        case warning
    }
}

struct DictationUploadResult: Sendable {
    let response: DictationResponse
    let timing: ProcessingTiming
}

struct ProcessingTiming: Sendable {
    let preparationMilliseconds: Int
    let requestMilliseconds: Int
    let intakeMilliseconds: Int?
    let asrMilliseconds: Int?
    let polishMilliseconds: Int?
    let chunkAsrTotalMilliseconds: Int?
    let chunkAsrMaxMilliseconds: Int?
    let asrSpanMilliseconds: Int?
    let mergeDedupeMilliseconds: Int?
    let chunkCount: Int?
    let emptyChunkCount: Int?
    let isChunked: Bool

    var summary: String {
        var parts = ["请求 \(formattedSeconds(requestMilliseconds))"]
        parts.append("准备 \(formattedSeconds(preparationMilliseconds))")
        if let intakeMilliseconds {
            parts.append("接收 \(formattedSeconds(intakeMilliseconds))")
        }
        if let chunkCount, chunkCount > 1 {
            let totalText = chunkAsrTotalMilliseconds.map(formattedSeconds) ?? "—"
            let maxText = chunkAsrMaxMilliseconds.map(formattedSeconds) ?? "—"
            parts.append("识别\(chunkCount)段 累计 \(totalText)（最长 \(maxText)）")
        } else if let asrMilliseconds {
            parts.append("识别 \(formattedSeconds(asrMilliseconds))")
        }
        if let emptyChunkCount, emptyChunkCount > 0 {
            parts.append("空 \(emptyChunkCount) 段")
        }
        if let mergeDedupeMilliseconds {
            parts.append("去重合并 \(formattedSeconds(mergeDedupeMilliseconds))")
        } else if let polishMilliseconds {
            parts.append("润色 \(formattedSeconds(polishMilliseconds))")
        }
        return parts.joined(separator: " · ")
    }

    private func formattedSeconds(_ milliseconds: Int) -> String {
        String(format: "%.1f 秒", Double(milliseconds) / 1000)
    }
}

private struct ErrorEnvelope: Decodable {
    struct APIError: Decodable {
        let code: String
        let message: String
    }
    let error: APIError
}

enum ClientError: LocalizedError {
    case configuration(String)
    case invalidRecording(String)
    case server(String)
    case invalidResponse
    case chunkUploadFailed(String)

    var errorDescription: String? {
        switch self {
        case .configuration(let message), .server(let message), .invalidRecording(let message):
            return message
        case .invalidResponse: return "服务返回了无法解析的结果"
        case .chunkUploadFailed(let message): return "分段上传失败：\(message)"
        }
    }
}

struct DictationClient: Sendable {
    let endpoint: URL
    let dashscopeAPIKey: String
    let deepSeekAPIKey: String

    /// Chunk-aware upload. Splits the recording into overlapping segments and
    /// fires them in parallel; the final chunk's response carries the polished
    /// text and per-chunk timing breakdown.
    func uploadChunked(recording: Recording) async throws -> DictationUploadResult {
        // Recordings within one full 32s window produce a single chunk with
        // nothing to merge, so upload them as one shot. This also skips the
        // WAV chunk parser entirely: a JUNK-block header can otherwise make
        // the chunker report "无法切分音频" on an otherwise valid recording.
        if recording.durationMilliseconds < AudioChunker.windowMilliseconds {
            return try await upload(recording: recording)
        }
        let preparationStarted = Date()
        let (audio, chunks) = try await Self.loadAndChunkRecording(
            url: recording.url,
            durationMs: recording.durationMilliseconds,
            mustCover: []
        )
        guard !audio.isEmpty else {
            throw ClientError.invalidRecording("录音文件为空，请重新录音")
        }
        // The recorder auto-stops near 5 minutes / 10 MiB. Keep a margin
        // above that for WAV headers and writer padding so a legitimate
        // recording never trips this guard; anything larger is anomalous.
        guard audio.count <= 12 << 20 else {
            throw ClientError.invalidRecording("录音文件过大（\(Self.megabytes(audio.count)) MB），超过上传上限")
        }

        guard !chunks.isEmpty,
              Self.isUsableChunkSet(chunks, durationMs: recording.durationMilliseconds, mustCover: []) else {
            // Chunking could not produce a complete split after retries (the
            // stop-time read may have seen a truncated file). Fall back to a
            // whole-file upload so the audio still reaches the server and
            // gets transcribed there.
            return try await upload(recording: recording)
        }

        let sessionID = UUID().uuidString
        let preparationMilliseconds = elapsedMilliseconds(since: preparationStarted)
        let requestStarted = Date()

        let outcomes = try await uploadChunks(
            sessionID: sessionID,
            chunks: chunks
        )

        let requestMilliseconds = elapsedMilliseconds(since: requestStarted)
        let lastOutcome = outcomes.last!
        let response = lastOutcome.response
        // Diagnostic for silent uploads: non-final outcomes carry their own
        // chunk's transcript; an empty final raw_text means every chunk was
        // silent.
        var emptyChunks = 0
        for outcome in outcomes.dropLast() where outcome.response.rawText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            emptyChunks += 1
        }
        if response.rawText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            emptyChunks = chunks.count
        }
        let timing = ProcessingTiming(
            preparationMilliseconds: preparationMilliseconds,
            requestMilliseconds: requestMilliseconds,
            intakeMilliseconds: lastOutcome.timing.intakeMilliseconds,
            asrMilliseconds: chunks.count == 1 ? lastOutcome.timing.asrMilliseconds : nil,
            polishMilliseconds: chunks.count == 1 ? lastOutcome.timing.polishMilliseconds : nil,
            chunkAsrTotalMilliseconds: aggregateChunkAsrTotal(outcomes),
            chunkAsrMaxMilliseconds: aggregateChunkAsrMax(outcomes),
            asrSpanMilliseconds: lastOutcome.timing.asrSpanMilliseconds,
            mergeDedupeMilliseconds: lastOutcome.timing.mergeDedupeMilliseconds,
            chunkCount: chunks.count,
            emptyChunkCount: emptyChunks > 0 ? emptyChunks : nil,
            isChunked: chunks.count > 1
        )
        return DictationUploadResult(response: response, timing: timing)
    }

    /// Uploads one non-final chunk for an in-progress recording session.
    /// `chunkTotal` may be 0 while the final count is unknown; the server
    /// grows the session total and finalizes it when the last chunk arrives.
    /// Used by the incremental pipeline that transcribes completed chunks
    /// while recording continues.
    func uploadChunk(sessionID: String, chunk: AudioChunker.Chunk, chunkTotal: Int) async throws -> DictationUploadResult {
        let outcome = try await Self.uploadOne(
            endpoint: endpoint,
            sessionID: sessionID,
            chunk: chunk,
            chunkTotal: chunkTotal,
            isLast: false,
            dashscopeAPIKey: dashscopeAPIKey,
            deepSeekAPIKey: deepSeekAPIKey
        )
        if let errorMessage = outcome.errorMessage {
            throw ClientError.chunkUploadFailed(errorMessage)
        }
        return DictationUploadResult(
            response: outcome.response,
            timing: Self.processingTiming(
                for: outcome,
                chunkCount: chunkTotal > 0 ? chunkTotal : nil,
                isChunked: true,
                preparationMilliseconds: 0,
                requestMilliseconds: 0
            )
        )
    }

    /// Completes an incremental session after recording stops: chunks the
    /// full recording, uploads any non-final chunks the background flusher
    /// could not finish (or that failed and were retried), then sends the
    /// tail as the final chunk. The final response carries the merged and
    /// polished text.
    func finishChunkedUpload(
        recording: Recording,
        sessionID: String,
        alreadyUploaded: Set<Int>
    ) async throws -> DictationUploadResult {
        let preparationStarted = Date()
        let (audio, chunks) = try await Self.loadAndChunkRecording(
            url: recording.url,
            durationMs: recording.durationMilliseconds,
            mustCover: alreadyUploaded
        )
        guard !audio.isEmpty else {
            throw ClientError.invalidRecording("录音文件为空，请重新录音")
        }
        guard audio.count <= 12 << 20 else {
            throw ClientError.invalidRecording("录音文件过大（\(Self.megabytes(audio.count)) MB），超过上传上限")
        }
        guard let lastChunk = chunks.last,
              Self.isUsableChunkSet(chunks, durationMs: recording.durationMilliseconds, mustCover: alreadyUploaded) else {
            // The stop-time read never stabilized into a chunk set that
            // covers the segments already uploaded during recording and
            // spans the full declared duration. Fall back to a whole-file
            // single shot rather than silently finalizing with a truncated
            // set (which would drop the tail of the recording).
            return try await upload(recording: recording)
        }
        let preparationMilliseconds = elapsedMilliseconds(since: preparationStarted)
        let requestStarted = Date()

        // Upload missing non-final chunks in parallel. Chunks the background
        // flusher acknowledged keep their server-side transcripts; a chunk
        // that hard-failed earlier is simply uploaded again here.
        let missing = chunks.filter {
            !alreadyUploaded.contains($0.chunkIndex) && $0.chunkIndex != lastChunk.chunkIndex
        }
        if !missing.isEmpty {
            try await withThrowingTaskGroup(of: Void.self) { group in
                for chunk in missing {
                    let endpoint = endpoint
                    let dashscopeAPIKey = dashscopeAPIKey
                    let deepSeekAPIKey = deepSeekAPIKey
                    let sessionID = sessionID
                    group.addTask {
                        let outcome = try await Self.uploadOne(
                            endpoint: endpoint,
                            sessionID: sessionID,
                            chunk: chunk,
                            chunkTotal: 0,
                            isLast: false,
                            dashscopeAPIKey: dashscopeAPIKey,
                            deepSeekAPIKey: deepSeekAPIKey
                        )
                        if let errorMessage = outcome.errorMessage {
                            throw ClientError.chunkUploadFailed(errorMessage)
                        }
                    }
                }
                try await group.waitForAll()
            }
        }

        // The tail chunk declares the authoritative chunk count; its response
        // blocks until every chunk is stored, then merges and polishes them.
        let finalOutcome = try await Self.uploadOne(
            endpoint: endpoint,
            sessionID: sessionID,
            chunk: lastChunk,
            chunkTotal: chunks.count,
            isLast: true,
            dashscopeAPIKey: dashscopeAPIKey,
            deepSeekAPIKey: deepSeekAPIKey
        )
        if let errorMessage = finalOutcome.errorMessage {
            throw ClientError.chunkUploadFailed(errorMessage)
        }
        let requestMilliseconds = elapsedMilliseconds(since: requestStarted)
        return DictationUploadResult(
            response: finalOutcome.response,
            timing: Self.processingTiming(
                for: finalOutcome,
                chunkCount: chunks.count,
                isChunked: chunks.count > 1,
                preparationMilliseconds: preparationMilliseconds,
                requestMilliseconds: requestMilliseconds
            )
        )
    }

    /// Legacy single-shot upload. Kept for any caller that still needs it
    /// (tests, future feature flags).
    func upload(recording: Recording) async throws -> DictationUploadResult {
        let preparationStarted = Date()
        let audio = try await Self.readRecordingData(
            url: recording.url,
            expectedBytes: Self.expectedWAVBytes(durationMs: recording.durationMilliseconds)
        )
        guard !audio.isEmpty else {
            throw ClientError.invalidRecording("录音文件为空，请重新录音")
        }
        guard audio.count <= 10 << 20 else {
            throw ClientError.invalidRecording("录音文件过大（\(Self.megabytes(audio.count)) MB），超过上传上限")
        }
        guard recording.durationMilliseconds <= 5 * 60 * 1000 else {
            throw ClientError.invalidRecording("录音时长超过 5 分钟上限")
        }

        let boundary = "TypeZero-\(UUID().uuidString)"
        var body = Data()
        body.appendField(name: "duration_ms", value: String(recording.durationMilliseconds), boundary: boundary)
        body.appendField(name: "output_mode", value: "polished", boundary: boundary)
        body.appendFile(name: "audio", filename: "recording.wav", contentType: "audio/wav", data: audio, boundary: boundary)
        body.append("--\(boundary)--\r\n")

        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"
        request.timeoutInterval = 150
        request.httpBody = body
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if !dashscopeAPIKey.isEmpty {
            request.setValue(dashscopeAPIKey, forHTTPHeaderField: "X-TypeZero-DashScope-Key")
        }
        if !deepSeekAPIKey.isEmpty {
            request.setValue(deepSeekAPIKey, forHTTPHeaderField: "X-TypeZero-DeepSeek-Key")
        }

        let preparationMilliseconds = elapsedMilliseconds(since: preparationStarted)
        let requestStarted = Date()
        let (data, response) = try await URLSession.shared.data(for: request)
        let requestMilliseconds = elapsedMilliseconds(since: requestStarted)
        guard let httpResponse = response as? HTTPURLResponse else {
            throw ClientError.invalidResponse
        }
        let timing = ServerTiming.parse(httpResponse.value(forHTTPHeaderField: "Server-Timing"))
        let processing = ProcessingTiming(
            preparationMilliseconds: preparationMilliseconds,
            requestMilliseconds: requestMilliseconds,
            intakeMilliseconds: timing.intakeMilliseconds,
            asrMilliseconds: timing.asrMilliseconds,
            polishMilliseconds: timing.polishMilliseconds,
            chunkAsrTotalMilliseconds: nil,
            chunkAsrMaxMilliseconds: nil,
            asrSpanMilliseconds: nil,
            mergeDedupeMilliseconds: nil,
            chunkCount: nil,
            emptyChunkCount: nil,
            isChunked: false
        )
        try Self.validate(httpResponse: httpResponse, bodyData: data)
        guard let decoded = try? JSONDecoder().decode(DictationResponse.self, from: data) else {
            throw ClientError.invalidResponse
        }
        return DictationUploadResult(response: decoded, timing: processing)
    }

    // MARK: - Chunk upload pipeline

    private struct ChunkOutcome {
        let response: DictationResponse
        let timing: ServerTiming
        let errorMessage: String?
    }

    private func uploadChunks(
        sessionID: String,
        chunks: [AudioChunker.Chunk]
    ) async throws -> [ChunkOutcome] {
        var outcomes: [ChunkOutcome?] = Array(repeating: nil, count: chunks.count)
        try await withThrowingTaskGroup(of: (Int, ChunkOutcome).self) { group in
            for (index, chunk) in chunks.enumerated() {
                let isLast = index == chunks.count - 1
                group.addTask { [endpoint, dashscopeAPIKey, deepSeekAPIKey] in
                    let outcome = try await Self.uploadOne(
                        endpoint: endpoint,
                        sessionID: sessionID,
                        chunk: chunk,
                        chunkTotal: chunks.count,
                        isLast: isLast,
                        dashscopeAPIKey: dashscopeAPIKey,
                        deepSeekAPIKey: deepSeekAPIKey
                    )
                    return (index, outcome)
                }
            }
            for try await (index, outcome) in group {
                if let errorMessage = outcome.errorMessage {
                    throw ClientError.chunkUploadFailed(errorMessage)
                }
                outcomes[index] = outcome
            }
        }
        return outcomes.compactMap { $0 }
    }

    private static func uploadOne(
        endpoint: URL,
        sessionID: String,
        chunk: AudioChunker.Chunk,
        chunkTotal: Int,
        isLast: Bool,
        dashscopeAPIKey: String,
        deepSeekAPIKey: String
    ) async throws -> ChunkOutcome {
        let boundary = "TypeZero-\(UUID().uuidString)"
        var body = Data()
        // Declare this chunk's own duration, not the whole recording's:
        // the server validates against the parsed audio length and a full
        // recording that slightly exceeds 5 minutes would otherwise reject
        // every chunk.
        let chunkDurationMs = chunk.chunkEndMilliseconds - chunk.chunkStartMilliseconds
        body.appendField(name: "duration_ms", value: String(chunkDurationMs), boundary: boundary)
        body.appendField(name: "output_mode", value: "polished", boundary: boundary)
        body.appendField(name: "session_id", value: sessionID, boundary: boundary)
        body.appendField(name: "chunk_index", value: String(chunk.chunkIndex), boundary: boundary)
        body.appendField(name: "chunk_total", value: String(chunkTotal), boundary: boundary)
        if isLast {
            body.appendField(name: "is_last", value: "true", boundary: boundary)
        }
        body.appendFile(name: "audio", filename: "recording.wav", contentType: "audio/wav", data: chunk.data, boundary: boundary)
        body.append("--\(boundary)--\r\n")

        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"
        // Per-chunk ASR latency dominates, and a non-final chunk can also
        // sit behind the server's ASR semaphore before its transcription
        // starts. Keep every chunk at or above the server request timeout
        // (140s) so slow upstream ASR surfaces as a server-side result
        // instead of a client-side timeout.
        request.timeoutInterval = 150
        request.httpBody = body
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if !dashscopeAPIKey.isEmpty {
            request.setValue(dashscopeAPIKey, forHTTPHeaderField: "X-TypeZero-DashScope-Key")
        }
        if !deepSeekAPIKey.isEmpty {
            request.setValue(deepSeekAPIKey, forHTTPHeaderField: "X-TypeZero-DeepSeek-Key")
        }

        let (data, response) = try await URLSession.shared.data(for: request)
        guard let httpResponse = response as? HTTPURLResponse else {
            return ChunkOutcome(
                response: DictationResponse(
                    requestID: "", sessionID: nil, chunkIndex: nil,
                    chunkCount: nil, status: nil, rawText: "",
                    finalText: nil, warning: nil
                ),
                timing: ServerTiming(intakeMilliseconds: nil, asrMilliseconds: nil, polishMilliseconds: nil, mergeDedupeMilliseconds: nil, chunkAsrTotalMilliseconds: nil, chunkAsrMaxMilliseconds: nil, asrSpanMilliseconds: nil, chunkCount: nil),
                errorMessage: "服务响应无效"
            )
        }
        let timing = ServerTiming.parse(httpResponse.value(forHTTPHeaderField: "Server-Timing"))
        let statusOK = (200..<300).contains(httpResponse.statusCode)
        if !statusOK {
            let message: String
            if let envelope = try? JSONDecoder().decode(ErrorEnvelope.self, from: data) {
                message = envelope.error.message
            } else {
                message = "服务请求失败（\(httpResponse.statusCode)）"
            }
            return ChunkOutcome(
                response: DictationResponse(
                    requestID: "", sessionID: nil, chunkIndex: nil,
                    chunkCount: nil, status: nil, rawText: "",
                    finalText: nil, warning: nil
                ),
                timing: timing,
                errorMessage: "第\(chunk.chunkIndex + 1)段：\(message)"
            )
        }
        guard let decoded = try? JSONDecoder().decode(DictationResponse.self, from: data) else {
            return ChunkOutcome(
                response: DictationResponse(
                    requestID: "", sessionID: nil, chunkIndex: nil,
                    chunkCount: nil, status: nil, rawText: "",
                    finalText: nil, warning: nil
                ),
                timing: timing,
                errorMessage: "第\(chunk.chunkIndex + 1)段返回结果无法解析"
            )
        }
        return ChunkOutcome(response: decoded, timing: timing, errorMessage: nil)
    }

    private static func validate(httpResponse: HTTPURLResponse, bodyData: Data) throws {
        guard (200..<300).contains(httpResponse.statusCode) else {
            if let envelope = try? JSONDecoder().decode(ErrorEnvelope.self, from: bodyData) {
                throw ClientError.server(envelope.error.message)
            }
            throw ClientError.server("服务请求失败（\(httpResponse.statusCode)）")
        }
    }

    private func aggregateChunkAsrTotal(_ outcomes: [ChunkOutcome]) -> Int? {
        // The server reports a session-cumulative ASR duration on every
        // chunk response, so the final chunk's value already is the
        // authoritative total; summing would count earlier chunks over and
        // over. Fall back to the max observed value if the final response
        // somehow lacks the metric.
        if let cumulative = outcomes.last?.timing.chunkAsrTotalMilliseconds {
            return cumulative
        }
        let values = outcomes.compactMap { $0.timing.chunkAsrTotalMilliseconds }
        return values.max()
    }

    private func aggregateChunkAsrMax(_ outcomes: [ChunkOutcome]) -> Int? {
        let values = outcomes.compactMap { $0.timing.chunkAsrMaxMilliseconds }
        guard !values.isEmpty else { return nil }
        return values.max()
    }

    private static func processingTiming(
        for outcome: ChunkOutcome,
        chunkCount: Int?,
        isChunked: Bool,
        preparationMilliseconds: Int,
        requestMilliseconds: Int
    ) -> ProcessingTiming {
        let timing = outcome.timing
        let singleShot = (chunkCount ?? 1) <= 1
        return ProcessingTiming(
            preparationMilliseconds: preparationMilliseconds,
            requestMilliseconds: requestMilliseconds,
            intakeMilliseconds: timing.intakeMilliseconds,
            asrMilliseconds: singleShot ? timing.asrMilliseconds : nil,
            polishMilliseconds: singleShot ? timing.polishMilliseconds : nil,
            chunkAsrTotalMilliseconds: timing.chunkAsrTotalMilliseconds,
            chunkAsrMaxMilliseconds: timing.chunkAsrMaxMilliseconds,
            asrSpanMilliseconds: timing.asrSpanMilliseconds,
            mergeDedupeMilliseconds: timing.mergeDedupeMilliseconds,
            chunkCount: chunkCount,
            emptyChunkCount: nil,
            isChunked: isChunked
        )
    }

    private static func megabytes(_ byteCount: Int) -> String {
        String(format: "%.1f", Double(byteCount) / Double(1 << 20))
    }

    /// Reads the finished recording and chunks it. AVAudioRecorder.stop() can
    /// return while buffered samples are still being flushed to disk, so the
    /// file may briefly be shorter than the recorded duration or carry a torn
    /// header that makes AudioChunker return no chunks. Retry until the chunk
    /// set is usable: non-empty, covers every index the incremental flusher
    /// already uploaded, and spans close to the recorder-declared duration
    /// (a stale read of an in-progress file yields a truncated set). If it
    /// never settles, return the last read so the caller can fall back.
    private static func loadAndChunkRecording(
        url: URL,
        durationMs: Int,
        mustCover: Set<Int>
    ) async throws -> (Data, [AudioChunker.Chunk]) {
        let expectedBytes = Self.expectedWAVBytes(durationMs: durationMs)
        var data = try await Self.readRecordingData(url: url, expectedBytes: expectedBytes)
        var chunks = AudioChunker.chunk(wavData: data, declaredDurationMs: durationMs)
        if !isUsableChunkSet(chunks, durationMs: durationMs, mustCover: mustCover) {
            for _ in 0..<20 {
                try? await Task.sleep(nanoseconds: 100_000_000)
                data = try Data(contentsOf: url)
                chunks = AudioChunker.chunk(wavData: data, declaredDurationMs: durationMs)
                if isUsableChunkSet(chunks, durationMs: durationMs, mustCover: mustCover) {
                    break
                }
            }
        }
        return (data, chunks)
    }

    /// A chunk set is usable when it has at least one chunk, contains every
    /// index the incremental flusher already uploaded, and ends close to the
    /// recorder-declared duration. The last condition rejects a stale read
    /// whose parsed length is far shorter than what was actually recorded
    /// (the silent-tail-loss failure mode).
    private static func isUsableChunkSet(
        _ chunks: [AudioChunker.Chunk],
        durationMs: Int,
        mustCover: Set<Int>
    ) -> Bool {
        guard let lastChunk = chunks.last else { return false }
        let indexes = Set(chunks.map { $0.chunkIndex })
        guard mustCover.allSatisfy({ indexes.contains($0) }) else { return false }
        let minimumEndMs = max(0, durationMs - 500)
        return lastChunk.chunkEndMilliseconds >= minimumEndMs
    }

    /// AVAudioRecorder.stop() can return while buffered samples are still
    /// being flushed to disk, so the finished WAV may briefly be shorter than
    /// the recorded duration. Retry the read until the file reaches the
    /// expected size (or a short budget is exhausted) so a truncated file is
    /// not handed to the chunker or uploader.
    private static func readRecordingData(url: URL, expectedBytes: Int) async throws -> Data {
        var data = try Data(contentsOf: url)
        var attempt = 0
        while data.count < expectedBytes && attempt < 30 {
            try? await Task.sleep(nanoseconds: 100_000_000)
            data = try Data(contentsOf: url)
            attempt += 1
        }
        return data
    }

    /// Floor of the recording duration in bytes for the 16 kHz mono 16-bit
    /// PCM WAV the recorder produces (44-byte canonical header + 32000 B/s).
    /// Flooring keeps the expectation at or below the real payload size even
    /// when the recorder's reported duration rounds up.
    private static func expectedWAVBytes(durationMs: Int) -> Int {
        let seconds = durationMs / 1000
        return AudioChunker.headerSize + seconds * AudioChunker.bytesPerSecond
    }
}

private struct ServerTiming {
    let intakeMilliseconds: Int?
    let asrMilliseconds: Int?
    let polishMilliseconds: Int?
    let mergeDedupeMilliseconds: Int?
    let chunkAsrTotalMilliseconds: Int?
    let chunkAsrMaxMilliseconds: Int?
    let asrSpanMilliseconds: Int?
    let chunkCount: Int?

    static func parse(_ header: String?) -> ServerTiming {
        guard let header, !header.isEmpty else {
            return ServerTiming(
                intakeMilliseconds: nil,
                asrMilliseconds: nil,
                polishMilliseconds: nil,
                mergeDedupeMilliseconds: nil,
                chunkAsrTotalMilliseconds: nil,
                chunkAsrMaxMilliseconds: nil,
                asrSpanMilliseconds: nil,
                chunkCount: nil
            )
        }

        var values: [String: Int] = [:]
        var pairValues: [String: Double] = [:]
        for item in header.split(separator: ",") {
            let attributes = item.split(separator: ";")
            guard let rawName = attributes.first else { continue }
            let name = rawName.trimmingCharacters(in: .whitespacesAndNewlines)

            for rawAttribute in attributes.dropFirst() {
                let pair = rawAttribute.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
                guard pair.count == 2 else { continue }
                let key = pair[0].trimmingCharacters(in: .whitespacesAndNewlines)
                let rawValue = pair[1].trimmingCharacters(in: .whitespacesAndNewlines)
                if key == "dur", let duration = Double(rawValue), duration >= 0 {
                    values["\(name)|\(key)"] = Int(duration.rounded())
                    pairValues[name] = duration
                } else if key == "n" || key.hasSuffix("_n") {
                    if let count = Int(rawValue) {
                        pairValues[name] = Double(count)
                    }
                }
            }
        }

        let intake = values["intake|dur"]
        let asrSingle = values["asr|dur"]
        let polish = values["polish|dur"]
        let mergeDedupe = values["merge_dedupe|dur"]
        let chunkAsrTotal = values["asr_chunks|dur"]
        let chunkAsrMax = values["asr_chunks_max|dur"]
        let asrSpan = values["asr_span|dur"]
        let chunkCount: Int? = {
            if let n = pairValues["asr_chunks_n"] { return Int(n) }
            return nil
        }()
        return ServerTiming(
            intakeMilliseconds: intake,
            asrMilliseconds: chunkCount == nil ? asrSingle : nil,
            polishMilliseconds: chunkCount == nil ? polish : nil,
            mergeDedupeMilliseconds: mergeDedupe,
            chunkAsrTotalMilliseconds: chunkAsrTotal,
            chunkAsrMaxMilliseconds: chunkAsrMax,
            asrSpanMilliseconds: asrSpan,
            chunkCount: chunkCount
        )
    }
}

private func elapsedMilliseconds(since started: Date) -> Int {
    max(0, Int(Date().timeIntervalSince(started) * 1000))
}

private extension Data {
    mutating func append(_ string: String) {
        append(string.data(using: .utf8)!)
    }

    mutating func appendField(name: String, value: String, boundary: String) {
        append("--\(boundary)\r\n")
        append("Content-Disposition: form-data; name=\"\(name)\"\r\n\r\n")
        append("\(value)\r\n")
    }

    mutating func appendFile(name: String, filename: String, contentType: String, data: Data, boundary: String) {
        append("--\(boundary)\r\n")
        append("Content-Disposition: form-data; name=\"\(name)\"; filename=\"\(filename)\"\r\n")
        append("Content-Type: \(contentType)\r\n\r\n")
        append(data)
        append("\r\n")
    }
}
