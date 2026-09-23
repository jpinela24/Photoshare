<p align="center">
  <img src="docs/logo.png" alt="PhotoShare" width="120" />
</p>

# PhotoShare

**A self-hosted photo & video library for your home network — runs as a Docker container or a native Windows desktop app.**

PhotoShare turns a folder of photos and videos into a fast, private, Google
Photos-style gallery you can browse from any device on your LAN — no cloud, no
subscriptions, no third-party accounts. It's a single Go binary with an embedded
React web UI, packaged as a small Docker image (Linux) or an installer with a
tray icon and a native window (Windows).

**Current version: v2.25.0** · Linux / Docker · Windows

---

## Features

### Browse & view
- **Folder-based library** — keeps your existing folder structure; tight Google Photos-style grid with three density sizes.
- **Library header** — each folder opens with its name, what it holds, and a clickable breadcrumb; folder cards carry their own photo/video counts.
- **Type filter** — All / Photos / Videos chips in the top bar. The choice sticks across folders and reloads, like tile size.
- **Amber theme** — a warm accent alongside dark, light and auto, on the same theme button.
- **Cinematic transitions** — opening a folder fades in with a quick staggered card zoom (respects `prefers-reduced-motion`).
- **Fast cached thumbnails** for photos, videos and HEIC, pre-generated in the background.
- **Full-screen viewer** — keyboard navigation, rotate, EXIF info, download, and an in-strip filmstrip.
- **HEIC support** — Apple HEIC photos are decoded on the server (via libheif) and shown as JPEG.
- **Video that plays anywhere** — HEVC/H.265 (iPhone `.MOV`/`.mp4`) is transcoded to H.264 on first play, cached, and served with correct MIME so it plays in **any** browser including Firefox. H.264 files stream directly.
- **Hover-scrub** video previews in the grid.

### Find
- **Search** by name with **type** (photo/video) and **date** filters.
- **Smart (AI) search** — find photos by what they show ("beach at sunset"), powered by local CLIP. Optional, fully private, off unless the ML sidecar is enabled.
- **Duplicate finder** — finds **exact** copies (content-hash) *and* **similar photos** (perceptual hash: the same picture saved at a different size or quality). Parallel + cached, so re-scans are near-instant; recommends which copy to keep and cleans up the rest in one click.
- **Storage stats** — library size, counts, and real disk usage.
- **Timeline** — the whole library newest-first by capture date, independent of folders, with a month rail to jump to any month. Pages in as you scroll.
- **Favorites** — star a photo from the viewer; the list is kept **server-side per account**, so it is the same on every device and survives clearing site data. Stars follow a file when it moves.
- **Library health** — pre-generation already decodes every file, so whatever it fails on is listed in **Settings → System** as a standing integrity check.
- **On This Day** memories — background date index surfaces past photos.
- **Map view** — plots photos by EXIF GPS on an OpenStreetMap.

### Manage (admin)
- Delete, move, copy, rename, rotate — single or batch. **Folders can be moved too**: turn on select mode and click one.
- **Drag-and-drop** onto sidebar folders; **shift-click range** and **marquee** multi-select.
- **Recycle bin** — restore deleted items, **several at a time** with checkboxes and Select all; auto-purges after 90 days.
- **Uploads** — public inbox + authenticated uploads, with optional **auto-sort into Year/Month** folders by capture date.
- **Share to PhotoShare (Android)** — install to the home screen and PhotoShare appears in the system share sheet; shared photos go straight to the inbox. Uses the Web Share Target API, which **iOS does not implement** — on iPhone (and on a laptop) use the upload page or drag-and-drop.

### Accounts & security
- **Full login gate** — nobody sees anything until signed in.
- **Multiple users** with **admin / viewer** roles, managed in-app.
- **Optional guest access** toggle.
- **Persistent sessions** — HttpOnly cookies, ~30-day sliding expiry ("stay logged in").
- **bcrypt**-hashed passwords and **login rate-limiting**.

#### Duplicate finder API

Scanning reads and hashes the whole library, so anything that starts work is
admin-only and goes through the POST + same-origin (CSRF) middleware. Reading
status is separate and never starts a scan:

