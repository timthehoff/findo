import FileProvider
import FindoKit

/// One enumerator instance per container the Files app asks to list: either
/// the extension's root (which lists configured volumes as folders) or a
/// specific volume/subfolder (which lists that directory's entries via
/// `GET /files`). No local cache — every enumeration is a live backend
/// call, which is fine at MVP's request volume.
final class FileProviderEnumerator: NSObject, NSFileProviderEnumerator {
    private enum Scope {
        case root
        case folder(volumeID: Int64, path: String)
    }

    private let scope: Scope

    init(enumeratedItemIdentifier: NSFileProviderItemIdentifier) {
        if enumeratedItemIdentifier == .rootContainer {
            scope = .root
        } else if let (volumeID, path) = FindoItemIdentifier.decode(enumeratedItemIdentifier.rawValue) {
            scope = .folder(volumeID: volumeID, path: path)
        } else {
            scope = .root
        }
        super.init()
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        Task {
            do {
                let items = try await fetchItems()
                observer.didEnumerate(items)
                observer.finishEnumerating(upTo: nil)
            } catch {
                observer.finishEnumeratingWithError(error)
            }
        }
    }

    private func fetchItems() async throws -> [NSFileProviderItem] {
        let client = try APIClient()
        switch scope {
        case .root:
            let volumes = try await client.listVolumes()
            return volumes.filter(\.enabled).map(FileProviderItem.volumeRoot)
        case .folder(let volumeID, let path):
            let entries = try await client.listFiles(volumeID: volumeID, path: path)
            return entries.map(FileProviderItem.file)
        }
    }
}
