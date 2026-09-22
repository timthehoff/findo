import FindoKit
import SwiftUI

/// The host app has no real UI job beyond this: it exists to carry the File
/// Provider Extension and to be the one place that can prompt for Local
/// Network access (extensions can't) and hold the App-Group-shared backend
/// URL. Everything else — browsing, opening, fetching files — happens
/// inside the Files app via the extension.
struct ContentView: View {
    @State private var backendURLText: String = BackendConfig.baseURL?.absoluteString ?? ""
    @State private var statusMessage: String?
    @State private var statusIsError = false
    @State private var isTesting = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Text("Findo fetches files from your NAS's Findo backend so the Files app, Quick Look, and (soon) Siri and Spotlight can find them.")
                        .foregroundStyle(.secondary)
                }

                Section("Backend") {
                    TextField("http://192.168.1.50:8080", text: $backendURLText)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()

                    Button("Save") {
                        save()
                    }
                    .disabled(URL(string: backendURLText) == nil)

                    Button {
                        Task { await testConnection() }
                    } label: {
                        if isTesting {
                            ProgressView()
                        } else {
                            Text("Test Connection")
                        }
                    }
                    .disabled(isTesting)

                    if let statusMessage {
                        Label(statusMessage, systemImage: statusIsError ? "xmark.circle" : "checkmark.circle")
                            .foregroundStyle(statusIsError ? .red : .green)
                    }
                }

                Section("Next step") {
                    Text("Open the Files app, tap Browse, then Edit at the top, and enable “Findo” under Locations. Your configured NAS volumes will appear as folders there.")
                        .foregroundStyle(.secondary)
                }
            }
            .navigationTitle("Findo")
        }
    }

    private func save() {
        guard let url = URL(string: backendURLText) else { return }
        BackendConfig.baseURL = url
        statusMessage = nil
    }

    private func testConnection() async {
        isTesting = true
        defer { isTesting = false }
        do {
            let client = try APIClient()
            let health = try await client.health()
            statusIsError = health.status != "ok"
            statusMessage = "Backend reports: \(health.status)"
        } catch {
            statusIsError = true
            statusMessage = "Couldn't reach backend: \(error.localizedDescription)"
        }
    }
}

#Preview {
    ContentView()
}