| Endpoint | Method | Who |
|----------|--------|-----|
| `/api/duplicates` | `GET` | any signed-in user — read-only status/results |
| `/api/duplicates/scan` | `POST` `{rescan}` | admin — start or force a rescan |
| `/api/duplicates/folder` | `POST` `{path, recursive}` | admin — scan one folder |
| `/api/duplicates/cancel` | `POST` | admin — stop a running scan |
| `/api/duplicates/resolve` | `POST` | admin — move duplicates to the recycle bin |

Cleanup re-verifies each file against the scan (size, nanosecond mtime and a
re-read content hash) immediately before trashing it, and refuses with
`file changed since scan; rescan required` if anything differs — so a result
that went stale can never delete a file that was edited or replaced in the
meantime.

### Sharing & UX
- **QR connect** — scan to open the gallery on a phone (uses the real published address).
- **PWA install** on phones.
- **Optional SMB** network path display per folder.
- **Upload notifications** — POST to an **ntfy** or **Discord** webhook when photos are uploaded (Settings → System). Optional, off by default.
- **Dark / light / auto** themes (Material, near-black dark).
- **Keyboard shortcuts** overlay (`?`).

---

## Tech stack

| Layer      | Tech                                                      |
|------------|----------------------------------------------------------|
| Backend    | Go (`net/http`), assets embedded via `embed`             |
| Frontend   | React + Vite, `@tanstack/react-virtual`, Leaflet (map)   |
| Images     | `disintegration/imaging`, `golang.org/x/image`, `goexif` |
| Video/HEIC | FFmpeg / ffprobe + libheif (`heif-convert`)              |
| Auth       | `golang.org/x/crypto/bcrypt`, cookie sessions            |
| Extras     | `skip2/go-qrcode` (QR)                                    |

---

## Run with Docker

```bash
docker compose up -d --build
```

Edit `docker-compose.yml` first to point `/photos` at your library and set an
initial `ADMIN_PASSWORD`. Config persists in `./photoshare-config` (`/config`).

| Env var | Purpose | Default |
|---------|---------|---------|
| `PHOTO_DIR` | Library path inside the container | `/photos` |
| `DATA_DIR` | Where config/cert persist (and the thumbnail cache, unless `THUMB_DIR` is set) | `/config` |
| `THUMB_DIR` | Thumbnail / transcode cache. Put it on a roomy disk — transcodes are full video re-encodes — in its own directory, **not** inside the library. Safe to delete; it rebuilds | `DATA_DIR/thumbs` |
| `PORT` | Listen port inside the container | `8080` |
| `HTTP_ONLY` | Plain HTTP (put a reverse proxy in front for TLS) | `true` |
| `ADMIN_USER` / `ADMIN_PASSWORD` | First-run admin account (ignored once accounts exist). If `ADMIN_PASSWORD` is unset, a random one is generated and printed once to the logs | `admin` / random |
| `SERVER_IP` | Host LAN IP for correct QR / network links | auto |
| `PUBLIC_PORT` | Published host port for QR / share links (if different from `PORT`) | = `PORT` |
| `PUBLIC_URL` | Full override for the advertised URL (wins over the above) | — |
| `AUTO_SORT` | Auto-file inbox uploads into Year/Month | `false` |

Saving Settings exits the process; with `restart: unless-stopped`, Docker brings
it back with the new config.

### Build / run without Docker

```bash
make build                                  # builds the React app + Go binary
./photoshare -dir /photos -http-only -port 8080
```

### Notes
- The **photo directory must exist** — the app won't auto-create it (so a typo can't spawn an empty library), but it auto-creates `_Trash` and `_Uploads` inside it.
- Logs go to stdout (`docker logs photoshare`).
- Transcoded video and thumbnails are cached in the container's temp dir, **not** in your photo library.

---

## Run on Windows

PhotoShare also ships as a native Windows desktop app: a system tray icon,
its own window (no browser tab), and an installer like any other Windows
program — no Docker required.

1. Download and run **PhotoShareSetup.exe** (Intel/AMD x64). On a Windows 11
   ARM device (Snapdragon etc.) grab **PhotoShareSetup-arm64.exe** for a
   native build — the x64 one also runs there via emulation if you prefer.
