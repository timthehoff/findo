# Findo iOS app

MVP milestone: a minimal File Provider Extension that browses the backend's
indexed volumes and opens a file (downloads it on demand, no save-back yet).
No App Intents/Spotlight indexing yet — that's the next milestone once
browse/open is proven out end to end. See `.claude/CLAUDE.md` at the repo
root for the full target architecture and current status.

## What's here

```
ios/
├── project.yml              # XcodeGen spec — source of truth, not the .xcodeproj
├── Findo/                   # host app: settings screen (backend URL) + carries the extension
├── FindoFileProvider/       # the File Provider Extension itself
└── FindoKit/                # shared framework: API client + models, used by both targets
```

`Findo.xcodeproj` is **generated**, not committed (see `.gitignore`) — this
avoids `.pbxproj` merge conflicts. `project.yml` is the thing to edit and
review in diffs; regenerate the `.xcodeproj` locally whenever it changes.

## One-time setup (on your Mac — none of this can be done from this session)

1. **Xcode**, latest stable, from the App Store or developer.apple.com.
2. **XcodeGen**: `brew install xcodegen`.
3. **Edit `ios/project.yml`** and replace the placeholders (all tied to your
   own Apple Developer account, so left as placeholders rather than
   guessed):
   - `com.timhoff.findo` → your own reverse-DNS bundle id prefix (e.g.
     `com.yourname.findo`)
   - `group.com.timhoff.findo` → an App Group id of your choosing, kept
     **identical** in all four places it appears: `Findo/Findo.entitlements`,
     `FindoFileProvider/FindoFileProvider.entitlements`,
     `FindoFileProvider/Info.plist`'s `NSExtensionFileProviderDocumentGroup`,
     and `FindoKit/Sources/FindoKit/BackendConfig.swift`'s `appGroupID`
4. **Generate the project**: `cd ios && xcodegen generate`, then open
   `Findo.xcodeproj`.
5. **Signing**: `project.yml` deliberately doesn't set a Team — Xcode's
   Accounts pane buries the raw Team ID differently across versions, so
   don't go hunting for it there. Instead, for each of the three targets
   (Findo, FindoFileProvider, FindoKit) in Signing & Capabilities, open the
   **Team** pop-up and pick your team by name (shown as "Your Name
   (Personal Team)") — Xcode fills in the ID for you. Do this for all
   three, including FindoKit (it's an embedded framework but still needs a
   team to build). If you want the choice to survive a future `xcodegen
   generate` instead of reselecting each time, look up the ID it picked
   with `grep -m1 DEVELOPMENT_TEAM Findo.xcodeproj/project.pbxproj` and add
   `DEVELOPMENT_TEAM: <that ID>` under `settings.base` in `project.yml`.

## Running it

- **Use a physical device, not the Simulator.** It needs to be on the same
  Wi-Fi as your Findo backend, and File Provider domains/Files-app
  integration are unreliable on Simulator historically. If you're on a
  free/personal team, trust the developer certificate once on-device
  (Settings → General → VPN & Device Management).
- Build & run the **Findo** scheme.
- On first launch, enter your backend's **LAN URL** (e.g.
  `http://192.168.1.50:8080` — not `localhost`) and tap **Save**, then **Test
  Connection**. Doing this from the foreground app matters: it's what
  triggers iOS's Local Network permission prompt, and the extension (which
  has no UI and can't prompt) relies on that same grant. Accept the prompt.
- Open the **Files** app → **Browse** → **Edit** (top right) → enable
  **Findo** under Locations. Your enabled volumes should appear as folders;
  drilling in lists real entries from the backend's index, and tapping a
  file downloads it via `GET /files/content` and opens it in Quick Look /
  whatever app claims its type.

## Troubleshooting

- **Install fails with "does not define an NSExtension dictionary in its
  Info.plist"**: something (usually Xcode's own "Fix Issue" button on an
  earlier signing error) set `GENERATE_INFOPLIST_FILE = YES` on
  `FindoFileProvider` directly in the `.xcodeproj`. That build setting
  makes Xcode synthesize the Info.plist from `INFOPLIST_KEY_*` build
  settings and silently drop arbitrary dictionaries like `NSExtension`
  from the real `Info.plist` file, even though `INFOPLIST_FILE` still
  points at it. Fix: quit Xcode, `rm -rf Findo.xcodeproj`, `xcodegen
  generate` again, wipe `~/Library/Developer/Xcode/DerivedData/Findo-*`,
  reopen, re-pick your Team on all three targets. Never use Xcode's "Fix
  Issue" for a signing/Info.plist error — it edits the generated project
  directly, which won't survive (or will half-survive) the next
  regenerate; make the fix in `project.yml` instead.

## Known MVP limitations

- **Read-only.** The backend has no write/save-back API yet, so items only
  advertise `.allowsReading` — edits in another app won't push back to the
  NAS. `itemChanged(at:)` is a deliberate no-op.
- **No Siri/Spotlight indexing yet** (App Intents + Core Spotlight is the
  next milestone per `.claude/CLAUDE.md`).
- **Full-file downloads.** `startProvidingItem` downloads the whole file
  even though the backend's `/files/content` supports `Range`; scrubbing/
  resuming large files isn't wired up in the extension yet.
- **No offline cache / local metadata DB.** Every enumeration and lookup is
  a live backend call — fine at home-LAN scale, not something to scale up
  without revisiting.

## Code map

- `FindoKit/Sources/FindoKit/APIClient.swift` — talks to the backend
  (`/health`, `/volumes`, `/files`, `/search`, `/files/content`). Matches
  `backend/internal/httpapi/server.go` exactly; keep both in sync if the
  backend's routes or JSON shapes change.
- `FindoKit/Sources/FindoKit/FindoItemIdentifier.swift` — encodes a File
  Provider item identifier as `(volumeID, path)`, base64'd. No local
  identifier database: everything's derived from the backend's own paths.
- `FindoFileProvider/Sources/FileProviderExtension.swift` — the
  `NSFileProviderExtension` subclass: item lookup, content fetch, placeholder
  handling. Uses the legacy (non-replicated) File Provider API rather than
  `NSFileProviderReplicatedExtension`, to keep this first milestone small.
- `FindoFileProvider/Sources/FileProviderEnumerator.swift` — lists a
  container's children (volumes at the root, files/dirs below that).
- `Findo/Sources/ContentView.swift` — the entire host app UI: backend URL
  entry + connection test. Everything else happens inside the Files app via
  the extension.
