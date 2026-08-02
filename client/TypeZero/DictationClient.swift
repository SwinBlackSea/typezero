import Foundation

struct DictationResponse: Decodable, Sendable {
    struct Warning: Decodable, Sendable {
        let code: String
        let message: String
    }

    let requestID: String
    let rawText: String
    let finalText: String
    let warning: Warning?

    enum CodingKeys: String, CodingKey {
        case requestID = "request_id"
        case rawText = "raw_text"
        case finalText = "final_text"
        case warning
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

    var errorDescription: String? {
        switch self {
        case .configuration(let message), .server(let message): return message
        case .invalidRecording: return "录音文件无效或超过 10 MB"
        case .invalidResponse: return "服务返回了无法解析的结果"
        }
    }
}

struct DictationClient: Sendable {
    let endpoint: URL
    let dashscopeAPIKey: String
    let deepSeekAPIKey: String

    func upload(recording: Recording) async throws -> DictationResponse {
        let audio = try Data(contentsOf: recording.url, options: .mappedIfSafe)
        guard !audio.isEmpty, audio.count <= 10 << 20, recording.durationMilliseconds <= 5 * 60 * 1000 else {
            throw ClientError.invalidRecording
        }

        let boundary = "TypeZero-\(UUID().uuidString)"
        var body = Data()
        body.appendField(name: "duration_ms", value: String(recording.durationMilliseconds), boundary: boundary)
        body.appendField(name: "output_mode", value: "polished", boundary: boundary)
        body.appendFile(name: "audio", filename: "recording.m4a", contentType: "audio/mp4", data: audio, boundary: boundary)
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

        let (data, response) = try await URLSession.shared.data(for: request)
        guard let httpResponse = response as? HTTPURLResponse else {
            throw ClientError.invalidResponse
        }
        guard (200..<300).contains(httpResponse.statusCode) else {
            if let envelope = try? JSONDecoder().decode(ErrorEnvelope.self, from: data) {
                throw ClientError.server(envelope.error.message)
            }
            throw ClientError.server("服务请求失败（\(httpResponse.statusCode)）")
        }
        guard let decoded = try? JSONDecoder().decode(DictationResponse.self, from: data) else {
            throw ClientError.invalidResponse
        }
        return decoded
    }
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