2. On first launch, pick your photo library folder and create the admin
   account right in the app — no config file editing needed.
3. PhotoShare lives in the system tray; closing the window just hides it.
   Right-click the tray icon for **Open**, **Open in browser**, **Copy URL**,
   and **Quit**.
4. In Settings, optionally enable **"Start PhotoShare when Windows starts"**
   and check for updates.

Config and the library path live in `%APPDATA%\PhotoShare`; the installer
never touches your photos.

### Building the Windows installer yourself

```bash
make build-windows                 # x64: builds the React app + photoshare.exe
iscc windows\installer.iss         # requires Inno Setup (https://jrsoftware.org/isinfo.php)

make build-windows-arm64           # ARM64 build instead
iscc /DAppArch=arm64 /DSetupName=PhotoShareSetup-arm64 windows\installer.iss
```

The compiled installer lands in `windows/Output/PhotoShareSetup.exe` (or
`PhotoShareSetup-arm64.exe`).
Re-running it for a later version updates the binary in place and keeps your
existing library path and accounts.

---

## Releasing a new version

One repo, one codebase — Go build tags (`*_windows.go` vs `*_stub.go`) keep
the Windows-only code out of the Linux/Docker build and vice versa. Pushing
code alone doesn't update anyone; each platform's artifact is built and
published separately, automatically, by [`.github/workflows/release.yml`](.github/workflows/release.yml)
whenever a version tag is pushed:

```bash
git tag v2.3
git push origin v2.3
```

