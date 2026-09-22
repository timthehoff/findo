import FileProvider
import FindoKit

/// MVP File Provider Extension: browses configured volumes and opens files
/// by downloading them from the backend on demand. No save-back yet
/// (`itemChanged` is a no-op) and no offline caching — every listing is a
/// live call to the backend's read-only HTTP API.
///
/// This is the legacy (non-replicated) `NSFileProviderExtension`, not
/// `NSFileProviderReplicatedExtension`: it needs no local materialized
/// database, which keeps this first iOS milestone small. Revisit if a
/// future milestone needs offline access or finer-grained sync.
final class FileProviderExtension: NSFileProviderExtension {
    override init() {
        super.init()
    }

    override func item(for identifier: NSFileProviderItemIdentifier) throws -> NSFileProviderItem {
        if identifier == .rootContainer {
            return FileProviderItem.root()
        }
        guard let (volumeID, path) = FindoItemIdentifier.decode(identifier.rawValue) else {
            throw NSFileProviderError(.noSuchItem)
        }

        if path.isEmpty {
            return try runSync {
                let client = try APIClient()
                let volumes = try await client.listVolumes()
                guard let volume = volumes.first(where: { $0.id == volumeID }) else {
                    throw NSFileProviderError(.noSuchItem)
                }
                return FileProviderItem.volumeRoot(volume)
            }
        }

        return try runSync {
            let client = try APIClient()
            // Matches the backend's own `dir` field for a top-level entry:
            // no parent directory means "" (the volume root), not "/".
            let parentDir = (path as NSString).deletingLastPathComponent
            let entries = try await client.listFiles(volumeID: volumeID, path: parentDir)
            guard let entry = entries.first(where: { $0.path == path }) else {
                throw NSFileProviderError(.noSuchItem)
            }
            return FileProviderItem.file(entry)
        }
    }

    override func urlForItem(withPersistentIdentifier identifier: NSFileProviderItemIdentifier) -> URL? {
        guard let item = try? item(for: identifier) as? FileProviderItem else { return nil }
        return documentStorageURL
            .appendingPathComponent(identifier.rawValue, isDirectory: true)
            .appendingPathComponent(item.filename, isDirectory: false)
    }

    override func persistentIdentifierForItem(at url: URL) -> NSFileProviderItemIdentifier? {
        NSFileProviderItemIdentifier(url.deletingLastPathComponent().lastPathComponent)
    }

    override func providePlaceholder(at url: URL, completionHandler: @escaping (Error?) -> Void) {
        guard let identifier = persistentIdentifierForItem(at: url) else {
            completionHandler(NSFileProviderError(.noSuchItem))
            return
        }
        do {
            let fileProviderItem = try item(for: identifier)
            let placeholderURL = NSFileProviderManager.placeholderURL(for: url)
            try FileManager.default.createDirectory(
                at: placeholderURL.deletingLastPathComponent(), withIntermediateDirectories: true)
            try NSFileProviderManager.writePlaceholder(at: placeholderURL, withMetadata: fileProviderItem)
            completionHandler(nil)
        } catch {
            completionHandler(error)
        }
    }

    override func startProvidingItem(at url: URL, completionHandler: @escaping (Error?) -> Void) {
        guard let identifier = persistentIdentifierForItem(at: url),
              let (volumeID, path) = FindoItemIdentifier.decode(identifier.rawValue)
        else {
            completionHandler(NSFileProviderError(.noSuchItem))
            return
        }

        do {
            try FileManager.default.createDirectory(
                at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        } catch {
            completionHandler(error)
            return
        }

        guard let fileProviderItem = try? item(for: identifier) as? FileProviderItem, !fileProviderItem.isFolder else {
            // Folders have no content of their own; `enumerator(for:)`
            // handles listing what's inside them.
            do {
                try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
                completionHandler(nil)
            } catch {
                completionHandler(error)
            }
            return
        }

        guard let client = try? APIClient() else {
            completionHandler(APIError.notConfigured)
            return
        }

        let request = URLRequest(url: client.contentURL(volumeID: volumeID, path: path))
        let task = URLSession.shared.downloadTask(with: request) { tempURL, response, error in
            if let error {
                completionHandler(error)
                return
            }
            guard let tempURL,
                  let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode)
            else {
                let status = (response as? HTTPURLResponse)?.statusCode ?? -1
                completionHandler(APIError.badResponse(status))
                return
            }
            do {
                if FileManager.default.fileExists(atPath: url.path) {
                    try FileManager.default.removeItem(at: url)
                }
                try FileManager.default.moveItem(at: tempURL, to: url)
                completionHandler(nil)
            } catch {
                completionHandler(error)
            }
        }
        task.resume()
    }

    override func itemChanged(at url: URL) {
        // MVP is read-only: the backend has no write API yet, so there's
        // nothing to push back. `FileProviderItem.capabilities` already
        // withholds write capabilities to keep the system from offering
        // edits it can't persist; this only guards the same invariant.
    }

    override func stopProvidingItem(at url: URL) {
        try? FileManager.default.removeItem(at: url)
        providePlaceholder(at: url) { _ in }
    }

    override func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier) throws -> NSFileProviderEnumerator {
        if containerItemIdentifier == .workingSet {
            throw NSError(domain: NSCocoaErrorDomain, code: NSFeatureUnsupportedError)
        }
        return FileProviderEnumerator(enumeratedItemIdentifier: containerItemIdentifier)
    }
}

/// Bridges an async throwing operation into the synchronous throwing calls
/// `NSFileProviderExtension.item(for:)`/`urlForItem` require — those run
/// off the main thread, so blocking here is safe.
private func runSync<T>(_ operation: @escaping () async throws -> T) throws -> T {
    let semaphore = DispatchSemaphore(value: 0)
    var result: Result<T, Error>!
    Task {
        do {
            result = .success(try await operation())
        } catch {
            result = .failure(error)
        }
        semaphore.signal()
    }
    semaphore.wait()
    return try result.get()
}
