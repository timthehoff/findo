import Foundation

public enum APIError: LocalizedError {
    case notConfigured
    case badResponse(Int)

    public var errorDescription: String? {
        switch self {
        case .notConfigured:
            return "No backend URL configured yet."
        case .badResponse(let status):
            return "Backend returned HTTP \(status)."
        }
    }
}

/// Thin wrapper over the backend's read-only HTTP API
/// (`backend/internal/httpapi/server.go`). No auth: the backend is assumed
/// to be reachable only on the home LAN, matching the rest of the project.
public struct APIClient: Sendable {
    private let baseURL: URL
    private let session: URLSession

    /// Throws `APIError.notConfigured` if no backend URL has been saved yet
    /// (`BackendConfig.baseURL`), so callers get a clear error instead of a
    /// confusing network failure.
    public init(session: URLSession = .shared) throws {
        guard let baseURL = BackendConfig.baseURL else { throw APIError.notConfigured }
        self.baseURL = baseURL
        self.session = session
    }

    public func health() async throws -> HealthResponse {
        try await get("/health", as: HealthResponse.self)
    }

    public func listVolumes() async throws -> [Volume] {
        try await get("/volumes", as: VolumesResponse.self).volumes
    }

    /// `path == ""` lists a volume's root directory.
    public func listFiles(volumeID: Int64, path: String) async throws -> [FileEntry] {
        var components = urlComponents(path: "/files")
        components.queryItems = [
            URLQueryItem(name: "volume", value: String(volumeID)),
            URLQueryItem(name: "path", value: path),
        ]
        return try await get(components, as: FilesResponse.self).entries
    }

    public func search(query: String, volumeID: Int64? = nil, limit: Int = 100) async throws -> [FileEntry] {
        var components = urlComponents(path: "/search")
        var items = [
            URLQueryItem(name: "q", value: query),
            URLQueryItem(name: "limit", value: String(limit)),
        ]
        if let volumeID {
            items.append(URLQueryItem(name: "volume", value: String(volumeID)))
        }
        components.queryItems = items
        return try await get(components, as: SearchResponse.self).entries
    }

    /// The Range-aware streaming URL for a file's content
    /// (`GET /files/content`, served via `http.ServeContent`). Callers
    /// download or stream directly from this URL rather than going through
    /// `APIClient` — e.g. `URLSession.downloadTask` in the File Provider
    /// Extension's `startProvidingItem`.
    public func contentURL(volumeID: Int64, path: String) -> URL {
        var components = urlComponents(path: "/files/content")
        components.queryItems = [
            URLQueryItem(name: "volume", value: String(volumeID)),
            URLQueryItem(name: "path", value: path),
        ]
        return components.url!
    }

    private func urlComponents(path: String) -> URLComponents {
        URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
    }

    private func get<T: Decodable>(_ path: String, as type: T.Type) async throws -> T {
        try await get(urlComponents(path: path), as: type)
    }

    private func get<T: Decodable>(_ components: URLComponents, as type: T.Type) async throws -> T {
        let (data, response) = try await session.data(from: components.url!)
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            let status = (response as? HTTPURLResponse)?.statusCode ?? -1
            throw APIError.badResponse(status)
        }
        return try JSONDecoder().decode(T.self, from: data)
    }
}
