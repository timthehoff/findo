import Foundation

/// Mirrors `index.File` from the backend (`backend/internal/index/index.go`).
/// Field names match the backend's JSON tags exactly so `JSONDecoder`'s
/// default key strategy works with no `CodingKeys` translation.
public struct FileEntry: Decodable, Sendable {
    public let volumeId: Int64
    public let path: String
    public let name: String
    public let dir: String
    public let ext: String
    public let isDir: Bool
    public let size: Int64
    /// Unix seconds, per `smbclient.Entry.ModTime`.
    public let modTime: Int64
}

/// Mirrors the `volumeStatus` shape from `GET /volumes`
/// (`backend/internal/httpapi/volumes.go`). Only the fields the extension
/// and app need are decoded; the rest of `volumeStatus`'s crawl/watch
/// telemetry is left for a future dashboard-style screen.
public struct Volume: Decodable, Sendable, Identifiable {
    public let id: Int64
    public let name: String
    public let enabled: Bool
    public let connected: Bool
    public let fileCount: Int
    public let dirCount: Int
}

struct FilesResponse: Decodable {
    let path: String
    let entries: [FileEntry]
}

struct SearchResponse: Decodable {
    let query: String
    let entries: [FileEntry]
}

struct VolumesResponse: Decodable {
    let volumes: [Volume]
}

public struct HealthResponse: Decodable, Sendable {
    public let status: String
}
