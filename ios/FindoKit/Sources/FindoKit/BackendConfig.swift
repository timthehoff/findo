import Foundation

/// Shared, host-app-and-extension-visible configuration, backed by an App
/// Group so the File Provider Extension (which has no UI of its own) can
/// read the backend URL the host app was configured with.
///
/// `appGroupID` must match the `com.apple.security.application-groups`
/// entry in both targets' entitlements (see `project.yml`) — it's read at
/// call time rather than hardcoded here so a misconfigured group shows up
/// as a clear "no backend configured" state instead of a silent crash.
public enum BackendConfig {
    public static let appGroupID = "group.com.example.findo"
    private static let baseURLKey = "findo.backendBaseURL"

    private static var defaults: UserDefaults? {
        UserDefaults(suiteName: appGroupID)
    }

    public static var baseURL: URL? {
        get {
            guard let raw = defaults?.string(forKey: baseURLKey) else { return nil }
            return URL(string: raw)
        }
        set {
            defaults?.set(newValue?.absoluteString, forKey: baseURLKey)
        }
    }
}
