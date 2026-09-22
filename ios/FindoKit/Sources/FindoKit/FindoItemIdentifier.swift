import Foundation

/// Encodes/decodes File Provider item identifiers as `(volumeID, path)`
/// pairs, so the extension never has to keep its own identifier<->path
/// database — the backend's index is the only source of truth.
///
/// A volume's root folder is encoded as `path == ""`; its parent is always
/// `NSFileProviderItemIdentifier.rootContainer`, since that's the one case
/// with no volumeID to encode.
public enum FindoItemIdentifier {
    private static let prefix = "v-"

    public static func encode(volumeID: Int64, path: String) -> String {
        let raw = "\(volumeID)\t\(path)"
        let base64 = Data(raw.utf8).base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
        return prefix + base64
    }

    public static func decode(_ identifier: String) -> (volumeID: Int64, path: String)? {
        guard identifier.hasPrefix(prefix) else { return nil }
        var base64 = String(identifier.dropFirst(prefix.count))
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        while base64.count % 4 != 0 {
            base64 += "="
        }
        guard let data = Data(base64Encoded: base64),
              let raw = String(data: data, encoding: .utf8)
        else { return nil }

        let parts = raw.split(separator: "\t", maxSplits: 1, omittingEmptySubsequences: false)
        guard parts.count == 2, let volumeID = Int64(parts[0]) else { return nil }
        return (volumeID, String(parts[1]))
    }

    /// The identifier of `path`'s containing folder: another file/dir
    /// identifier within the same volume if `dir` is non-empty, otherwise
    /// the volume's own root-folder identifier (`path == ""`).
    public static func parentIdentifier(volumeID: Int64, dir: String) -> String {
        encode(volumeID: volumeID, path: dir)
    }
}
