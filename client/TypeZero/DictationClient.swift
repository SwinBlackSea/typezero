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
    let mergeDedupeMilliseconds: Int?
    let chunkCount: Int?
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
            parts.append("识别\(chunkCount)段 \(totalText)（最长 \(maxText)）")
        } else if let asrMilliseconds {
            parts.append("识别 \(formattedSeconds(asrMilliseconds))")
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
    case invalidRecording
    case server(String)
    case invalidResponse
    case chunkUploadFailed(String)

    var errorDescription: String? {
        switch self {
        case .configuration(let message), .server(let message): return message
        case .invalidRecording: return "录音文件无效或超过 10 MB"
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
        let preparationStarted = Date()
        let audio = try Data(contentsOf: recording.url, options: .mappedIfSafe)
        guard !audio.isEmpty, audio.count <= 10 << 20 else {
            throw ClientError.invalidRecording
        }

        let chunks = AudioChunker.chunk(
            wavData: audio,
            declaredDurationMs: recording.durationMilliseconds
        )
        guard !chunks.isEmpty else {
            throw ClientError.invalidRecording
        }

        let sessionID = UUID().uuidString
        let preparationMilliseconds = elapsedMilliseconds(since: preparationStarted)
        let requestStarted = Date()

        let outcomes = try await uploadChunks(
            sessionID: sessionID,
            chunks: chunks,
            totalDurationMs: recording.durationMilliseconds
        )

        let requestMilliseconds = elapsedMilliseconds(since: requestStarted)
        let lastOutcome = outcomes.last!
        let response = lastOutcome.response
        let timing = ProcessingTiming(
            preparationMilliseconds: preparationMilliseconds,
            requestMilliseconds: requestMilliseconds,
            intakeMilliseconds: lastOutcome.timing.intakeMilliseconds,
            asrMilliseconds: chunks.count == 1 ? lastOutcome.timing.asrMilliseconds : nil,
            polishMilliseconds: chunks.count == 1 ? lastOutcome.timing.polishMilliseconds : nil,
            chunkAsrTotalMilliseconds: aggregateChunkAsrTotal(outcomes),
            chunkAsrMaxMilliseconds: aggregateChunkAsrMax(outcomes),
            mergeDedupeMilliseconds: lastOutcome.timing.mergeDedupeMilliseconds,
            chunkCount: chunks.count,
            isChunked: chunks.count > 1
        )
        return DictationUploadResult(response: response, timing: timing)
    }

    /// Legacy single-shot upload. Kept for any caller that still needs it
    /// (tests, future feature flags).
    func upload(recording: Recording) async throws -> DictationUploadResult {
        let preparationStarted = Date()
        let audio = try Data(contentsOf: recording.url, options: .mappedIfSafe)
        guard !audio.isEmpty, audio.count <= 10 << 20, recording.durationMilliseconds <= 5 * 60 * 1000 else {
            throw ClientError.invalidRecording
        }

        let boundary = "TypeZero-\(UUID().uuidString)"
        var body = Data()
        body.appendField(name: "duration_ms", value: String(recording.durationMilliseconds), boundary: boundary)
        body.appendField(name: "output_mode", value: "polished", boundary: boundary)
        body.appendFile(name: "audio", filename: "recording.wav", contentType: "audio/wav", data: audio, boundary: boundary)
        body.append("--\(boundary)--\r\n")

        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"
        request.timeoutInterval = 105
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
            mergeDedupeMilliseconds: nil,
            chunkCount: nil,
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
        chunks: [AudioChunker.Chunk],
        totalDurationMs: Int
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
                        totalDurationMs: totalDurationMs,
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
        totalDurationMs: Int,
        dashscopeAPIKey: String,
        deepSeekAPIKey: String
    ) async throws -> ChunkOutcome {
        let boundary = "TypeZero-\(UUID().uuidString)"
        var body = Data()
        body.appendField(name: "duration_ms", value: String(totalDurationMs), boundary: boundary)
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
        // Per-chunk ASR latency dominates; the final chunk also waits for
        // earlier ASR results + polish, so use the full request timeout.
        request.timeoutInterval = isLast ? 105 : 60
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
                timing: ServerTiming(intakeMilliseconds: nil, asrMilliseconds: nil, polishMilliseconds: nil, mergeDedupeMilliseconds: nil, chunkAsrTotalMilliseconds: nil, chunkAsrMaxMilliseconds: nil, chunkCount: nil),
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
        // Sum every chunk's reported asr duration. The server now keeps a
        // running total on the session and emits it on each response, so
        // summing is equivalent to reading the last entry but does not
        // break if the server falls back to a per-chunk metric.
        let values = outcomes.compactMap { $0.timing.chunkAsrTotalMilliseconds }
        guard !values.isEmpty else { return nil }
        return values.reduce(0, +)
    }

    private func aggregateChunkAsrMax(_ outcomes: [ChunkOutcome]) -> Int? {
        let values = outcomes.compactMap { $0.timing.chunkAsrMaxMilliseconds }
        guard !values.isEmpty else { return nil }
        return values.max()
    }
}

private struct ServerTiming {
    let intakeMilliseconds: Int?
    let asrMilliseconds: Int?
    let polishMilliseconds: Int?
    let mergeDedupeMilliseconds: Int?
    let chunkAsrTotalMilliseconds: Int?
    let chunkAsrMaxMilliseconds: Int?
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