# PhotoShare

**A self-hosted photo & video library for your home network — runs as a Docker container.**

PhotoShare turns a folder of photos and videos into a fast, private, Google
Photos-style gallery you can browse from any device on your LAN — no cloud, no
subscriptions, no third-party accounts. It's a single Go binary with an embedded
React web UI, packaged as a small Docker image.

**Current version: v2.2** · Linux / Docker

---

## Features

### Browse & view
- **Folder-based library** — keeps your existing folder structure; tight Google Photos-style grid with three density sizes.
- **Cinematic transitions** — opening a folder fades in with a quick staggered card zoom (respects `prefers-reduced-motion`).
- **Fast cached thumbnails** for photos, videos and HEIC, pre-generated in the background.
- **Full-screen viewer** — keyboard navigation, rotate, EXIF info, download, and an in-strip filmstrip.
- **HEIC support** — Apple HEIC photos are decoded on the server (via libheif) and shown as JPEG.
- **Video that plays anywhere** — HEVC/H.265 (iPhone `.MOV`/`.mp4`) is transcoded to H.264 on first play, cached, and served with correct MIME so it plays in **any** browser including Firefox. H.264 files stream directly.
- **Hover-scrub** video previews in the grid.

### Find
- **Search** by name with **type** (photo/video) and **date** filters.
- **Duplicate finder** — content-hash based.
- **Storage stats** — library size, counts, and real disk usage.
- **On This Day** memories — background date index surfaces past photos.
- **Map view** — plots photos by EXIF GPS on an OpenStreetMap.

### Manage (admin)
- Delete, move, copy, rename, rotate — single or batch.
- **Drag-and-drop** onto sidebar folders; **shift-click range** and **marquee** multi-select.
- **Recycle bin** — restore deleted items; auto-purges after 90 days.
- **Uploads** — public inbox + authenticated uploads, with optional **auto-sort into Year/Month** folders by capture date.

### Accounts & security
- **Full login gate** — nobody sees anything until signed in.
- **Multiple users** with **admin / viewer** roles, managed in-app.
- **Optional guest access** toggle.
- **Persistent sessions** — HttpOnly cookies, ~30-day sliding expiry ("stay logged in").
- **bcrypt**-hashed passwords and **login rate-limiting**.

### Sharing & UX
- **QR connect** — scan to open the gallery on a phone (uses the real published address).
- **PWA install** on phones.
- **Optional SMB** network path display per folder.
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
| `DATA_DIR` | Where config/cert persist | `/config` |
| `PORT` | Listen port inside the container | `8080` |
| `HTTP_ONLY` | Plain HTTP (put a reverse proxy in front for TLS) | `true` |
| `ADMIN_USER` / `ADMIN_PASSWORD` | First-run admin account (ignored once accounts exist) | `admin` / — |
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

---

## License

Personal / home project. Use at your own risk on a trusted local network.

## Author

Made by [jpinela24](https://github.com/jpinela24).

🤖 Built with [Claude Code](https://claude.com/claude-code)
