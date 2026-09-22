import FileProvider
import FindoKit
import UniformTypeIdentifiers

/// One `NSFileProviderItem`, backed directly by a `FindoKit.FileEntry` (or,
/// for a volume's root folder, a `FindoKit.Volume`) — no local cache, since
/// the backend's index is cheap to re-query and MVP scope has no
/// save-back/offline story yet.
final class FileProviderItem: NSObject, NSFileProviderItem {
    let itemIdentifier: NSFileProviderItemIdentifier
    let parentItemIdentifier: NSFileProviderItemIdentifier
    let filename: String
    let isFolder: Bool
    let size: Int64
    let modTime: Date?

    init(itemIdentifier: NSFileProviderItemIdentifier, parentItemIdentifier: NSFileProviderItemIdentifier,
         filename: String, isFolder: Bool, size: Int64, modTime: Date?) {
        self.itemIdentifier = itemIdentifier
        self.parentItemIdentifier = parentItemIdentifier
        self.filename = filename
        self.isFolder = isFolder
        self.size = size
        self.modTime = modTime
    }

    /// The synthetic item representing the extension's top level, one
    /// folder per configured volume below it.
    static func root() -> FileProviderItem {
        FileProviderItem(itemIdentifier: .rootContainer, parentItemIdentifier: .rootContainer,
                          filename: "Findo", isFolder: true, size: 0, modTime: nil)
    }

    static func volumeRoot(_ volume: FindoKit.Volume) -> FileProviderItem {
        FileProviderItem(
            itemIdentifier: NSFileProviderItemIdentifier(FindoItemIdentifier.encode(volumeID: volume.id, path: "")),
            parentItemIdentifier: .rootContainer,
            filename: volume.name, isFolder: true, size: 0, modTime: nil
        )
    }

    static func file(_ entry: FileEntry) -> FileProviderItem {
        FileProviderItem(
            itemIdentifier: NSFileProviderItemIdentifier(FindoItemIdentifier.encode(volumeID: entry.volumeId, path: entry.path)),
            parentItemIdentifier: NSFileProviderItemIdentifier(FindoItemIdentifier.parentIdentifier(volumeID: entry.volumeId, dir: entry.dir)),
            filename: entry.name, isFolder: entry.isDir, size: entry.size,
            modTime: Date(timeIntervalSince1970: TimeInterval(entry.modTime))
        )
    }

    var typeIdentifier: String {
        if isFolder { return UTType.folder.identifier }
        let ext = (filename as NSString).pathExtension
        return UTType(filenameExtension: ext)?.identifier ?? UTType.data.identifier
    }

    var capabilities: NSFileProviderItemCapabilities {
        // MVP is read-only: no save-back API on the backend yet, so don't
        // advertise write capabilities the extension can't honor.
        isFolder ? [.allowsReading, .allowsContentEnumerating] : [.allowsReading]
    }

    var documentSize: NSNumber? {
        isFolder ? nil : NSNumber(value: size)
    }

    var contentModificationDate: Date? { modTime }
    var creationDate: Date? { modTime }
}