That single push triggers two parallel jobs:
- **Windows**: builds `photoshare.exe`, compiles the Inno Setup installer,
  and attaches `PhotoShareSetup.exe` to the GitHub Release for that tag
  (creating the release if it doesn't exist yet) — this is what the app's
  in-app "Check for updates" button looks for.
- **Docker**: builds the image and pushes it to
  `ghcr.io/jpinela24/photoshare:v2.3` and `:latest`.

No secrets to configure — both jobs use the automatic `GITHUB_TOKEN`.

---

## Changelog

| Ver | Highlights |
|-----|-----------|
| **1.0** | Core photo server: browse, full-screen viewer, search, duplicate finder, storage stats, recycle bin, uploads, admin (delete/move/copy/rename/rotate + batch), HTTPS, PWA |
| **1.5** | Apple "liquid glass" redesign; collapsible push-sidebar |
| **1.6** | Full SVG icon set; dark / light theme toggle |
| **1.7** | QR connect, auto theme, 90-day trash auto-purge, hover-scrub video, HTTP-only option, bcrypt passwords + login rate-limit, panic recovery |
| **1.8** | Auto-sort uploads into Year/Month folders; "On This Day" memories |
| **1.9** | Map view (EXIF GPS), shift-click range + marquee select, `?` shortcuts overlay |
| **2.0** | Login gate with multiple accounts (admin/viewer) + guest access, cookie sessions; Docker/Linux deployment with env-var config |
| **2.1** | Google Photos / Material restyle — tight grid, image-only photo tiles, blue accent, dark + light |
| **2.2** | HEIC via libheif; on-the-fly HEVC→H.264 transcode with explicit MIME (plays everywhere); cinematic folder transitions; `PUBLIC_PORT`/`PUBLIC_URL` for correct QR; real disk-space stats; hidden housekeeping folders; **Docker/Linux only** |
| **2.3** | Windows support is back, done properly this time: native installer, system tray, WebView2 window, in-app first-run setup (pick your library folder + create the admin account with no config editing), opt-in login autostart, in-app update check |
| **2.4** | Tray icon no longer lingers as a "ghost" after quitting or uninstalling — the tray watches the app's process and disposes itself the moment it's gone (quit, crash, Task Manager, or uninstall); `loadConfig` no longer resurrects default values into an existing config; Settings warns when the admin account still uses a default password; Windows window loads loopback (no "Not Secure"), blocks browser zoom/reload, and remembers its size/position |
| **2.4.1** | Windows desktop app now always serves plain HTTP — fixes the `NET::ERR_CERT_AUTHORITY_INVALID` screen that appeared in the window when an older config had self-signed HTTPS enabled (the "Use HTTPS" toggle is hidden on Windows, since real TLS belongs in a reverse proxy, not the desktop build) |
| **2.4.2** | New brand logo throughout — browser tab favicon, PWA/home-screen icon, Windows tray + taskbar icon, and the login/onboarding screens all use the real PhotoShare mark |
| **2.4.3** | The logo is now embedded in the Windows `.exe` itself (multi-size icon + version metadata), so it shows in Explorer, Add/Remove Programs, and the app's file properties — not just at runtime |
| **2.4.4** | Start Menu and desktop shortcuts now carry the logo explicitly (the installer ships `icon.ico` and points the shortcuts at it), fixing the generic shortcut icon |
| **2.5.0** | Native **Windows 11 ARM64** build — a separate `PhotoShareSetup-arm64.exe`, with the in-app updater auto-picking the installer matching your CPU (the x64 build still runs on ARM via emulation if you prefer) |
| **2.5.1** | The Windows window's native title bar is now dark to match the app, instead of the default light caption |
| **2.5.2** | Fixed the Windows app exiting (and not relaunching) after first-run setup or a settings save — the relaunched copy was tripping the single-instance lock the old process still held; the lock is now released before relaunch |
| **2.6.0** | Security hardening from an audit: `/api/quit` + `/api/show` are now local-only + POST (no remote shutdown); trash-restore and batch-rename can't write outside the library (path-traversal guards); first-run seeds a **random** admin password instead of a fixed default; updated `golang.org/x/crypto`/`x/image`; `govulncheck` in CI; reproducible `npm ci` builds; added path-boundary + authorization regression tests |
| **2.7.0** | Redesigned the Copy/Move folder picker into a single-pane navigator — breadcrumb, click a folder to step into it, live destination, and a **"New folder here"** button so you can create a destination on the spot |
| **2.7.1** | "Check for updates" now shows on every platform, not just Windows — on Docker/browser it reports whether a newer release exists and how to update (`git pull && docker compose up`), with a link to the release notes |
| **2.7.2** | Fixed the Duplicate Finder hanging on "Scanning library…" forever on large libraries — the scan now runs in the background with live progress (no long blocking request that drops behind a proxy/Tailscale), and only fully hashes files that share both a size and a 64 KiB fingerprint, so it no longer reads gigabytes of unrelated videos |
| **2.7.3** | Sharper Windows app/shortcut icon — the `.ico` is regenerated with high-quality downscaling + light sharpening at every size, fixing the low-res desktop/Start-Menu icon |
| **2.8.0** | Reorganized Settings into clean **tabs** (General · Sharing · Users · System) instead of one long form. New **"Enable Web UI on your network"** toggle (Windows) — when off, the server binds to loopback only so only this PC can use it while the app window keeps working; on by default |
| **2.8.1** | Windows-Explorer-style **Quick access** in the library Browse dialog — jump straight to Desktop, Downloads, Documents, Pictures, Videos, Music or Home (OneDrive-aware), with drives under "This PC". Also restyles the picker, which had lost its styling in 2.7.0 |
| **2.9.0** | **AI semantic search (Phase 1)** — find photos by what they show ("beach at sunset") instead of filenames, powered by local CLIP running in an optional `photoshare-ml` sidecar. Fully private (no cloud) and feature-flagged behind `ML_URL`, so it's off unless enabled. Adds a "Smart" search toggle in the sidebar with live indexing progress |
| **2.10.0** | **Face recognition — People view (Phase 2)** — detects faces on the same ML sidecar (small `buffalo_s` model), groups them into people you can name and browse, all local. Deliberately low-impact: **off by default** (`FACES=1` to enable), lazy-loaded, and the background detector yields to the semantic-search indexer so the two never compete for CPU |
| **2.10.1** | Face recognition is now a **Settings toggle** (System tab) instead of env-only — flip it on/off in the app when the AI sidecar is available; `FACES=1` still works as an override |
| **2.10.2** | **Removed face recognition / the People view** — the detector produced too many false positives to be useful. Semantic (Smart) search is unaffected; the ML sidecar is back to CLIP-only (no insightface), so it's smaller and builds cleanly |
| **2.11.0** | **Better path bar + keyboard/selection** — the address bar gets an **up-one-level** button, a home icon, and scrolls on long paths. Full grid keyboard nav: **↑/↓ jump a row**, Home/End, Enter to open, **Backspace** to go up, Esc to clear, with the focused item auto-scrolled into view. Multi-select now works without entering select mode first: **⌘/Ctrl-click** toggles items, **Shift-click** ranges, **Space** toggles the focused item, **⌘/Ctrl+A** selects all (also fixes a range-select anchor bug) |
| **2.12.0** | **Upload notifications (integration)** — set an **ntfy** or **Discord** webhook in Settings → System and get a message whenever photos are uploaded (public inbox or a folder). Auto-detects Discord (JSON) vs ntfy/generic (plain POST + Title header), with a **Send test** button. Fire-and-forget, off by default |
| **2.12.1** | **Tighter, less-cluttered grid** — tiles are smaller across all three densities, the grid now **defaults to Small**, and your density choice is **remembered** across reloads (it used to reset to Medium every time) |
| **2.25.0** | **Move folders, not just files** — in select mode, clicking a folder picks it instead of opening it, so folders can go through **Move** like anything else. Moving a folder into itself or its own subtree is refused with a clear message rather than left to the filesystem, cached thumbnails for the whole subtree are cleared, and **stars inside a moved folder follow it**. Copy stays disabled for folders (it would need a recursive copy the server doesn't do) — move them instead |
| **2.24.1** | **The type filter sticks** — All / Photos / Videos reset to All on every navigation, so it had to be re-applied in each folder. It is now remembered across folders and across reloads, like tile size. A folder whose contents the filter hides now says so and offers **Show all**, instead of looking like an empty folder |
| **2.24.0** | **Filter chips move into the top bar** — **All / Photos / Videos** now sit beside the sort control instead of above the grid, and the search field gives up most of its width to make room. Search keeps a usable minimum so it can't collapse to just the magnifier, and the Upload button drops its label below 1150px. On phones the chips stay above the grid: the top bar is held to two rows there and cannot fit chips, search and the tools at once |
| **2.23.2** | **Back goes back one folder** — opening a folder from the **grid** never recorded a history entry (only the sidebar and breadcrumbs did), so Back skipped every folder opened that way and jumped to the last entry the sidebar happened to record — usually all the way home. Grid and search-result folders are now recorded like any other navigation, and the history index is tracked so Back, Forward and the disabled states stay in step |
| **2.23.1** | **Select All respects the All/Photos/Videos filter** — with a filter on, both **⌘/Ctrl+A** and the **Select All** button selected the whole folder, including items the filter had hidden, so a Move or Delete could act on files that were not on screen. Both now select only what is visible, and switching the filter drops anything it just hid. Introduced in 2.21.0 with the filter chips |
| **2.23.0** | **Restore several files at once from the Recycle Bin** — checkboxes and a **Select all**, with a selection bar to **Restore** or **Delete forever** everything picked. A restore that partly fails now reports what worked instead of stopping at the first error. Also fixes two light-mode contrast bugs on that screen: the row's **Restore** button kept its dark-theme ink and sat at roughly 1.5:1 on white — effectively invisible — and the delete buttons were not much better |
| **2.22.0** | **Favorites, library health, and a tidier sidebar** — **Favorites**: star a photo from the viewer and it lands in a new Favorites view. The list is **server-side and per account**, so unlike the pinned folders it sits next to, it is the same on every device and survives clearing site data; stars also follow a file when it is moved. **Library health** (Settings → System): thumbnail pre-generation already decodes every photo and video, so anything it cannot read is now listed with the reason — a standing corruption check over the whole library that costs nothing extra. **On This Day** and **Map** viewers gained working ‹ › arrows and a filmstrip; opening a photo from either used to be a dead end. The sidebar is reorganised: Timeline, Favorites, On This Day and Map move to the top under **Library**, and **Rebuild Thumbnails** moves down beside Duplicates with the other maintenance tools |
| **2.21.4** | **Service worker (for PWA install)** — adds a deliberately inert service worker at `/sw.js`. It caches nothing and never intercepts a response; it exists only because Chrome gates **PWA installability** on a registered worker with a fetch handler, and installing to the home screen is what the Android **share target** needs. Note that installability also requires **HTTPS** — a plain-HTTP LAN deployment stays non-installable, and there the worker simply never registers |
| **2.21.3** | **Quieter amber accents** — the Upload button and the selected tab in Settings were solid blocks of accent colour, the loudest things on screen. They now use the sidebar's Upload Inbox treatment: amber text and icon on a soft tint, no solid fill. Light mode uses a darker amber so the text stays readable — contrast is 6.5:1 on dark and 4.5:1 on light, slightly better than the white-on-blue it replaced |
| **2.21.2** | **Dialogs centre over the photos, not the window** — with the sidebar open, confirmation dialogs were centred on the whole browser window, which put them visibly left of the grid they were about. They now inset by the docked sidebar width and follow it as it opens and closes. Applies above 1100px only: on a narrower window the inset would squeeze wide dialogs like Settings, and the offset isn't noticeable there. The full-screen photo viewer stays dead centre, since it covers the sidebar |
| **2.21.1** | **One account control, and the QR code moves into Settings** — the username chip and the log-out button are now a single control: tapping your name opens a small menu with your role and **Log out**, so a stray click on your own name can no longer sign you out. The **Connect a phone** QR code left the top bar for **Settings → Server**, where the rest of the "how is this reachable" options live |
| **2.21.0** | **Library UI refresh** — folders now open with a proper page header: the folder name, a plain-English summary of what it holds, and a clickable **breadcrumb**. Folder cards gained their own **photo/video counts**, which ride along with the folder listing rather than costing one request per card. **All / Photos / Videos** chips filter the current folder (folders always stay visible, so a filter can never strand you). Search is now a **permanent field** in the top bar instead of a magnifier that expands — it replaced the address bar, whose job the breadcrumb now does — and **Upload Photos** is a filled button rather than hiding in the sidebar. Adds an **amber** theme to the existing dark/light/auto cycle. The phone top bar stays at two rows |
| **2.20.0** | **Timeline, and share-to-app on Android** — a new **Timeline** view lists the whole library newest-first by capture date, independent of folders, with a month rail that jumps straight to any month; it pages in as you scroll rather than loading everything, and the viewer's ‹ › arrows and filmstrip now work inside it instead of opening on a dead end. It reuses the same date index that powers On This Day, so it costs nothing extra to maintain. Installing the app to an Android home screen now also puts **PhotoShare in the system share sheet** — shared photos and videos land in the upload inbox. Shares are only accepted for a signed-in session and are checked by extension *and* magic bytes, exactly like a normal upload, so the share sheet can't be used to drop arbitrary files into the library. Note that the Web Share Target API is Android-only: **iOS does not implement it**, so on iPhone the upload page is still the way in |
| **2.19.1** | **`THUMB_DIR` — put the thumbnail cache on its own disk** — 2.19.0 moved the cache into `DATA_DIR`, which is the right default but the wrong disk if your config volume is small: the cache holds H.264 transcodes, which are full video re-encodes and can run to gigabytes. `THUMB_DIR` now overrides the location, so config stays small and backup-friendly while the cache lives wherever there is room. `docker-compose.yml` ships with it pointed at a `/cache` volume alongside the library. Pointing it *inside* the photo library is refused at startup (the library walkers would treat thumbnails as content); the library walk skips the cache directory either way Also fixes a flaky test: the duplicate-scan endpoint test let its background scan outlive the test, racing the next test and dropping a stray `dupe-cache.gob` in the repo root |
| **2.19.0** | **Thumbnail cache survives deploys, and stops going stale** — the cache moved from the OS temp directory into `DATA_DIR`. On Docker it lived in `/tmp` *inside* the container, so every `docker compose up -d --build` threw away the whole cache and re-generated thumbnails for the entire library. Cache entries are now keyed on each file's size and modification time as well as its path: editing or replacing a photo in place used to keep serving the thumbnail built from the **old** contents forever, with no way to refresh it short of "Rebuild Thumbnails". Moving, renaming or trashing a file now discards its cached thumbnail instead of leaking it, and a sweep at startup reclaims entries orphaned by changes made outside the app (a network share, a sync client). **One-time rebuild:** the first start after upgrading regenerates every thumbnail, because both the location and the key scheme changed — expect the library to fill in gradually while pre-generation runs. This happens once; after that the cache persists across restarts and container rebuilds |
| **2.18.1** | **Moving files updates the grid immediately** — moving a selection used to leave every card on screen until you refreshed, even though the files were already gone from the folder. The server now reports exactly which files left, so the grid drops those cards and the sidebar folder counts re-fetch straight away; a file that failed to move, or that was already in the destination, correctly stays put. Also fixes batch **rename** and inbox uploads never refreshing the view at all, and a partial move that copied a file but could not delete the original now reports the failure instead of claiming success |
| **2.18.0** | **Security audit fixes** — duplicate cleanup now re-verifies every file against the scan (size, nanosecond mtime and a re-read content hash) immediately before trashing it, so a stale result can no longer delete a file that was edited or replaced since the scan. Concurrent uploads of the same filename can no longer overwrite each other — destination names are claimed atomically instead of checked-then-renamed. Config saves are **transactional**, so a settings save racing a user change can no longer drop accounts, and a failed write rolls back rather than leaving the server disagreeing with disk. Starting, cancelling and folder-scoped duplicate scans are now **admin-only POSTs** (reading status stays a GET and never starts work). Frontend build dependencies upgraded to clear **6 advisories (4 high) → 0**; Docker/CI move to Node 22 |
| **2.17.3** | **Prev/next arrows while watching a video** — the viewer's ‹ › arrows were suppressed for videos, so a video was a dead end: you had to close the lightbox and pick the next item by hand. They now show for video exactly as for photos (the player already reserves side gutters for them, so they sit clear of the native video controls) |
| **2.17.2** | **Roomier phone path row** — the sort control moved up to the first row beside the other view preferences (it was the only tool with a text label, so it dominated the path row), and the back/forward arrows took its place next to the address bar the way a browser arranges them. The address bar gains room and the row drops from four controls to three. Also fixes a regression from 2.17.1 where the mobile rule that hides the top-bar username was also hiding every name in **Settings → Users** |
| **2.17.1** | **Mobile-friendly top bar** — on a phone the top bar's ~11 controls ran off the right edge, leaving the sort dropdown and account buttons unreachable. It now folds into **two compact rows** (identity + view controls, then path + tools), the QR button hides (you're already on the phone), the username collapses to its lock icon, and buttons grow to thumb-sized. The sidebar **slides over** the grid instead of squeezing it, and the Small/Medium/Large tile toggle is finally respected on phones — a blanket mobile rule had been overriding it. Desktop layout is unchanged |
| **2.17.0** | **Duplicates in the folder you're browsing** — a new toolbar button checks **just the current folder** for duplicates, instead of making you scan the whole library and hunt for the relevant rows. Toggle **"Including subfolders"** to widen it. Runs in one request (it reuses the library scan's hash cache, so it's near-instant on an already-scanned library) and comes with the same keep-best recommendation, checkbox multi-select and one-click cleanup |
| **2.16.1** | **Click the PhotoShare name to go home** — the app name in the top-left corner is now a button that jumps back to **All Photos** from anywhere (and closes an open search), the way a site logo usually behaves |
| **2.16.0** | **Search moved into the grid** — search now opens from a **magnifier next to the sort controls** instead of living in the sidebar, and results fill the **main grid** as normal photo/folder cards (full-size thumbnails, lightbox, select, delete) rather than a cramped sidebar list. Smart (AI) search and the type/date filters came along; the address bar reads **"Search results"** while a search is active, and opening a folder result jumps there and closes search |
| **2.15.6** | **Drag files onto the sidebar to upload** — dropping photos/videos from your desktop onto the **Upload Inbox** row uploads them straight to the inbox (the row highlights as you drag over it). Dragging *library* files onto the same row still moves them into the inbox as before — the two drag types are told apart automatically |
| **2.15.5** | **Cleaner top bar** — the upload button moved out of the toolbar and into the sidebar's **Upload Inbox** row (where uploads actually land), and the tile-size toggle moved to the left cluster next to the theme button. The top bar is now just navigation, view controls, and account |
| **2.15.4** | **One-button tile size** — the three Small/Medium/Large buttons are now a single toolbar button that **cycles** through the sizes on click, just like the theme toggle. Less toolbar clutter, same three densities, still remembered across reloads |
| **2.15.3** | **Hand-picked duplicate cleanup** — every non-keeper file in the Duplicate Finder now has a **checkbox**; select any mix across groups and hit **"Trash selected (N)"** to bin exactly those, without touching the rest. Recommended keepers can't be selected, and everything still goes to the recycle bin |
| **2.15.2** | **Similar photos sorted by match strength** — similar-photo groups in the Duplicate Finder are now ordered by **match percentage, highest first** (recoverable size breaks ties), so the most confident cleanups are always at the top |
| **2.15.1** | **Thumbnail folders no longer pollute results** — hidden housekeeping folders (`thumbs`, at any depth) were skipped by the browse view but still scanned by the duplicate finder, search, storage stats, On This Day, and the thumbnail pre-generator. A cached thumbnail is by definition a smaller copy of its original, so the duplicate finder saw endless "duplicates" of files you can't even see. All library scanners now skip them consistently |
| **2.15.0** | **Settings redesigned** — a proper settings dialog with **sidebar navigation** (Library · Server · Notifications · Users · System), each page with clear titles and per-setting descriptions. Checkboxes became real **toggle switches**; rarely-touched options (FFmpeg path, upload folder name, server IP, port) moved into collapsed **Advanced** sections. The **Save & Restart** button and its warning now only appear when something actually changed. Fully responsive — the nav collapses to a top bar on phones |
| **2.14.0** | **Duplicate finder: faster, and finds more** — hashing now runs on a **worker pool** and every hash is **cached** (keyed by path+size+mtime), so a re-scan only pays for new or changed files. Sampling checks the file's **head *and* tail**, so same-camera videos no longer force needless full reads. New: **similar-photo detection** via perceptual hashing — catches the same picture saved at a different size/quality (WhatsApp copies, resizes, HEIC vs JPEG) that an exact hash can never see. Each group now **recommends which copy to keep** (highest resolution, most original name/location) with one-click **"keep best, trash the rest"** and a global **"trash all extras"**; long scans can be **canceled**. Hardlinks are no longer miscounted as wasted space |
| **2.13.2** | **A little more room in the grid** — bumped the spacing between folder/photo tiles (the effective gap was only 3px, which read as cluttered) so the grid breathes without changing tile size |
| **2.13.1** | **Content-validated uploads** — uploaded files are now checked by their actual **magic bytes**, not just the filename extension, so a script/HTML/executable renamed to `.jpg` is refused (`contents are not a photo or video`). Covers every accepted format including HEIC/HEIF and all video containers, so valid photos/videos are never rejected |
| **2.13.0** | **Security hardening** — every state-changing endpoint now enforces its HTTP method (`405` + `Allow`) and rejects **cross-origin** requests (CSRF), so a `GET` can never delete or purge. Sessions are **revalidated on every request** against the live user list: deleting or demoting a user, changing a password, or disabling guest access now takes effect immediately instead of lingering until the token expires. `safePath` resolves **symlinks** and blocks any that escape the library (in-library symlinks still work), and `/api/photo` refuses non-media files. Also: guest login no longer resets the password brute-force lockout, uploads are written **atomically** with per-file error reporting (no more truncated files on failure), and the config file is written atomically with `0600` permissions (it can hold secret webhook tokens). **Reverse-proxy note:** the CSRF check compares the browser `Origin`/`Referer` host against the request `Host`, so your proxy must preserve the original `Host` header (nginx: `proxy_set_header Host $host;`) |

---

## License

Personal / home project. Use at your own risk on a trusted local network.

## Author

Made by [jpinela24](https://github.com/jpinela24).

🤖 Built with [Claude Code](https://claude.com/claude-code)
