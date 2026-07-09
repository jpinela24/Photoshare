package main

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	"github.com/rwcarlsen/goexif/exif"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
	_ "golang.org/x/image/webp"
)

//go:embed all:client/dist
var clientDist embed.FS

var (
	baseDir        string
	thumbDir       string
	trashDir       string
	uploadDir      string
	port           string
	httpOnly       bool   // serve plain HTTP (no cert, no "Not Secure" warning)
	autoSort       bool   // auto-file inbox uploads into Year/Month folders
	lanAccess      bool   // serve the web UI on the LAN (false = loopback only)

	usersMu     sync.Mutex
	users       []User // accounts that can log in
	guestAccess bool   // allow anonymous view-only "Continue as guest"
	shareName      string
	serverIPFlag   string
	ffmpegFlag     string
	cachedFFmpeg   string
	configFilePath string
	dataDir        string // where config/cert live (env DATA_DIR, default = exe dir)

	// netURL is the shareable LAN address shown in the QR code.
	netURL string
	// mainMux is the shared HTTP handler, set during main() startup.
	mainMux *http.ServeMux
)

// ── Config file ───────────────────────────────────────────────────────────────

type AppConfig struct {
	PhotoDir     string `json:"photoDir"`
	Port         string `json:"port"`
	AdminPass    string `json:"adminPassword,omitempty"`     // plaintext: only for first-run/legacy input; migrated to a hash
	AdminPassHash string `json:"adminPasswordHash,omitempty"` // bcrypt hash (preferred)
	ShareName    string `json:"shareName"`
	ServerIP     string `json:"serverIP"`
	UploadFolder string `json:"uploadFolder"`
	FfmpegPath   string `json:"ffmpegPath"`
	HTTPOnly     bool   `json:"httpOnly,omitempty"`        // serve plain HTTP instead of self-signed HTTPS
	AutoSort     bool   `json:"autoSortUploads,omitempty"` // file inbox uploads into Year/Month folders by date
	Users        []User `json:"users,omitempty"`           // login accounts
	GuestAccess  bool   `json:"guestAccess,omitempty"`     // allow "Continue as guest" (view-only)
	// DisableWebUI, when true, binds the server to loopback only so the web UI
	// is reachable only from this machine (the Windows native window still
	// works); absent/false = reachable on the LAN (the default). Inverted so an
	// existing config without the key keeps LAN access.
	DisableWebUI bool `json:"disableWebUI,omitempty"`
	// FacesEnabled turns on face recognition / the People view. Only has an
	// effect when the ML sidecar is configured (ML_URL). Off by default because
	// the detector is more CPU-heavy than search.
	FacesEnabled bool `json:"facesEnabled,omitempty"`
}

// User is a login account. Role is "admin" (full) or "viewer" (view-only).
type User struct {
	Username string `json:"username"`
	PassHash string `json:"passwordHash"`
	Role     string `json:"role"`
}

func defaultConfig() AppConfig {
	return AppConfig{
		PhotoDir:     `D:\MEMORIES`,
		Port:         "8080",
		UploadFolder: "_Uploads",
	}
}

// randomPassword returns a URL-safe random password for seeding a first-run
// admin account when the operator didn't supply one — far safer than a known
// fixed default. It's logged once so it can be read from the startup output.
func randomPassword() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is exceptional; fall back to a time-seeded value
		// so we still never seed a *fixed*, publicly-known password.
		return fmt.Sprintf("ps-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func loadConfig(path string) AppConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		// No config file yet — this really is a fresh first run.
		return defaultConfig()
	}
	// An existing file is the source of truth: unmarshal into a zero-value
	// struct rather than defaultConfig(), so an omitted field reads back as
	// empty rather than silently resurrecting defaultConfig()'s "123456"
	// admin password (or the Windows-only `D:\MEMORIES` photo dir) on every
	// single load — which previously caused a config rewrite on every
	// restart and is exactly the kind of merge bug that nearly cost real
	// admin accounts during the v2.3 rollout.
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultConfig()
	}
	// Only backfill fields that need a non-empty operational default and
	// carry no security sensitivity — never AdminPass/AdminPassHash/PhotoDir.
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.UploadFolder == "" {
		cfg.UploadFolder = "_Uploads"
	}
	return cfg
}

func saveConfig(path string, cfg AppConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// envOr returns the env var value if set, else the fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool parses a boolean-ish env var, falling back when unset/unrecognized.
func envBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

// ── Admin password (bcrypt) ─────────────────────────────────────────────────

// hashPassword returns a bcrypt hash for storage.
func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// findUser returns the account with the given username (case-insensitive).
func findUser(username string) (User, int) {
	usersMu.Lock()
	defer usersMu.Unlock()
	for i, u := range users {
		if strings.EqualFold(u.Username, username) {
			return u, i
		}
	}
	return User{}, -1
}

// authenticate verifies username/password and returns the matching user.
func authenticate(username, password string) (User, bool) {
	u, idx := findUser(username)
	if idx < 0 {
		// Compare against a dummy hash so timing doesn't reveal valid usernames.
		bcrypt.CompareHashAndPassword([]byte("$2a$10$N9qo8uLOickgx2ZMRZoMy.MH/rMc5Q1bQa1Z1Z1Z1Z1Z1Z1Z1Z1Zu"), []byte(password))
		return User{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(password)) != nil {
		return User{}, false
	}
	return u, true
}

// knownDefaultPasswords are the values PhotoShare itself has ever seeded an
// admin account with — defaultConfig()'s "123456" and the docker-compose.yml
// example's "change-me" — so a still-default account can be flagged in the
// UI instead of only ever warning in the server log on first run.
var knownDefaultPasswords = []string{"123456", "change-me"}

// hasDefaultAdminPassword reports whether any admin account's password still
// matches one of knownDefaultPasswords. Passwords are bcrypt-hashed and
// never stored in recoverable form, so this is the only way to detect it —
// by comparing the hash against each known default, the same way a real
// login attempt would.
func hasDefaultAdminPassword() bool {
	usersMu.Lock()
	defer usersMu.Unlock()
	for _, u := range users {
		if u.Role != "admin" {
			continue
		}
		for _, pw := range knownDefaultPasswords {
			if bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(pw)) == nil {
				return true
			}
		}
	}
	return false
}

// adminCount returns how many admin accounts exist (to prevent removing the last one).
func adminCount() int {
	usersMu.Lock()
	defer usersMu.Unlock()
	n := 0
	for _, u := range users {
		if u.Role == "admin" {
			n++
		}
	}
	return n
}

// ── Login rate limiter (per IP) ──────────────────────────────────────────────

type loginGuard struct {
	mu       sync.Mutex
	attempts map[string]*attemptInfo
}

type attemptInfo struct {
	count       int
	lockedUntil time.Time
}

var logins = &loginGuard{attempts: make(map[string]*attemptInfo)}

const (
	maxLoginAttempts = 5
	loginLockout     = 5 * time.Minute
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isLocalRequest reports whether r originated from this machine — either
// true loopback (127.0.0.1/::1) or one of this machine's own LAN IPs (the
// Windows desktop app's native window/tray navigate to the LAN address
// rather than localhost, so loopback alone isn't enough to recognize it as
// local). Used to restrict the unauthenticated setup endpoints (filesystem
// browsing + onboarding) to the machine running PhotoShare, so a remote
// device on the same network can't race to claim the admin account or
// enumerate directories before first-run setup completes.
func isLocalRequest(r *http.Request) bool {
	host := clientIP(r)
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.String() == host {
			return true
		}
	}
	return false
}

// locked reports whether the IP is currently locked out.
func (g *loginGuard) locked(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	a := g.attempts[ip]
	return a != nil && time.Now().Before(a.lockedUntil)
}

func (g *loginGuard) fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Opportunistically prune stale entries so the map can't grow unbounded.
	now := time.Now()
	for k, v := range g.attempts {
		if v.count == 0 && now.After(v.lockedUntil) {
			delete(g.attempts, k)
		}
	}
	a := g.attempts[ip]
	if a == nil {
		a = &attemptInfo{}
		g.attempts[ip] = a
	}
	a.count++
	if a.count >= maxLoginAttempts {
		a.lockedUntil = now.Add(loginLockout)
		a.count = 0
	}
}

func (g *loginGuard) reset(ip string) {
	g.mu.Lock()
	delete(g.attempts, ip)
	g.mu.Unlock()
}

// ── Cookie-based sessions ───────────────────────────────────────────────────

const (
	sessionCookie = "ps_session"
	sessionTTL    = 30 * 24 * time.Hour // persistent "stay logged in"
)

type session struct {
	Username string
	Role     string // "admin" | "viewer"
	Expires  time.Time
}

type sessionStore struct {
	mu sync.Mutex
	m  map[string]*session
}

var sessions = &sessionStore{m: make(map[string]*session)}

func (s *sessionStore) create(username, role string) string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := fmt.Sprintf("%x", b)
	s.mu.Lock()
	s.m[tok] = &session{Username: username, Role: role, Expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return tok
}

func (s *sessionStore) get(tok string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[tok]
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.Expires) {
		delete(s.m, tok)
		return nil, false
	}
	sess.Expires = time.Now().Add(sessionTTL) // sliding
	return sess, true
}

func (s *sessionStore) revoke(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

// sessionFromRequest reads + validates the session cookie.
func sessionFromRequest(r *http.Request) (*session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	return sessions.get(c.Value)
}

// setSessionCookie writes the session cookie. Not marked Secure so it works over
// plain HTTP too (LAN / reverse-proxy / native-window local listener); it is
// always HttpOnly + SameSite=Lax.
func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL / time.Second),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
}

// requireAuth ensures a valid session (any role — admin, viewer, or guest).
func requireAuth(w http.ResponseWriter, r *http.Request) (*session, bool) {
	sess, ok := sessionFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return sess, true
}

// requireAdmin ensures an admin session.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	sess, ok := sessionFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if sess.Role != "admin" {
		http.Error(w, "forbidden — admin only", http.StatusForbidden)
		return false
	}
	return true
}


type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	IsVideo bool   `json:"isVideo,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Mod     int64  `json:"mod,omitempty"`
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".gif": true, ".webp": true, ".bmp": true,
	".tiff": true, ".tif": true,
	".heic": true, ".heif": true,
}

func isHeic(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".heic" || ext == ".heif"
}

var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".mov": true,
	".avi": true, ".wmv": true, ".webm": true,
	".m4v": true, ".flv": true, ".ts": true,
}

// hiddenFolderNames are housekeeping directories hidden from the browse view
// at any depth (case-insensitive).
var hiddenFolderNames = map[string]bool{
	"thumbs": true,
}

func isImage(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

func isVideo(name string) bool {
	return videoExts[strings.ToLower(filepath.Ext(name))]
}

func safePath(base, rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	full := filepath.Join(base, filepath.Clean("/"+rel))
	if !strings.HasPrefix(full+string(filepath.Separator), filepath.Clean(base)+string(filepath.Separator)) &&
		full != filepath.Clean(base) {
		return "", fmt.Errorf("path outside base")
	}
	return full, nil
}

// isSafeFilename reports whether name is a single, ordinary filename — no path
// separators, no "."/"..", no control characters, and none of the characters
// Windows forbids in a name (<>:"/\|?*). Used to keep user-supplied names
// (e.g. batch-rename patterns) from turning into directory traversal.
func isSafeFilename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if name != filepath.Base(name) {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"|?*`, r) {
			return false
		}
	}
	return true
}

func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		next(w, r)
	}
}

// recoverMW catches any panic in a handler so one bad request can't take down
// the connection silently — it logs and returns a 500 instead.
func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[PANIC] %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func browseHandler(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	dirEntries, err := os.ReadDir(full)
	if err != nil {
		http.Error(w, "cannot read directory", http.StatusInternalServerError)
		return
	}

	var result []Entry
	// Names to always hide (case-insensitive)
	trashRelName  := strings.ToLower(filepath.Base(trashDir))
	uploadRelName := strings.ToLower(uploadDir)

	for _, e := range dirEntries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			lower := strings.ToLower(name)
			// Always hide trash and upload inbox folders at the library root.
			if rel == "" && (lower == trashRelName || lower == uploadRelName) {
				continue
			}
			// Hide housekeeping folders (e.g. "thumbs") wherever they appear.
			if hiddenFolderNames[lower] {
				continue
			}
		}
		entryRel := filepath.ToSlash(filepath.Join(rel, name))
		if rel == "" {
			entryRel = name
		}
		if e.IsDir() {
			result = append(result, Entry{Name: name, Path: entryRel, IsDir: true})
		} else if isImage(name) || isVideo(name) {
			info, _ := e.Info()
			var size int64
			if info != nil {
				size = info.Size()
			}
			var mod int64
			if info != nil {
				mod = info.ModTime().Unix()
			}
			result = append(result, Entry{Name: name, Path: entryRel, IsVideo: isVideo(name), Size: size, Mod: mod})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

type FolderInfo struct {
	PhotoCount    int      `json:"photoCount"`
	VideoCount    int      `json:"videoCount"`
	FolderCount   int      `json:"folderCount"`
	TotalSize     int64    `json:"totalSize"`
	FirstImage    string   `json:"firstImage,omitempty"`
	PreviewImages []string `json:"previewImages,omitempty"`
}

type FileMeta struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Modified  string `json:"modified"`
	Taken     string `json:"taken,omitempty"` // EXIF DateTimeOriginal
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Duration  string `json:"duration,omitempty"`
}

type SearchEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Parent  string `json:"parent"`
	IsDir   bool   `json:"isDir"`
	IsVideo bool   `json:"isVideo,omitempty"`
}

// GET /api/settings — returns current config (admin only)
// POST /api/settings — saves new config and restarts
func settingsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		// Never send the password/hash to the client; the field stays blank and
		// is only written if the admin types a new one.
		cfg := AppConfig{
			PhotoDir: baseDir, Port: port,
			ShareName: shareName, ServerIP: serverIPFlag,
			UploadFolder: uploadDir, FfmpegPath: ffmpegFlag, HTTPOnly: httpOnly, AutoSort: autoSort,
			DisableWebUI: !lanAccess, FacesEnabled: facesOn,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			AppConfig
			UsingDefaultPassword bool `json:"usingDefaultPassword"`
		}{cfg, hasDefaultAdminPassword()})
		return
	}
	if r.Method == http.MethodPost {
		var cfg AppConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// The photo directory must already exist — never auto-create it. Reject
		// the save (so the app doesn't restart into a crash) if the path is wrong.
		if strings.TrimSpace(cfg.PhotoDir) == "" {
			http.Error(w, "photo directory is required", http.StatusBadRequest)
			return
		}
		if fi, err := os.Stat(cfg.PhotoDir); err != nil || !fi.IsDir() {
			http.Error(w, "photo directory does not exist: "+cfg.PhotoDir, http.StatusBadRequest)
			return
		}
		// Accounts are managed via /api/users, not here — preserve them so a
		// settings save can never wipe the user list or lock anyone out.
		cfg.AdminPass = ""
		cfg.AdminPassHash = ""
		usersMu.Lock()
		cfg.Users = append([]User(nil), users...)
		usersMu.Unlock()
		cfg.GuestAccess = guestAccess
		if err := saveConfig(configFilePath, cfg); err != nil {
			http.Error(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[SETTINGS] Config saved — restarting")
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(600 * time.Millisecond)
			restartProcess()
		}()
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// appVersion is the running build's version — must match client APP_VERSION.
const appVersion = "2.10.0"

// updateRepo is the GitHub "owner/repo" releases are published under, used by
// the in-app "Check for updates" feature.
const updateRepo = "jpinela24/Photoshare"

// GET /api/onboarding-status — {needsSetup} is true when no photo library
// path has been configured yet (first run on a fresh Windows install).
// While setup is incomplete this is restricted to local requests (see
// isLocalRequest) so a remote LAN device can't even learn that the server is
// unclaimed; once setup is done there's nothing sensitive left to expose.
func onboardingStatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if baseDir == "" && !isLocalRequest(r) {
		json.NewEncoder(w).Encode(map[string]bool{"needsSetup": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"needsSetup": baseDir == ""})
}

// POST /api/onboarding {photoDir, username, password} — first-run setup: only
// works while no library path is configured. Sets the library path, renames
// the seeded default admin account to the chosen credentials, and restarts.
// Restricted to local requests (see isLocalRequest) so a remote device on
// the same network can't race to claim the admin account before the person
// sitting at the machine finishes setup.
func onboardingHandler(w http.ResponseWriter, r *http.Request) {
	if baseDir != "" {
		http.Error(w, "setup already completed", http.StatusConflict)
		return
	}
	if !isLocalRequest(r) {
		http.Error(w, "setup can only be completed from the machine running PhotoShare", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PhotoDir string `json:"photoDir"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body.PhotoDir = strings.TrimSpace(body.PhotoDir)
	body.Username = strings.TrimSpace(body.Username)
	if body.PhotoDir == "" {
		http.Error(w, "photo directory is required", http.StatusBadRequest)
		return
	}
	if fi, err := os.Stat(body.PhotoDir); err != nil || !fi.IsDir() {
		http.Error(w, "photo directory does not exist: "+body.PhotoDir, http.StatusBadRequest)
		return
	}
	if body.Username == "" || body.Password == "" {
		http.Error(w, "username and password are required", http.StatusBadRequest)
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		http.Error(w, "failed to hash password", http.StatusInternalServerError)
		return
	}
	cfg := loadConfig(configFilePath)
	cfg.PhotoDir = body.PhotoDir
	usersMu.Lock()
	users = []User{{Username: body.Username, PassHash: hash, Role: "admin"}}
	cfg.Users = append([]User(nil), users...)
	usersMu.Unlock()
	cfg.AdminPass = ""
	cfg.AdminPassHash = ""
	if err := saveConfig(configFilePath, cfg); err != nil {
		http.Error(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[ONBOARDING] Library set to %s — restarting", body.PhotoDir)
	w.WriteHeader(http.StatusOK)
	go func() {
		time.Sleep(600 * time.Millisecond)
		restartProcess()
	}()
}

// GET /api/platform — lets the UI conditionally show Windows-only controls.
func platformHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"windows": runtime.GOOS == "windows",
		"version": appVersion,
	})
}

// FSEntry is a directory entry for the unrestricted filesystem browser used
// to pick the library root itself (distinct from browseHandler, which is
// scoped to inside the already-configured library).
type FSEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Kind  string `json:"kind,omitempty"` // "quick" | "drive" (roots only)
}

// GET /api/fs/roots — top-level roots to start browsing from (drive letters
// on Windows, "/" + home directory elsewhere).
func fsRootsHandler(w http.ResponseWriter, r *http.Request) {
	var roots []FSEntry

	// Quick-access known folders (Windows-style: Desktop, Downloads, …) — only
	// those that actually exist. On Windows these may be OneDrive-redirected,
	// so each is also looked up under %OneDrive% as a fallback.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		oneDrive := os.Getenv("OneDrive")
		known := []struct {
			label string
			subs  []string
		}{
			{"Desktop", []string{"Desktop"}},
			{"Downloads", []string{"Downloads"}},
			{"Documents", []string{"Documents"}},
			{"Pictures", []string{"Pictures"}},
			{"Videos", []string{"Videos", "Movies"}},
			{"Music", []string{"Music"}},
		}
		isDir := func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
		for _, k := range known {
			found := ""
			for _, sub := range k.subs {
				if p := filepath.Join(home, sub); isDir(p) {
					found = p
					break
				}
			}
			if found == "" && oneDrive != "" {
				for _, sub := range k.subs {
					if p := filepath.Join(oneDrive, sub); isDir(p) {
						found = p
						break
					}
				}
			}
			if found != "" {
				roots = append(roots, FSEntry{Name: k.label, Path: found, IsDir: true, Kind: "quick"})
			}
		}
		roots = append(roots, FSEntry{Name: "Home", Path: home, IsDir: true, Kind: "quick"})
	}

	// Drives / filesystem roots.
	if runtime.GOOS == "windows" {
		for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
			drive := string(letter) + ":\\"
			if _, err := os.Stat(drive); err == nil {
				roots = append(roots, FSEntry{Name: drive, Path: drive, IsDir: true, Kind: "drive"})
			}
		}
	} else {
		roots = append(roots, FSEntry{Name: "/", Path: "/", IsDir: true, Kind: "drive"})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(roots)
}

// GET /api/fs/browse?path= — lists any directory the OS user can read, for
// picking the library root on first run or from Settings.
func fsBrowseHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, "cannot read directory: "+err.Error(), http.StatusBadRequest)
		return
	}
	var result []FSEntry
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		result = append(result, FSEntry{Name: e.Name(), Path: filepath.Join(path, e.Name()), IsDir: true})
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// GET/POST /api/autostart — Windows-only: toggle launching PhotoShare at login.
func autostartHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if runtime.GOOS != "windows" {
		http.Error(w, "autostart is only supported on Windows", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"enabled": autostartEnabled()})
	case http.MethodPost:
		var body struct{ Enabled bool `json:"enabled"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := setAutostart(body.Enabled); err != nil {
			http.Error(w, "failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// POST /api/show — restores/focuses the native window (tray "Open" item).
// Local-only + POST: it's invoked by the on-machine tray, never by a remote
// client, so there's no reason to expose it across the network.
func showHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !isLocalRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	showWindow()
	w.Write([]byte("ok"))
}

// POST /api/quit — gracefully exits (tray "Quit" item). Local-only + POST:
// stopping the server is a same-machine action; without this gate any client
// that can reach the app could shut it down (a trivial DoS).
func quitHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !isLocalRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Write([]byte("bye"))
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}()
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// GET /api/update/check — compares the running version against the latest
// GitHub release and returns whether an update is available.
func updateCheckHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	rel, err := latestRelease()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	json.NewEncoder(w).Encode(map[string]any{
		"current":     appVersion,
		"latest":      latest,
		"available":   latest != "" && latest != appVersion,
		"releaseURL":  rel.HTMLURL,
		"downloadURL": installerAssetURL(rel),
	})
}

// POST /api/update/run — downloads the latest installer and launches it
// (Windows only); the installer takes over and this process exits.
func updateRunHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if runtime.GOOS != "windows" {
		http.Error(w, "in-app updates are only supported on Windows", http.StatusNotImplemented)
		return
	}
	rel, err := latestRelease()
	if err != nil {
		http.Error(w, "update check failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	url := installerAssetURL(rel)
	if url == "" {
		http.Error(w, "no installer asset found in latest release", http.StatusInternalServerError)
		return
	}
	if err := downloadAndRunInstaller(url); err != nil {
		http.Error(w, "update failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

func latestRelease() (*githubRelease, error) {
	resp, err := http.Get("https://api.github.com/repos/" + updateRepo + "/releases/latest")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github returned %d", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// installerAssetURL picks the .exe installer matching the running CPU
// architecture. Releases ship two: PhotoShareSetup.exe (x64) and
// PhotoShareSetup-arm64.exe — the ARM one is named with "arm64", the x64 one
// isn't, so we match on that. Falls back to any .exe if there's no exact
// arch match (e.g. an older release that only had the x64 build).
func installerAssetURL(rel *githubRelease) string {
	if rel == nil {
		return ""
	}
	wantArm := runtime.GOARCH == "arm64"
	var fallback string
	for _, a := range rel.Assets {
		n := strings.ToLower(a.Name)
		if !strings.HasSuffix(n, ".exe") {
			continue
		}
		if strings.Contains(n, "arm64") == wantArm {
			return a.BrowserDownloadURL // exact arch match
		}
		if fallback == "" {
			fallback = a.BrowserDownloadURL
		}
	}
	return fallback
}

func downloadAndRunInstaller(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}
	out := filepath.Join(os.TempDir(), "photoshare-update-installer.exe")
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()
	cmd := exec.Command(out)
	return cmd.Start()
}

// GET /api/server-info — returns server IP, share name and shareable URL
func serverInfoHandler(w http.ResponseWriter, r *http.Request) {
	ip := serverIP()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"ip":           ip,
		"shareName":    shareName,
		"uploadFolder": uploadDir,
		"url":          netURL,
	})
}

// GET /api/qr — PNG QR code encoding the shareable LAN URL
func qrHandler(w http.ResponseWriter, r *http.Request) {
	target := netURL
	if target == "" { // safety fallback if computed before startup finished
		target = "http://" + serverIP() + ":" + port
	}
	png, err := qrcode.Encode(target, qrcode.Medium, 320)
	if err != nil {
		http.Error(w, "qr error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

// virtualIfaceKeywords match adapter names for VPN / virtual / tunnel NICs that
// should not be picked as the LAN address.
var virtualIfaceKeywords = []string{
	"vpn", "tap", "tunnel", "virtual", "vethernet", "hamachi",
	"openvpn", "wireguard", "vmware", "virtualbox", "hyper-v", "loopback",
}

func isVirtualIface(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range virtualIfaceKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func serverIP() string {
	if serverIPFlag != "" {
		return serverIPFlag
	}
	// First pass prefers real LAN adapters; second pass accepts any non-loopback
	// IPv4 so we never regress to "localhost" when only virtual NICs are present.
	for _, skipVirtual := range []bool{true, false} {
		ifaces, _ := net.Interfaces()
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			if skipVirtual && isVirtualIface(iface.Name) {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() {
					return ip4.String()
				}
			}
		}
	}
	return "localhost"
}

// ── Recycle Bin ───────────────────────────────────────────────────────────────

type TrashEntry struct {
	Name         string `json:"name"`
	OriginalPath string `json:"originalPath"`
	DeletedAt    string `json:"deletedAt"`
	Size         int64  `json:"size"`
	IsImage      bool   `json:"isImage"`
	IsVideo      bool   `json:"isVideo"`
}

func readTrashInfo(trashFile string) (original, deletedAt string) {
	data, err := os.ReadFile(trashFile + ".trashinfo")
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "original: ") {
			original = strings.TrimSpace(strings.TrimPrefix(line, "original: "))
		}
		if strings.HasPrefix(line, "deleted:  ") {
			deletedAt = strings.TrimSpace(strings.TrimPrefix(line, "deleted:  "))
		}
	}
	return
}

// GET /api/trash
func trashListHandler(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	var result []TrashEntry
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".trashinfo") {
			continue
		}
		original, deletedAt := readTrashInfo(filepath.Join(trashDir, e.Name()))
		info, _ := e.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		result = append(result, TrashEntry{
			Name:         e.Name(),
			OriginalPath: original,
			DeletedAt:    deletedAt,
			Size:         size,
			IsImage:      isImage(e.Name()),
			IsVideo:      isVideo(e.Name()),
		})
	}
	if result == nil {
		result = []TrashEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// GET /api/trash/thumb?file=  — thumbnail for a file in the trash dir
func trashThumbHandler(w http.ResponseWriter, r *http.Request) {
	file := filepath.Base(r.URL.Query().Get("file")) // base only — no path traversal
	full := filepath.Join(trashDir, file)
	if _, err := os.Stat(full); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(full+"_trash")))
	thumbPath := filepath.Join(thumbDir, hash+".jpg")

	if _, err := os.Stat(thumbPath); err == nil {
		http.ServeFile(w, r, thumbPath)
		return
	}

	if isVideo(file) {
		ff := ffmpegPath()
		if ff == "" {
			http.Error(w, "ffmpeg not found", http.StatusNotImplemented)
			return
		}
		args := []string{"-ss", "00:00:01", "-i", full,
			"-vframes", "1",
			"-vf", "scale=300:300:force_original_aspect_ratio=increase,crop=300:300",
			"-q:v", "3", "-y", thumbPath}
		hideCmd(exec.Command(ff, args...)).CombinedOutput()
		// If thumbnail wasn't created (video shorter than 1s), grab frame 0
		if _, err := os.Stat(thumbPath); err != nil {
			args = []string{"-i", full,
				"-vframes", "1",
				"-vf", "scale=300:300:force_original_aspect_ratio=increase,crop=300:300",
				"-q:v", "3", "-y", thumbPath}
			hideCmd(exec.Command(ff, args...)).CombinedOutput()
		}
	} else if isHeic(file) {
		// HEIC: decode via libheif, then resize
		tmp := thumbPath + ".src.jpg"
		if err := heicToJPEG(full, tmp); err == nil {
			if img, err := imaging.Open(tmp, imaging.AutoOrientation(true)); err == nil {
				imaging.Save(imaging.Thumbnail(img, 300, 300, imaging.Lanczos), thumbPath)
			}
			os.Remove(tmp)
		}
	} else if isImage(file) {
		img, err := imaging.Open(full, imaging.AutoOrientation(true))
		if err == nil {
			thumb := imaging.Thumbnail(img, 300, 300, imaging.Lanczos)
			imaging.Save(thumb, thumbPath)
		}
	}

	if _, err := os.Stat(thumbPath); err == nil {
		http.ServeFile(w, r, thumbPath)
	} else {
		http.Error(w, "cannot generate thumbnail", http.StatusInternalServerError)
	}
}

// POST /api/trash/restore  body: {"name":"...","originalPath":"..."}
func trashRestoreHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Name         string `json:"name"`
		OriginalPath string `json:"originalPath"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	src := filepath.Join(trashDir, filepath.Base(body.Name))
	if _, err := os.Stat(src); err != nil {
		http.Error(w, "file not found in trash", http.StatusNotFound)
		return
	}

	// Determine destination — always resolve through safePath so a crafted
	// originalPath (e.g. "../../etc/cron.d/x") can't restore a file outside
	// the photo library.
	var dest string
	if body.OriginalPath != "" {
		d, err := safePath(baseDir, filepath.FromSlash(body.OriginalPath))
		if err != nil {
			http.Error(w, "invalid restore path", http.StatusBadRequest)
			return
		}
		dest = d
	} else {
		// Strip timestamp prefix (e.g. 20240603_141523_filename.jpg → filename.jpg)
		name := body.Name
		parts := strings.SplitN(name, "_", 3)
		if len(parts) == 3 {
			name = parts[2]
		}
		dest = filepath.Join(baseDir, filepath.Base(name))
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		http.Error(w, "cannot create destination folder: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Handle name conflict
	if _, err := os.Stat(dest); err == nil {
		ext := filepath.Ext(dest)
		base := strings.TrimSuffix(filepath.Base(dest), ext)
		dest = filepath.Join(filepath.Dir(dest), fmt.Sprintf("%s_restored%s", base, ext))
	}

	if err := os.Rename(src, dest); err != nil {
		if err2 := copyFile(src, dest); err2 != nil {
			http.Error(w, "restore failed: "+err2.Error(), http.StatusInternalServerError)
			return
		}
		os.Remove(src)
	}
	os.Remove(src + ".trashinfo")
	log.Printf("[TRASH] restored: %s → %s", src, dest)
	w.WriteHeader(http.StatusOK)
}

// DELETE /api/trash/purge?file=  — permanently delete one item
func trashPurgeHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	file := filepath.Base(r.URL.Query().Get("file"))
	if file == "" || file == "." {
		http.Error(w, "invalid file", http.StatusBadRequest)
		return
	}
	full := filepath.Join(trashDir, file)
	os.RemoveAll(full)
	os.Remove(full + ".trashinfo")
	w.WriteHeader(http.StatusOK)
}

// DELETE /api/trash/purge-all — empty the entire trash
func trashPurgeAllHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	entries, _ := os.ReadDir(trashDir)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(trashDir, e.Name()))
	}
	w.WriteHeader(http.StatusOK)
}

// maxUploadBytes caps the total size of a single upload request (form
// overhead + all files combined). ParseMultipartForm's own size argument
// only bounds how much of a non-file field is buffered in memory — it does
// NOT cap how much gets streamed to temp files on disk, so without this any
// signed-in user (any role, including a guest if guest access is enabled)
// could keep uploading arbitrarily large files and exhaust disk space.
const maxUploadBytes = 10 << 30 // 10 GiB per request

// POST /api/inbox-upload — upload to the public uploads inbox (requires a
// logged-in session of any role, see the protected() wrapper at registration)
func inboxUploadHandler(w http.ResponseWriter, r *http.Request) {
	destDir := filepath.Join(baseDir, uploadDir)
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		http.Error(w, "upload too large or malformed: "+err.Error(), http.StatusBadRequest)
		return
	}
	uploaded := 0
	var skipped []string
	for _, fh := range r.MultipartForm.File["files"] {
		// Preserve the original filename case; isImage/isVideo lowercase the
		// extension themselves, so no need to flatten the whole name.
		name := filepath.Base(fh.Filename)
		if !isImage(name) && !isVideo(name) {
			skipped = append(skipped, name+" (not a photo or video)")
			continue
		}
		src, err := fh.Open()
		if err != nil {
			continue
		}
		dest := filepath.Join(destDir, name)
		if _, err := os.Stat(dest); err == nil {
			ext := filepath.Ext(dest)
			base := strings.TrimSuffix(filepath.Base(dest), ext)
			dest = filepath.Join(destDir, fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext))
		}
		out, err := os.Create(dest)
		if err != nil {
			src.Close()
			continue
		}
		io.Copy(out, src)
		out.Close()
		src.Close()
		uploaded++
		log.Printf("[INBOX] %s", fh.Filename)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"uploaded": uploaded, "skipped": skipped})
}

// POST /api/upload?path=folder — upload one or more files into a folder
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	rel := r.URL.Query().Get("path")
	destDir, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		http.Error(w, "upload too large or malformed: "+err.Error(), http.StatusBadRequest)
		return
	}
	uploaded := 0
	var errs []string
	for _, fh := range r.MultipartForm.File["files"] {
		src, err := fh.Open()
		if err != nil {
			errs = append(errs, fh.Filename+": open error")
			continue
		}
		dest := filepath.Join(destDir, filepath.Base(fh.Filename))
		if _, err := os.Stat(dest); err == nil {
			ext := filepath.Ext(dest)
			base := strings.TrimSuffix(filepath.Base(dest), ext)
			dest = filepath.Join(destDir, fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext))
		}
		out, err := os.Create(dest)
		if err != nil {
			src.Close()
			errs = append(errs, fh.Filename+": create error")
			continue
		}
		io.Copy(out, src)
		out.Close()
		src.Close()
		uploaded++
		log.Printf("[UPLOAD] %s → %s", fh.Filename, dest)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"uploaded": uploaded, "errors": errs})
}

// GET /manifest.json — PWA web app manifest
func manifestHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	json.NewEncoder(w).Encode(map[string]any{
		"name":             "PhotoShare",
		"short_name":       "PhotoShare",
		"description":      "Home network photo library",
		"start_url":        "/",
		"display":          "standalone",
		"background_color": "#0c0c0e",
		"theme_color":      "#6366f1",
		"icons": []map[string]string{
			{"src": "/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any maskable"},
			{"src": "/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any maskable"},
		},
	})
}

// appIcon renders the PhotoShare camera glyph at the given size — shared by
// the PWA manifest icons and (on Windows) the tray/taskbar icon.
func appIcon(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	bg     := color.NRGBA{99, 102, 241, 255}  // indigo
	accent := color.NRGBA{165, 180, 252, 255} // light indigo
	white  := color.NRGBA{255, 255, 255, 255}

	// Fill background
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetNRGBA(x, y, bg)
		}
	}
	// Camera body
	m := size / 16
	for y := m * 5; y < m*11; y++ {
		for x := m * 2; x < m*14; x++ {
			img.SetNRGBA(x, y, accent)
		}
	}
	// Viewfinder bump
	for y := m * 3; y < m*6; y++ {
		for x := m * 5; x < m*11; x++ {
			img.SetNRGBA(x, y, accent)
		}
	}
	// Lens outer (white circle)
	cx, cy, r := size/2, size/2+m/2, size/5
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
				img.SetNRGBA(x, y, white)
			}
		}
	}
	// Lens inner (bg circle)
	r2 := r * 3 / 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r2*r2 {
				img.SetNRGBA(x, y, bg)
			}
		}
	}
	return img
}

var (
	logoOnce   sync.Once
	logoMaster image.Image
)

// loadLogoMaster decodes the embedded brand logo once. The transparent PNG
// lives at client/public/logo.png and is bundled into client/dist by Vite,
// so it's already part of the embedded client FS. Returns nil if it's somehow
// missing, letting callers fall back to the drawn glyph.
func loadLogoMaster() image.Image {
	logoOnce.Do(func() {
		data, err := clientDist.ReadFile("client/dist/logo.png")
		if err != nil {
			return
		}
		if img, err := png.Decode(bytes.NewReader(data)); err == nil {
			logoMaster = img
		}
	})
	return logoMaster
}

// brandIcon returns the PhotoShare logo resized to size×size, falling back to
// the drawn glyph (appIcon) if the embedded logo can't be loaded.
func brandIcon(size int) image.Image {
	if m := loadLogoMaster(); m != nil {
		return imaging.Resize(m, size, size, imaging.Lanczos)
	}
	return appIcon(size)
}

// appIconPNG renders the brand icon to PNG bytes — used by the Windows
// tray/window icon, which needs a file on disk rather than an HTTP response.
func appIconPNG(size int) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, brandIcon(size))
	return buf.Bytes()
}

// servePWAIcon serves the brand icon (PWA manifest / apple-touch) at the size.
func servePWAIcon(w http.ResponseWriter, size int) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	png.Encode(w, brandIcon(size))
}

// GET /api/open-folder?path=
func openFolderHandler(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	var smbURL string
	if shareName != "" {
		subPath := strings.TrimPrefix(full, baseDir)
		ip := serverIP()
		// SMB:  smb://192.168.1.82/memories/Vacation
		smbURL = "smb://" + ip + "/" + shareName + strings.ReplaceAll(subPath, `\`, "/")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"fullPath": full,
		"smbURL":   smbURL,
	})
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	q        := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	typeFilter := r.URL.Query().Get("type") // "image" | "video" | ""
	fromStr  := r.URL.Query().Get("from")  // YYYY-MM-DD
	toStr    := r.URL.Query().Get("to")

	var fromTime, toTime time.Time
	if fromStr != "" { fromTime, _ = time.Parse("2006-01-02", fromStr) }
	if toStr   != "" { toTime, _   = time.Parse("2006-01-02", toStr); toTime = toTime.Add(24*time.Hour) }

	if q == "" && typeFilter == "" && fromStr == "" && toStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}

	var results []SearchEntry
	filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if q != "" && !strings.Contains(strings.ToLower(name), q) {
			return nil
		}
		// Type filter
		if typeFilter == "image" && !isImage(name) { return nil }
		if typeFilter == "video" && !isVideo(name)  { return nil }
		// Date range filter
		if !fromTime.IsZero() && info.ModTime().Before(fromTime) { return nil }
		if !toTime.IsZero()   && info.ModTime().After(toTime)    { return nil }
		rel, _ := filepath.Rel(baseDir, path)
		rel = filepath.ToSlash(rel)
		parent := filepath.ToSlash(filepath.Dir(rel))
		if parent == "." {
			parent = ""
		}
		if info.IsDir() {
			results = append(results, SearchEntry{Name: name, Path: rel, Parent: parent, IsDir: true})
		} else if isImage(name) || isVideo(name) {
			results = append(results, SearchEntry{Name: name, Path: rel, Parent: parent, IsVideo: isVideo(name)})
		}
		if len(results) >= 60 {
			return filepath.SkipAll
		}
		return nil
	})

	if results == nil {
		results = []SearchEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func metaHandler(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	fi, err := os.Stat(full)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	meta := FileMeta{
		Name:     fi.Name(),
		Size:     fi.Size(),
		Modified: fi.ModTime().Format("Jan 2, 2006"),
	}

	name := strings.ToLower(fi.Name())

	if isImage(name) {
		if isHeic(name) {
			// Use ffprobe for HEIC dimensions
			if ff := ffmpegPath(); ff != "" {
				ffprobe := strings.Replace(ff, "ffmpeg", "ffprobe", 1)
				if _, err := os.Stat(ffprobe); err == nil {
					out, err := hideCmd(exec.Command(ffprobe,
						"-v", "quiet", "-select_streams", "v:0",
						"-show_entries", "stream=width,height",
						"-of", "csv=p=0", full,
					)).Output()
					if err == nil {
						parts := strings.Split(strings.TrimSpace(string(out)), ",")
						if len(parts) == 2 {
							meta.Width, _ = strconv.Atoi(parts[0])
							meta.Height, _ = strconv.Atoi(parts[1])
						}
					}
				}
			}
		} else {
			// Fast decode — only reads header
			if f, err := os.Open(full); err == nil {
				if cfg, _, err := image.DecodeConfig(f); err == nil {
					meta.Width = cfg.Width
					meta.Height = cfg.Height
				}
				f.Close()
			}
			// EXIF date taken
			if f, err := os.Open(full); err == nil {
				if x, err := exif.Decode(f); err == nil {
					if t, err := x.DateTime(); err == nil {
						meta.Taken = t.Format("Jan 2, 2006 3:04 PM")
					}
				}
				f.Close()
			}
		}
	} else if isVideo(name) {
		// Get duration via ffprobe if available
		if ff := ffmpegPath(); ff != "" {
			ffprobe := strings.Replace(ff, "ffmpeg", "ffprobe", 1)
			if _, err := os.Stat(ffprobe); err == nil {
				out, err := hideCmd(exec.Command(ffprobe,
					"-v", "quiet",
					"-show_entries", "format=duration",
					"-of", "csv=p=0",
					full,
				)).Output()
				if err == nil {
					secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
					if err == nil {
						d := time.Duration(secs) * time.Second
						h := int(d.Hours())
						m := int(d.Minutes()) % 60
						s := int(d.Seconds()) % 60
						if h > 0 {
							meta.Duration = fmt.Sprintf("%d:%02d:%02d", h, m, s)
						} else {
							meta.Duration = fmt.Sprintf("%d:%02d", m, s)
						}
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

func folderInfoHandler(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	dirEntries, err := os.ReadDir(full)
	if err != nil {
		http.Error(w, "cannot read directory", http.StatusInternalServerError)
		return
	}

	var info FolderInfo
	for _, e := range dirEntries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		entryRel := name
		if rel != "" {
			entryRel = filepath.ToSlash(filepath.Join(rel, name))
		}
		if e.IsDir() {
			info.FolderCount++
		} else if isImage(name) {
			fi, _ := e.Info()
			if fi != nil {
				info.TotalSize += fi.Size()
			}
			info.PhotoCount++
			if info.FirstImage == "" {
				info.FirstImage = entryRel
			}
			if len(info.PreviewImages) < 4 {
				info.PreviewImages = append(info.PreviewImages, entryRel)
			}
		} else if isVideo(name) {
			fi, _ := e.Info()
			if fi != nil {
				info.TotalSize += fi.Size()
			}
			info.VideoCount++
			// Use video frame as preview if we still need images
			if len(info.PreviewImages) < 4 {
				info.PreviewImages = append(info.PreviewImages, entryRel)
			}
		}
	}

	// Scan one level deeper if no previews found in direct children
	if len(info.PreviewImages) == 0 {
		for _, e := range dirEntries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			subRel := e.Name()
			if rel != "" {
				subRel = filepath.ToSlash(filepath.Join(rel, e.Name()))
			}
			subEntries, _ := os.ReadDir(filepath.Join(full, e.Name()))
			for _, se := range subEntries {
				sn := se.Name()
				if strings.HasPrefix(sn, ".") || se.IsDir() {
					continue
				}
				if isImage(sn) || isVideo(sn) {
					imgRel := filepath.ToSlash(filepath.Join(subRel, sn))
					if info.FirstImage == "" && isImage(sn) {
						info.FirstImage = imgRel
					}
					info.PreviewImages = append(info.PreviewImages, imgRel)
					if len(info.PreviewImages) >= 4 {
						break
					}
				}
			}
			if len(info.PreviewImages) >= 4 {
				break
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// ffmpegPath finds the ffmpeg binary (called once at startup, result cached in cachedFFmpeg)
func ffmpegPath() string {
	if cachedFFmpeg != "" {
		return cachedFFmpeg
	}
	// 1. Explicit override flag
	if ffmpegFlag != "" {
		return ffmpegFlag
	}
	// 2. System PATH
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	// 3. Common install locations
	candidates := []string{
		"/usr/bin/ffmpeg",
		"/usr/local/bin/ffmpeg",
		"/opt/ffmpeg/bin/ffmpeg",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	log.Printf("WARNING: ffmpeg not found — video/HEIC thumbnails disabled. Use -ffmpeg-path to set location.")
	return ""
}

// ── HEIC decoding via libheif (most ffmpeg builds can't decode HEIC) ──────────

var cachedHeif string

func heifConvertPath() string {
	if cachedHeif != "" {
		return cachedHeif
	}
	if p, err := exec.LookPath("heif-convert"); err == nil {
		cachedHeif = p
	}
	return cachedHeif
}

// heicToJPEG decodes a HEIC/HEIF file to a full-size JPEG at dst using libheif's
// heif-convert. Returns an error if heif-convert is missing or fails.
func heicToJPEG(src, dst string) error {
	hc := heifConvertPath()
	if hc == "" {
		return fmt.Errorf("heif-convert not found (install libheif)")
	}
	if out, err := hideCmd(exec.Command(hc, "-q", "90", src, dst)).CombinedOutput(); err != nil {
		return fmt.Errorf("heif-convert failed: %v: %s", err, out)
	}
	if _, err := os.Stat(dst); err != nil {
		return fmt.Errorf("heif-convert produced no output")
	}
	return nil
}

// ffprobePath derives the ffprobe path from the resolved ffmpeg path.
func ffprobePath() string {
	ff := ffmpegPath()
	if ff == "" {
		return ""
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return strings.Replace(ff, "ffmpeg", "ffprobe", 1)
}

// videoCodec returns the codec_name of a video's first video stream (e.g.
// "h264", "hevc"), or "" if it can't be determined.
func videoCodec(path string) string {
	fp := ffprobePath()
	if fp == "" {
		return ""
	}
	out, err := hideCmd(exec.Command(fp, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path)).Output()
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(out)))
}

// transcodeLocks serializes transcodes per output file so two concurrent plays
// of the same video don't both run ffmpeg.
var (
	transcodeMu    sync.Mutex
	transcodeLocks = map[string]*sync.Mutex{}
)

func transcodeLock(key string) *sync.Mutex {
	transcodeMu.Lock()
	defer transcodeMu.Unlock()
	m, ok := transcodeLocks[key]
	if !ok {
		m = &sync.Mutex{}
		transcodeLocks[key] = m
	}
	return m
}

// transcodeToCache converts src to a browser-playable H.264/AAC MP4 at cachePath
// (with +faststart so it streams and seeks properly). It is CPU-capped (2
// threads) and scaled to ≤1280px so a weak homelab CPU stays responsive instead
// of freezing the way an unbounded full-resolution encode did. Concurrent
// requests for the same file are serialized; later callers find the cache ready.
//
// We deliberately do NOT stream a live/fragmented transcode: a fragmented MP4
// served as a plain <video src> doesn't play reliably (it needs MSE), so we
// produce a complete faststart file and serve it with http.ServeFile (Range).
func transcodeToCache(src, cachePath string) error {
	ff := ffmpegPath()
	if ff == "" {
		return fmt.Errorf("ffmpeg not found")
	}

	lk := transcodeLock(cachePath)
	lk.Lock()
	defer lk.Unlock()

	// Another request may have finished it while we waited for the lock.
	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}

	tmp := cachePath + ".part.mp4"
	args := []string{"-i", src,
		"-vf", "scale='min(1280,iw)':-2",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "26",
		"-threads", "2",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart", "-y", tmp}
	if out, err := hideCmd(exec.Command(ff, args...)).CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("transcode failed: %v: %s", err, out)
	}
	return os.Rename(tmp, cachePath)
}

func thumbHandler(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	hash := fmt.Sprintf("%x", md5.Sum([]byte(full)))
	thumbPath := filepath.Join(thumbDir, hash+".jpg")

	// Serve from cache if available
	if _, err := os.Stat(thumbPath); err == nil {
		http.ServeFile(w, r, thumbPath)
		return
	}

	if isVideo(filepath.Base(full)) {
		// Generate video thumbnail with FFmpeg
		ff := ffmpegPath()
		if ff == "" {
			http.Error(w, "ffmpeg not found — install it to enable video thumbnails", http.StatusNotImplemented)
			return
		}
		args := []string{"-ss", "00:00:01", "-i", full,
			"-vframes", "1",
			"-vf", "scale=300:300:force_original_aspect_ratio=increase,crop=300:300",
			"-q:v", "3", "-y", thumbPath}
		hideCmd(exec.Command(ff, args...)).CombinedOutput()
		// Fallback to frame 0 if video shorter than 1s
		if _, ferr := os.Stat(thumbPath); ferr != nil {
			args = []string{"-i", full,
				"-vframes", "1",
				"-vf", "scale=300:300:force_original_aspect_ratio=increase,crop=300:300",
				"-q:v", "3", "-y", thumbPath}
			if out, err := hideCmd(exec.Command(ff, args...)).CombinedOutput(); err != nil {
				log.Printf("ffmpeg thumb error for %s: %v\n%s", full, err, out)
				http.Error(w, "failed to generate thumbnail", http.StatusInternalServerError)
				return
			}
		}
	} else if isHeic(filepath.Base(full)) {
		// HEIC: decode to a temp JPEG via libheif, then resize with imaging
		tmp := thumbPath + ".src.jpg"
		if err := heicToJPEG(full, tmp); err != nil {
			log.Printf("heic thumb error for %s: %v", full, err)
			http.Error(w, "failed to generate HEIC thumbnail", http.StatusInternalServerError)
			return
		}
		img, err := imaging.Open(tmp, imaging.AutoOrientation(true))
		os.Remove(tmp)
		if err != nil {
			http.Error(w, "cannot open converted HEIC", http.StatusInternalServerError)
			return
		}
		thumb := imaging.Thumbnail(img, 300, 300, imaging.Lanczos)
		if err := imaging.Save(thumb, thumbPath); err != nil {
			http.Error(w, "cannot save thumbnail", http.StatusInternalServerError)
			return
		}
	} else {
		// Generate image thumbnail
		img, err := imaging.Open(full, imaging.AutoOrientation(true))
		if err != nil {
			http.Error(w, "cannot open image", http.StatusInternalServerError)
			return
		}
		thumb := imaging.Thumbnail(img, 300, 300, imaging.Lanczos)
		if err := imaging.Save(thumb, thumbPath); err != nil {
			http.Error(w, "cannot save thumbnail", http.StatusInternalServerError)
			return
		}
	}

	http.ServeFile(w, r, thumbPath)
}

func photoHandler(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// HEIC: browsers can't display it — serve a converted JPEG instead
	if isHeic(filepath.Base(full)) {
		hash := fmt.Sprintf("%x", md5.Sum([]byte(full+"_display")))
		convPath := filepath.Join(thumbDir, hash+".jpg")

		if _, err := os.Stat(convPath); os.IsNotExist(err) {
			if err := heicToJPEG(full, convPath); err != nil {
				log.Printf("heic display convert error for %s: %v", full, err)
				http.Error(w, "failed to convert HEIC", http.StatusInternalServerError)
				return
			}
		}
		http.ServeFile(w, r, convPath)
		return
	}

	if isVideo(filepath.Base(full)) {
		// HEVC/H.265 (iPhone .MOV/.mp4): browsers can't decode it. Transcode to
		// H.264 once, cache it, then serve the cached MP4.
		if codec := videoCodec(full); codec == "hevc" || codec == "h265" {
			hash := fmt.Sprintf("%x", md5.Sum([]byte(full+"_h264")))
			convPath := filepath.Join(thumbDir, hash+".mp4")

			if _, err := os.Stat(convPath); os.IsNotExist(err) {
				if err := transcodeToCache(full, convPath); err != nil {
					log.Printf("video transcode error for %s: %v", full, err)
					http.Error(w, "failed to transcode video", http.StatusInternalServerError)
					return
				}
			}
			// Cached file is always H.264 MP4.
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeFile(w, r, convPath)
			return
		}

		// Otherwise serve the file as-is, but set the Content-Type explicitly:
		// the minimal container image has no /etc/mime.types, so Go can't infer
		// it from the extension and would fall back to a type browsers reject.
		w.Header().Set("Content-Type", videoMIME(full))
		http.ServeFile(w, r, full)
		return
	}

	http.ServeFile(w, r, full)
}

// videoMIME returns the MIME type for a video file based on its extension.
func videoMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".wmv":
		return "video/x-ms-wmv"
	case ".flv":
		return "video/x-flv"
	case ".ts":
		return "video/mp2t"
	default:
		return "video/mp4"
	}
}

// POST /api/admin/folder/create?path=parent&name=NewFolder
func adminCreateFolderHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	parent := r.URL.Query().Get("path")
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" || strings.ContainsAny(name, `/\:*?"<>|`) {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	full, err := safePath(baseDir, filepath.Join(parent, name))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(full, 0755); err != nil {
		http.Error(w, "could not create folder: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[ADMIN] created folder: %s", full)
	w.WriteHeader(http.StatusOK)
}

// POST /api/admin/folder/rename  body: {"path":"...","newName":"..."}
func adminRenameFolderHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body.NewName = strings.TrimSpace(body.NewName)
	if body.NewName == "" || strings.ContainsAny(body.NewName, `/\:*?"<>|`) {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	full, err := safePath(baseDir, body.Path)
	if err != nil || full == baseDir {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	newFull := filepath.Join(filepath.Dir(full), body.NewName)
	if _, err := os.Stat(newFull); err == nil {
		http.Error(w, "a folder with that name already exists", http.StatusConflict)
		return
	}
	if err := os.Rename(full, newFull); err != nil {
		http.Error(w, "rename failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[ADMIN] renamed folder: %s → %s", full, newFull)
	w.WriteHeader(http.StatusOK)
}

// DELETE /api/admin/folder/delete?path=  — only succeeds if folder is empty
func adminDeleteFolderHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := safePath(baseDir, rel)
	if err != nil || full == baseDir {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		http.Error(w, "could not read folder", http.StatusInternalServerError)
		return
	}
	// Filter out hidden files before deciding if empty
	visible := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			visible++
		}
	}
	if visible > 0 {
		http.Error(w, "folder is not empty — remove all files first", http.StatusConflict)
		return
	}
	if err := os.RemoveAll(full); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[ADMIN] deleted empty folder: %s", full)
	w.WriteHeader(http.StatusOK)
}

// ── Batch operations ──────────────────────────────────────────────────────────

type batchBody struct {
	Paths      []string `json:"paths"`
	DestFolder string   `json:"destFolder"`
}

// POST /api/admin/batch/delete  {"paths":[...]}
func adminBatchDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body batchBody
	json.NewDecoder(r.Body).Decode(&body)
	var errs []string
	for _, rel := range body.Paths {
		full, err := safePath(baseDir, rel)
		if err != nil {
			errs = append(errs, rel+": invalid path")
			continue
		}
		if err := moveToTrash(full, rel); err != nil {
			errs = append(errs, rel+": "+err.Error())
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"errors": errs})
}

// POST /api/admin/batch/copy  {"paths":[...],"destFolder":"..."}
func adminBatchCopyHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body batchBody
	json.NewDecoder(r.Body).Decode(&body)
	var destDir string
	var err error
	if body.DestFolder == "" {
		destDir = baseDir
	} else {
		destDir, err = safePath(baseDir, body.DestFolder)
		if err != nil {
			http.Error(w, "invalid destination", http.StatusBadRequest)
			return
		}
	}
	var errs []string
	for _, rel := range body.Paths {
		src, err := safePath(baseDir, rel)
		if err != nil {
			errs = append(errs, rel+": invalid path")
			continue
		}
		dest := filepath.Join(destDir, filepath.Base(src))
		if _, err := os.Stat(dest); err == nil {
			ext := filepath.Ext(dest)
			base := strings.TrimSuffix(filepath.Base(dest), ext)
			dest = filepath.Join(destDir, fmt.Sprintf("%s_copy%s", base, ext))
		}
		if err := copyFile(src, dest); err != nil {
			errs = append(errs, rel+": "+err.Error())
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"errors": errs})
}

// POST /api/admin/batch/move  {"paths":[...],"destFolder":"..."}
func adminBatchMoveHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body batchBody
	json.NewDecoder(r.Body).Decode(&body)
	var destDir string
	var err error
	if body.DestFolder == "" {
		destDir = baseDir
	} else {
		destDir, err = safePath(baseDir, body.DestFolder)
		if err != nil {
			http.Error(w, "invalid destination", http.StatusBadRequest)
			return
		}
	}
	var errs []string
	for _, rel := range body.Paths {
		src, err := safePath(baseDir, rel)
		if err != nil {
			errs = append(errs, rel+": invalid path")
			continue
		}
		if filepath.Dir(src) == destDir {
			continue // already there
		}
		dest := filepath.Join(destDir, filepath.Base(src))
		if _, err := os.Stat(dest); err == nil {
			ext := filepath.Ext(dest)
			base := strings.TrimSuffix(filepath.Base(dest), ext)
			dest = filepath.Join(destDir, fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext))
		}
		if err := os.Rename(src, dest); err != nil {
			if err2 := copyFile(src, dest); err2 == nil {
				os.Remove(src)
			} else {
				errs = append(errs, rel+": "+err2.Error())
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"errors": errs})
}

// POST /api/admin/batch/rename  {"paths":[...],"pattern":"Vacation_{n}","start":1,"padding":3}
func adminBatchRenameHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Paths   []string `json:"paths"`
		Pattern string   `json:"pattern"`
		Start   int      `json:"start"`
		Padding int      `json:"padding"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Padding < 1 {
		body.Padding = 3
	}
	var errs []string
	for i, rel := range body.Paths {
		full, err := safePath(baseDir, rel)
		if err != nil {
			errs = append(errs, rel+": invalid path")
			continue
		}
		ext := filepath.Ext(filepath.Base(full))
		origName := strings.TrimSuffix(filepath.Base(full), ext)
		n := body.Start + i
		numStr := fmt.Sprintf("%0*d", body.Padding, n)
		newName := strings.ReplaceAll(body.Pattern, "{n}", numStr)
		newName = strings.ReplaceAll(newName, "{name}", origName)
		newName += ext
		// The new name must be a single filename — never a path. Reject any
		// separator, "..", or characters illegal in a filename so a pattern
		// can't move files across directories or out of the library.
		if !isSafeFilename(newName) {
			errs = append(errs, rel+": invalid characters in new name")
			continue
		}
		newFull := filepath.Join(filepath.Dir(full), newName)
		// Belt-and-suspenders: confirm the result is still inside the library.
		if rel2, err := filepath.Rel(baseDir, newFull); err != nil || strings.HasPrefix(rel2, "..") {
			errs = append(errs, rel+": target escapes the library")
			continue
		}
		if _, err := os.Stat(newFull); err == nil {
			errs = append(errs, rel+": target name already exists")
			continue
		}
		if err := os.Rename(full, newFull); err != nil {
			errs = append(errs, rel+": "+err.Error())
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"errors": errs})
}

// GET /api/stats
type FolderStat struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Files int    `json:"files"`
}

type Stats struct {
	TotalSize    int64        `json:"totalSize"`
	TotalPhotos  int          `json:"totalPhotos"`
	TotalVideos  int          `json:"totalVideos"`
	TotalFolders int          `json:"totalFolders"`
	TopFolders   []FolderStat `json:"topFolders"`
	DiskTotal    uint64       `json:"diskTotal"`
	DiskFree     uint64       `json:"diskFree"`
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	var stats Stats
	folderSizes := map[string]*FolderStat{}

	filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		if info.IsDir() {
			if path != baseDir {
				stats.TotalFolders++
				rel, _ := filepath.Rel(baseDir, path)
				// Only count top-level folders
				if !strings.Contains(filepath.ToSlash(rel), "/") {
					folderSizes[rel] = &FolderStat{Path: filepath.ToSlash(rel)}
				}
			}
			return nil
		}
		name := info.Name()
		size := info.Size()
		stats.TotalSize += size
		if isImage(name) {
			stats.TotalPhotos++
		} else if isVideo(name) {
			stats.TotalVideos++
		}
		// Add to parent top-level folder
		rel, _ := filepath.Rel(baseDir, path)
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		if len(parts) > 1 {
			if fstat, ok := folderSizes[parts[0]]; ok {
				fstat.Size += size
				fstat.Files++
			}
		}
		return nil
	})

	// Sort top folders by size descending, take top 10
	folders := make([]FolderStat, 0, len(folderSizes))
	for _, fs := range folderSizes {
		folders = append(folders, *fs)
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Size > folders[j].Size })
	if len(folders) > 10 {
		folders = folders[:10]
	}
	stats.TopFolders = folders
	stats.DiskTotal, stats.DiskFree = getDiskSpace(baseDir)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// GET /api/duplicates — find exact duplicate files by MD5 hash
type DupFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Mod  string `json:"mod"`
}

type DuplicateGroup struct {
	Hash  string    `json:"hash"`
	Size  int64     `json:"size"`
	Files []DupFile `json:"files"`
}

// ── Duplicate finder (async, polled) ─────────────────────────────────────────
// The scan runs in the background and the handler reports progress, so the
// request never blocks for minutes (which made the UI hang "Scanning…" forever
// on large libraries, especially behind a reverse proxy / over Tailscale).

type dupeState struct {
	mu         sync.Mutex
	running    bool
	phase      string // indexing | sampling | hashing | done
	processed  int    // files full-hashed so far
	total      int    // full-hash candidates
	groups     []DuplicateGroup
	totalWaste int64
	finishedAt time.Time
	err        string
}

var dupes = &dupeState{}

// GET /api/duplicates       — returns current state; starts a scan if none has
//                             run yet and none is running.
// GET /api/duplicates?rescan=1 — forces a fresh scan.
func duplicatesHandler(w http.ResponseWriter, r *http.Request) {
	rescan := r.URL.Query().Get("rescan") == "1"
	dupes.mu.Lock()
	if !dupes.running && (dupes.finishedAt.IsZero() || rescan) {
		dupes.running = true
		dupes.phase = "indexing"
		dupes.processed, dupes.total = 0, 0
		dupes.groups, dupes.totalWaste, dupes.err = nil, 0, ""
		go runDuplicateScan()
	}
	resp := map[string]any{
		"scanning":   dupes.running,
		"phase":      dupes.phase,
		"processed":  dupes.processed,
		"total":      dupes.total,
		"groups":     []DuplicateGroup{},
		"totalWaste": dupes.totalWaste,
	}
	if !dupes.running && dupes.groups != nil {
		resp["groups"] = dupes.groups
	}
	if dupes.err != "" {
		resp["error"] = dupes.err
	}
	dupes.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func runDuplicateScan() {
	groups, waste := computeDuplicates()
	dupes.mu.Lock()
	dupes.running = false
	dupes.phase = "done"
	dupes.groups = groups
	dupes.totalWaste = waste
	dupes.finishedAt = time.Now()
	dupes.mu.Unlock()
}

// sampleHash hashes only the first 64 KiB — a cheap fingerprint to rule out
// same-size files that obviously differ, without reading whole multi-GB videos.
func sampleHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n, _ := io.ReadFull(f, buf) // short files: n = file size, err = ErrUnexpectedEOF
	sum := md5.Sum(buf[:n])
	return fmt.Sprintf("%x", sum)
}

func fullHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func computeDuplicates() ([]DuplicateGroup, int64) {
	trashLower := strings.ToLower(filepath.Base(trashDir))

	type fileEntry struct {
		path string
		info os.FileInfo
	}

	// Pass 1 (index): group by size — stat only, no I/O.
	bySize := map[int64][]fileEntry{}
	filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			lower := strings.ToLower(name)
			if lower == trashLower || lower == strings.ToLower(uploadDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() == 0 || (!isImage(name) && !isVideo(name)) {
			return nil
		}
		bySize[info.Size()] = append(bySize[info.Size()], fileEntry{path, info})
		return nil
	})

	// Pass 2 (sample): split each size group by a 64 KiB prefix hash. Files
	// that differ in their first 64 KiB can't be identical, so this avoids
	// fully reading huge videos that merely happen to share a size.
	dupes.mu.Lock()
	dupes.phase = "sampling"
	dupes.mu.Unlock()
	bySample := map[string][]fileEntry{} // key: "<size>:<sampleHash>"
	for sz, files := range bySize {
		if len(files) < 2 {
			continue
		}
		for _, fe := range files {
			sh := sampleHash(fe.path)
			if sh == "" {
				continue
			}
			bySample[fmt.Sprintf("%d:%s", sz, sh)] = append(bySample[fmt.Sprintf("%d:%s", sz, sh)], fe)
		}
	}

	// Count the files that still need a full hash, for progress reporting.
	total := 0
	for _, files := range bySample {
		if len(files) >= 2 {
			total += len(files)
		}
	}
	dupes.mu.Lock()
	dupes.phase = "hashing"
	dupes.total = total
	dupes.processed = 0
	dupes.mu.Unlock()

	// Pass 3 (confirm): full-hash only the sample-collision candidates.
	type hashEntry struct {
		files []DupFile
		size  int64
	}
	hashes := map[string]*hashEntry{}
	for _, files := range bySample {
		if len(files) < 2 {
			continue
		}
		for _, fe := range files {
			full := fullHash(fe.path)
			dupes.mu.Lock()
			dupes.processed++
			dupes.mu.Unlock()
			if full == "" {
				continue
			}
			rel, _ := filepath.Rel(baseDir, fe.path)
			rel = filepath.ToSlash(rel)
			df := DupFile{Path: rel, Name: fe.info.Name(), Size: fe.info.Size(), Mod: fe.info.ModTime().Format("Jan 2, 2006")}
			if e, ok := hashes[full]; ok {
				e.files = append(e.files, df)
			} else {
				hashes[full] = &hashEntry{files: []DupFile{df}, size: fe.info.Size()}
			}
		}
	}

	var groups []DuplicateGroup
	var totalWaste int64
	for hash, e := range hashes {
		if len(e.files) > 1 {
			groups = append(groups, DuplicateGroup{Hash: hash, Size: e.size, Files: e.files})
			totalWaste += e.size * int64(len(e.files)-1)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Size > groups[j].Size })
	if groups == nil {
		groups = []DuplicateGroup{}
	}

	log.Printf("[DUPES] %d size-groups → %d full-hash candidates → %d dup groups (%s wasted)",
		len(bySize), total, len(groups), fmtSizeGo(totalWaste))
	return groups, totalWaste
}

func fmtSizeGo(b int64) string {
	if b < 1<<20 {
		return fmt.Sprintf("%d KB", b>>10)
	}
	if b < 1<<30 {
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(b)/float64(1<<30))
}

// POST /api/admin/file/move  body: {"path":"rel/file.jpg","destFolder":"rel/folder"}
func adminMoveFileHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Path       string `json:"path"`
		DestFolder string `json:"destFolder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	srcFull, err := safePath(baseDir, body.Path)
	if err != nil {
		http.Error(w, "invalid source path", http.StatusBadRequest)
		return
	}
	var destDir string
	if body.DestFolder == "" {
		destDir = baseDir
	} else {
		destDir, err = safePath(baseDir, body.DestFolder)
		if err != nil {
			http.Error(w, "invalid destination", http.StatusBadRequest)
			return
		}
	}
	info, err := os.Stat(destDir)
	if err != nil || !info.IsDir() {
		http.Error(w, "destination is not a folder", http.StatusBadRequest)
		return
	}
	// Prevent moving into the same folder
	if filepath.Dir(srcFull) == destDir {
		w.WriteHeader(http.StatusOK) // no-op
		return
	}
	destFull := filepath.Join(destDir, filepath.Base(srcFull))
	// Resolve name conflict
	if _, err := os.Stat(destFull); err == nil {
		ext  := filepath.Ext(destFull)
		base := strings.TrimSuffix(filepath.Base(destFull), ext)
		destFull = filepath.Join(destDir, fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext))
	}
	// Fast rename (same drive)
	if err := os.Rename(srcFull, destFull); err != nil {
		// Cross-drive fallback: copy then delete
		if err2 := copyFile(srcFull, destFull); err2 != nil {
			http.Error(w, "move failed: "+err2.Error(), http.StatusInternalServerError)
			return
		}
		os.Remove(srcFull)
	}
	log.Printf("[ADMIN] moved file: %s → %s", srcFull, destFull)
	w.WriteHeader(http.StatusOK)
}

// POST /api/admin/login  {"password":"..."}  → {"token":"..."}
// POST /api/login  {username,password}  OR  {guest:true}
func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r)
	if logins.locked(ip) {
		http.Error(w, "too many attempts — try again later", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Guest    bool   `json:"guest"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	var username, role string
	if body.Guest {
		if !guestAccess {
			http.Error(w, "guest access disabled", http.StatusForbidden)
			return
		}
		username, role = "guest", "viewer"
	} else {
		u, ok := authenticate(body.Username, body.Password)
		if !ok {
			logins.fail(ip)
			time.Sleep(500 * time.Millisecond) // slow brute-force
			http.Error(w, "wrong username or password", http.StatusUnauthorized)
			return
		}
		username, role = u.Username, u.Role
	}
	logins.reset(ip)
	token := sessions.create(username, role)
	setSessionCookie(w, token)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"username": username, "role": role})
}

// POST /api/logout
func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		sessions.revoke(c.Value)
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusOK)
}

// GET /api/me — who am I + whether guest login is offered
func meHandler(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"authenticated": false, "guestAccess": guestAccess}
	if sess, ok := sessionFromRequest(r); ok {
		resp["authenticated"] = true
		resp["username"] = sess.Username
		resp["role"] = sess.Role
		resp["isGuest"] = sess.Username == "guest"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ── User management (admin only) ─────────────────────────────────────────────

// persistUsers saves the current users + guestAccess into the config file.
func persistUsers() {
	cfg := loadConfig(configFilePath)
	usersMu.Lock()
	cfg.Users = append([]User(nil), users...)
	usersMu.Unlock()
	cfg.GuestAccess = guestAccess
	cfg.AdminPass = ""     // ensure no stray plaintext
	cfg.AdminPassHash = "" // superseded by the users list
	saveConfig(configFilePath, cfg)
}

// GET /api/users — list accounts (no hashes)
func usersListHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	usersMu.Lock()
	out := make([]map[string]string, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]string{"username": u.Username, "role": u.Role})
	}
	usersMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"users": out, "guestAccess": guestAccess})
}

// POST /api/users {username,password,role} — add or update an account
func usersSaveHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" {
		http.Error(w, "username required", http.StatusBadRequest)
		return
	}
	if body.Role != "admin" && body.Role != "viewer" {
		body.Role = "viewer"
	}
	existing, idx := findUser(body.Username)
	if idx < 0 && body.Password == "" {
		http.Error(w, "password required for a new user", http.StatusBadRequest)
		return
	}
	// Prevent demoting the last admin.
	if idx >= 0 && existing.Role == "admin" && body.Role != "admin" && adminCount() <= 1 {
		http.Error(w, "cannot demote the last admin", http.StatusConflict)
		return
	}
	hash := existing.PassHash
	if body.Password != "" {
		h, err := hashPassword(body.Password)
		if err != nil {
			http.Error(w, "could not hash password", http.StatusInternalServerError)
			return
		}
		hash = h
	}
	usersMu.Lock()
	if idx >= 0 {
		users[idx] = User{Username: body.Username, PassHash: hash, Role: body.Role}
	} else {
		users = append(users, User{Username: body.Username, PassHash: hash, Role: body.Role})
	}
	usersMu.Unlock()
	persistUsers()
	w.WriteHeader(http.StatusOK)
}

// DELETE /api/users?username= — remove an account
func usersDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	username := r.URL.Query().Get("username")
	u, idx := findUser(username)
	if idx < 0 {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	if u.Role == "admin" && adminCount() <= 1 {
		http.Error(w, "cannot delete the last admin", http.StatusConflict)
		return
	}
	usersMu.Lock()
	users = append(users[:idx], users[idx+1:]...)
	usersMu.Unlock()
	persistUsers()
	w.WriteHeader(http.StatusOK)
}

// POST /api/guest-access {enabled:bool}
func guestAccessHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	guestAccess = body.Enabled
	persistUsers()
	w.WriteHeader(http.StatusOK)
}

// moveToTrash moves src into trashDir, preserving the name and adding a
// timestamp prefix so repeated deletions of the same name never collide.
// It tries os.Rename first (instant, same drive); falls back to copy+delete
// when src and trashDir are on different drives.
// ── Auto-purge old trash ──────────────────────────────────────────────────────

const trashRetentionDays = 90

// purgeOldTrash permanently deletes trashed items older than trashRetentionDays.
func purgeOldTrash() {
	cutoff := time.Now().AddDate(0, 0, -trashRetentionDays)
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		return
	}
	purged := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".trashinfo") {
			continue
		}
		var t time.Time
		if _, deletedAt := readTrashInfo(filepath.Join(trashDir, name)); deletedAt != "" {
			t, _ = time.Parse(time.RFC1123, deletedAt)
		}
		if t.IsZero() { // no/invalid sidecar — fall back to file mod time
			if info, e2 := e.Info(); e2 == nil {
				t = info.ModTime()
			} else {
				continue
			}
		}
		if t.Before(cutoff) {
			os.RemoveAll(filepath.Join(trashDir, name))
			os.Remove(filepath.Join(trashDir, name+".trashinfo"))
			purged++
		}
	}
	if purged > 0 {
		log.Printf("[TRASH] auto-purged %d item(s) older than %d days", purged, trashRetentionDays)
	}
}

// trashAutoPurger runs purgeOldTrash at startup and once a day.
func trashAutoPurger() {
	for {
		purgeOldTrash()
		time.Sleep(24 * time.Hour)
	}
}

// ── Date helpers (shared by auto-sort and the memories index) ─────────────────

// fileDate returns a file's best-known capture date: EXIF DateTimeOriginal for
// JPEG/PNG/etc., falling back to the filesystem modification time.
func fileDate(path string, info os.FileInfo) time.Time {
	name := info.Name()
	if isImage(name) && !isHeic(name) {
		if f, err := os.Open(path); err == nil {
			if x, derr := exif.Decode(f); derr == nil {
				if t, terr := x.DateTime(); terr == nil && !t.IsZero() {
					f.Close()
					return t
				}
			}
			f.Close()
		}
	}
	return info.ModTime()
}

// ── Auto-sort the upload inbox into Year/Month folders ───────────────────────

// sortUploads moves completed media files out of the upload inbox into
// <baseDir>/<YYYY>/<YYYY-MM>/ based on their capture date.
func sortUploads() {
	if !autoSort {
		return
	}
	inbox := filepath.Join(baseDir, uploadDir)
	entries, err := os.ReadDir(inbox)
	if err != nil {
		return
	}
	moved := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || (!isImage(name) && !isVideo(name)) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Skip files that may still be uploading (e.g. an in-progress SMB copy).
		if time.Since(info.ModTime()) < 30*time.Second {
			continue
		}
		full := filepath.Join(inbox, name)
		when := fileDate(full, info)
		destDir := filepath.Join(baseDir, when.Format("2006"), when.Format("2006-01"))
		if err := os.MkdirAll(destDir, 0755); err != nil {
			continue
		}
		dest := filepath.Join(destDir, name)
		if _, err := os.Stat(dest); err == nil { // name conflict
			ext := filepath.Ext(name)
			base := strings.TrimSuffix(name, ext)
			dest = filepath.Join(destDir, fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext))
		}
		if err := os.Rename(full, dest); err != nil {
			if copyFile(full, dest) == nil {
				os.Remove(full)
			} else {
				continue
			}
		}
		moved++
	}
	if moved > 0 {
		log.Printf("[AUTOSORT] filed %d upload(s) into Year/Month folders", moved)
	}
}

// uploadSorter runs sortUploads on a short interval (no-op unless enabled).
func uploadSorter() {
	for {
		sortUploads()
		time.Sleep(2 * time.Minute)
	}
}

// ── "On This Day" memories index ─────────────────────────────────────────────

type datedFile struct {
	Path    string
	Taken   time.Time
	IsVideo bool
	Lat     float64
	Lng     float64
	Geo     bool
}

// fileDateGeo decodes EXIF once to extract both capture date and GPS coords.
func fileDateGeo(path string, info os.FileInfo) (when time.Time, lat, lng float64, hasGeo bool) {
	when = info.ModTime()
	name := info.Name()
	if isImage(name) && !isHeic(name) {
		if f, err := os.Open(path); err == nil {
			if x, derr := exif.Decode(f); derr == nil {
				if t, terr := x.DateTime(); terr == nil && !t.IsZero() {
					when = t
				}
				if la, lo, gerr := x.LatLong(); gerr == nil && (la != 0 || lo != 0) {
					lat, lng, hasGeo = la, lo, true
				}
			}
			f.Close()
		}
	}
	return
}

var (
	dateIndexMu    sync.RWMutex
	dateIndex      []datedFile
	dateIndexBuilt bool
)

// buildDateIndex walks the library once and records each media file's capture
// date, so /api/on-this-day can answer instantly.
func buildDateIndex() {
	trashLow := strings.ToLower(filepath.Base(trashDir))
	uploadLow := strings.ToLower(uploadDir)
	var idx []datedFile
	filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			lower := strings.ToLower(info.Name())
			if strings.HasPrefix(info.Name(), ".") || lower == trashLow || lower == uploadLow {
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, ".") || (!isImage(name) && !isVideo(name)) {
			return nil
		}
		rel, _ := filepath.Rel(baseDir, path)
		when, lat, lng, geo := fileDateGeo(path, info)
		idx = append(idx, datedFile{Path: filepath.ToSlash(rel), Taken: when, IsVideo: isVideo(name), Lat: lat, Lng: lng, Geo: geo})
		return nil
	})
	dateIndexMu.Lock()
	dateIndex = idx
	dateIndexBuilt = true
	dateIndexMu.Unlock()
	geoCount := 0
	for _, f := range idx {
		if f.Geo {
			geoCount++
		}
	}
	log.Printf("[MEMORIES] indexed %d files by date (%d geotagged)", len(idx), geoCount)
}

// dateIndexer builds the index shortly after startup and refreshes daily.
func dateIndexer() {
	time.Sleep(5 * time.Second)
	for {
		buildDateIndex()
		time.Sleep(24 * time.Hour)
	}
}

// GET /api/on-this-day — photos taken on today's month/day in previous years.
func onThisDayHandler(w http.ResponseWriter, r *http.Request) {
	dateIndexMu.RLock()
	built := dateIndexBuilt
	idx := dateIndex
	dateIndexMu.RUnlock()

	now := time.Now()
	type item struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		IsVideo bool   `json:"isVideo"`
	}
	type group struct {
		Year     int    `json:"year"`
		YearsAgo int    `json:"yearsAgo"`
		Items    []item `json:"items"`
	}
	byYear := map[int]*group{}
	for _, f := range idx {
		if f.Taken.Month() == now.Month() && f.Taken.Day() == now.Day() && f.Taken.Year() != now.Year() {
			y := f.Taken.Year()
			g := byYear[y]
			if g == nil {
				g = &group{Year: y, YearsAgo: now.Year() - y}
				byYear[y] = g
			}
			g.Items = append(g.Items, item{Path: f.Path, Name: filepath.Base(f.Path), IsVideo: f.IsVideo})
		}
	}
	groups := make([]*group, 0, len(byYear))
	for _, g := range byYear {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Year > groups[j].Year }) // newest first
	if groups == nil {
		groups = []*group{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"built": built, "groups": groups})
}

// GET /api/geo — all geotagged photos with their coordinates (for the map).
func geoHandler(w http.ResponseWriter, r *http.Request) {
	dateIndexMu.RLock()
	built := dateIndexBuilt
	idx := dateIndex
	dateIndexMu.RUnlock()

	type point struct {
		Path    string  `json:"path"`
		Name    string  `json:"name"`
		Lat     float64 `json:"lat"`
		Lng     float64 `json:"lng"`
		IsVideo bool    `json:"isVideo"`
	}
	points := []point{}
	for _, f := range idx {
		if f.Geo {
			points = append(points, point{Path: f.Path, Name: filepath.Base(f.Path), Lat: f.Lat, Lng: f.Lng, IsVideo: f.IsVideo})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"built": built, "points": points})
}

func moveToTrash(src, originalRel string) error {
	timestamp := time.Now().Format("20060102_150405")
	base      := filepath.Base(src)
	dest      := filepath.Join(trashDir, timestamp+"_"+base)

	// Avoid the (unlikely) same-second collision
	if _, err := os.Stat(dest); err == nil {
		dest = filepath.Join(trashDir, fmt.Sprintf("%s_%d_%s", timestamp, time.Now().UnixNano(), base))
	}

	// Fast path: rename (same drive)
	if err := os.Rename(src, dest); err == nil {
		writeTrashInfo(dest, originalRel)
		return nil
	}

	// Slow path: copy recursively then remove original
	if err := copyAll(src, dest); err != nil {
		return err
	}
	writeTrashInfo(dest, originalRel)
	return os.RemoveAll(src)
}

// writeTrashInfo saves a small sidecar so you know where a file came from.
func writeTrashInfo(dest, originalRel string) {
	info := fmt.Sprintf("original: %s\ndeleted:  %s\n", originalRel, time.Now().Format(time.RFC1123))
	os.WriteFile(dest+".trashinfo", []byte(info), 0644)
}

// copyAll recursively copies src to dst.
func copyAll(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.ReadFrom(in)
	return err
}

// DELETE /api/admin/delete?path=...
func adminDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := safePath(baseDir, rel)
	if err != nil || full == baseDir {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := moveToTrash(full, rel); err != nil {
		http.Error(w, "move to trash failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[ADMIN] trashed: %s → %s", full, trashDir)
	w.WriteHeader(http.StatusOK)
}

// ── Background thumbnail pre-generation ──────────────────────────────────────

type pregenState struct {
	mu      sync.Mutex
	running bool
	total   int
	done    int
	errors  int
}

var pregen = &pregenState{}

func thumbStatusHandler(w http.ResponseWriter, r *http.Request) {
	pregen.mu.Lock()
	s := map[string]any{
		"running": pregen.running,
		"total":   pregen.total,
		"done":    pregen.done,
		"errors":  pregen.errors,
	}
	pregen.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func pregenThumbs() {
	// Wait for server to start before scanning
	time.Sleep(3 * time.Second)

	trashLow := strings.ToLower(filepath.Base(trashDir))

	var files []string
	filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil { return nil }
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") { return filepath.SkipDir }
			lower := strings.ToLower(info.Name())
			if lower == trashLow || lower == strings.ToLower(uploadDir) { return filepath.SkipDir }
			return nil
		}
		if !strings.HasPrefix(info.Name(), ".") && (isImage(info.Name()) || isVideo(info.Name())) {
			files = append(files, path)
		}
		return nil
	})

	pregen.mu.Lock()
	pregen.running = true
	pregen.total = len(files)
	pregen.mu.Unlock()

	log.Printf("[PREGEN] Starting thumbnail pre-generation for %d files", len(files))

	// Worker pool — 3 concurrent thumb generators
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup

	for _, filePath := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()

			hash := fmt.Sprintf("%x", md5.Sum([]byte(p)))
			thumbPath := filepath.Join(thumbDir, hash+".jpg")

			// Skip if already cached
			if _, err := os.Stat(thumbPath); err == nil {
				pregen.mu.Lock(); pregen.done++; pregen.mu.Unlock()
				return
			}

			name := filepath.Base(p)
			ff := ffmpegPath()
			var genErr error

			if isVideo(name) {
				if ff == "" { pregen.mu.Lock(); pregen.errors++; pregen.mu.Unlock(); return }
				args := []string{"-ss", "00:00:01", "-i", p, "-vframes", "1",
					"-vf", "scale=300:300:force_original_aspect_ratio=increase,crop=300:300",
					"-q:v", "3", "-y", thumbPath}
				hideCmd(exec.Command(ff, args...)).CombinedOutput()
				if _, err := os.Stat(thumbPath); err != nil {
					args = []string{"-i", p, "-vframes", "1",
						"-vf", "scale=300:300:force_original_aspect_ratio=increase,crop=300:300",
						"-q:v", "3", "-y", thumbPath}
				}
				_, genErr = hideCmd(exec.Command(ff, args...)).CombinedOutput()
			} else if isHeic(name) {
				// HEIC: decode via libheif, then resize
				tmp := thumbPath + ".src.jpg"
				if err := heicToJPEG(p, tmp); err != nil {
					genErr = err
				} else if img, oerr := imaging.Open(tmp, imaging.AutoOrientation(true)); oerr != nil {
					genErr = oerr
				} else {
					genErr = imaging.Save(imaging.Thumbnail(img, 300, 300, imaging.Lanczos), thumbPath)
				}
				os.Remove(tmp)
			} else {
				img, err := imaging.Open(p, imaging.AutoOrientation(true))
				if err != nil { genErr = err } else {
					thumb := imaging.Thumbnail(img, 300, 300, imaging.Lanczos)
					genErr = imaging.Save(thumb, thumbPath)
				}
			}

			pregen.mu.Lock()
			if genErr != nil { pregen.errors++ } else { pregen.done++ }
			pregen.mu.Unlock()
		}(filePath)
	}

	wg.Wait()
	pregen.mu.Lock()
	pregen.running = false
	pregen.mu.Unlock()
	log.Printf("[PREGEN] Done — %d thumbnails generated, %d errors", pregen.done, pregen.errors)
}

// POST /api/thumbs/clear — delete all cached thumbnails and restart pre-gen
func thumbClearHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	entries, _ := os.ReadDir(thumbDir)
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jpg") {
			os.Remove(filepath.Join(thumbDir, e.Name()))
			count++
		}
	}
	// Reset and restart pre-generation
	pregen.mu.Lock()
	pregen.done = 0
	pregen.total = 0
	pregen.errors = 0
	pregen.running = false
	pregen.mu.Unlock()
	go pregenThumbs()
	log.Printf("[THUMBS] Cleared %d cached thumbnails, restarting pre-gen", count)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"cleared": count})
}

// POST /api/admin/rotate?path=...&angle=90|180|270
func adminRotateHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	rel := r.URL.Query().Get("path")
	angleStr := r.URL.Query().Get("angle")
	angle, err := strconv.Atoi(angleStr)
	if err != nil || (angle != 90 && angle != 180 && angle != 270) {
		http.Error(w, "angle must be 90, 180 or 270", http.StatusBadRequest)
		return
	}
	full, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if isHeic(filepath.Base(full)) {
		http.Error(w, "HEIC rotation not supported — convert to JPEG first", http.StatusBadRequest)
		return
	}
	img, err := imaging.Open(full, imaging.AutoOrientation(true))
	if err != nil {
		http.Error(w, "cannot open image: "+err.Error(), http.StatusInternalServerError)
		return
	}
	switch angle {
	case 90:
		img = imaging.Rotate90(img)
	case 180:
		img = imaging.Rotate180(img)
	case 270:
		img = imaging.Rotate270(img)
	}
	if err := imaging.Save(img, full); err != nil {
		http.Error(w, "cannot save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Clear thumbnail cache for this file
	hash := fmt.Sprintf("%x", md5.Sum([]byte(full)))
	os.Remove(filepath.Join(thumbDir, hash+".jpg"))
	log.Printf("[ROTATE] %s by %d°", full, angle)
	w.WriteHeader(http.StatusOK)
}

func generateSelfSignedCert(certPath, keyPath string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	ip := serverIP()
	ips := []net.IP{net.ParseIP(ip), net.ParseIP("127.0.0.1")}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject:      pkix.Name{Organization: []string{"PhotoShare"}, CommonName: ip},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  ips,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	cf, err := os.Create(certPath)
	if err != nil {
		return err
	}
	pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	cf.Close()

	kf, err := os.Create(keyPath)
	if err != nil {
		return err
	}
	pem.Encode(kf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	kf.Close()

	log.Printf("Generated self-signed certificate for %s (valid 10 years)", ip)
	return nil
}

func main() {
	// webview2 (Windows) must run on the OS main thread; locking this goroutine
	// to it is a no-op everywhere else.
	runtime.LockOSThread()

	// Config/log/cert location — DATA_DIR (a Docker volume), %APPDATA%\PhotoShare
	// on Windows, or next to the exe everywhere else.
	exe, _ := os.Executable()
	defaultDataDir := filepath.Dir(exe)
	if runtime.GOOS == "windows" {
		if cfgDir, err := os.UserConfigDir(); err == nil {
			defaultDataDir = filepath.Join(cfgDir, "PhotoShare")
		}
	}
	dataDir = envOr("DATA_DIR", defaultDataDir)
	os.MkdirAll(dataDir, 0755)
	configFilePath = filepath.Join(dataDir, "photoshare.config.json")

	// Load config file first — flags/env can override individual values
	cfg := loadConfig(configFilePath)

	flag.StringVar(&baseDir, "dir", envOr("PHOTO_DIR", cfg.PhotoDir), "Base directory to serve photos from")
	flag.StringVar(&port, "port", envOr("PORT", cfg.Port), "Port to listen on")
	adminPassFlag := flag.String("admin-password", "", "Set/replace the admin password (stored hashed)")
	flag.StringVar(&shareName, "share-name", cfg.ShareName, "SMB network share name (optional)")
	flag.StringVar(&serverIPFlag, "server-ip", envOr("SERVER_IP", cfg.ServerIP), "Override the server IP")
	flag.StringVar(&ffmpegFlag, "ffmpeg-path", cfg.FfmpegPath, "Explicit path to the ffmpeg binary")
	flag.StringVar(&uploadDir, "upload-folder", cfg.UploadFolder, "Name of the uploads inbox folder")
	flag.BoolVar(&httpOnly, "http-only", envBool("HTTP_ONLY", cfg.HTTPOnly), "Serve plain HTTP instead of self-signed HTTPS")
	flag.BoolVar(&autoSort, "auto-sort", envBool("AUTO_SORT", cfg.AutoSort), "Auto-file inbox uploads into Year/Month folders")
	// Deprecated: trash is always <photoDir>/_Trash now. Accepted-but-ignored so
	// an existing scheduled task that still passes -trash-dir won't fail to start.
	flag.String("trash-dir", "", "deprecated — ignored (trash is always <photoDir>/_Trash)")
	flag.Parse()

	// The Windows desktop app always serves plain HTTP. Its native window loads
	// over loopback (127.0.0.1), which browsers treat as a secure context — so
	// no "Not Secure" warning. Self-signed HTTPS would only break this: the
	// WebView2 window can't trust the cert and shows a NET::ERR_CERT_AUTHORITY_
	// INVALID interstitial, while LAN clients still see "not secure" anyway
	// (the cert isn't trusted on their devices either). Real TLS belongs in a
	// reverse proxy in front (the Docker path), not the desktop build — so we
	// force HTTP here regardless of config/flag/env. The Settings "Use HTTPS"
	// toggle is hidden on Windows to match (see SettingsModal).
	if runtime.GOOS == "windows" {
		httpOnly = true
	}

	// Web UI on the LAN unless explicitly disabled (absent key = enabled).
	lanAccess = !cfg.DisableWebUI

	// Load accounts and migrate the legacy single admin password into a user.
	users = append([]User(nil), cfg.Users...)
	guestAccess = cfg.GuestAccess

	// Resolve a legacy/seed admin hash (flag > stored hash > config plaintext > env).
	// Seed-password precedence: -admin-password flag > ADMIN_PASSWORD env >
	// stored hash > config plaintext (the "123456" default is the last resort).
	legacyHash := cfg.AdminPassHash
	if *adminPassFlag != "" {
		if h, err := hashPassword(*adminPassFlag); err == nil {
			legacyHash = h
		}
	} else if os.Getenv("ADMIN_PASSWORD") != "" {
		if h, err := hashPassword(os.Getenv("ADMIN_PASSWORD")); err == nil {
			legacyHash = h
		}
	} else if legacyHash == "" && cfg.AdminPass != "" {
		if h, err := hashPassword(cfg.AdminPass); err == nil {
			legacyHash = h
		}
	}

	usersChanged := false
	if len(users) == 0 {
		// First run / upgrade from single-password mode: seed an admin account.
		// If the operator gave no password (no -admin-password, no
		// ADMIN_PASSWORD, no stored hash), generate a random one and print it
		// once — never seed a fixed, publicly-known default.
		adminUser := envOr("ADMIN_USER", "admin")
		if legacyHash == "" {
			pw := randomPassword()
			legacyHash, _ = hashPassword(pw)
			log.Printf("WARNING: no admin password configured — generated one for %q: %s", adminUser, pw)
			log.Printf("         Log in with it, then change it in Settings (set ADMIN_PASSWORD to control this).")
		}
		users = []User{{Username: adminUser, PassHash: legacyHash, Role: "admin"}}
		usersChanged = true
		log.Printf("Created default admin account %q", adminUser)
	} else if *adminPassFlag != "" {
		// -admin-password resets the first admin account's password.
		for i := range users {
			if users[i].Role == "admin" {
				users[i].PassHash = legacyHash
				usersChanged = true
				break
			}
		}
	}

	// Persist config on first run, migration, or any account change.
	_, statErr := os.Stat(configFilePath)
	if os.IsNotExist(statErr) || cfg.AdminPass != "" || cfg.AdminPassHash != "" || usersChanged {
		cfg.PhotoDir = baseDir
		cfg.Port = port
		cfg.ShareName = shareName
		cfg.ServerIP = serverIPFlag
		cfg.UploadFolder = uploadDir
		cfg.FfmpegPath = ffmpegFlag
		cfg.HTTPOnly = httpOnly
		cfg.AutoSort = autoSort
		cfg.AdminPass = ""     // fully migrated to the users list
		cfg.AdminPassHash = "" // ditto
		cfg.Users = users
		cfg.GuestAccess = guestAccess
		saveConfig(configFilePath, cfg)
		log.Printf("Wrote config: %s", configFilePath)
	}

	// Resolve ffmpeg once so we never spawn `where` during requests
	cachedFFmpeg = ffmpegPath()
	log.Printf("FFmpeg: %s", cachedFFmpeg)

	// Logs go to stdout so `docker logs` (or systemd's journal) captures them.

	// Resolve the photo directory to an absolute path first.
	var err error
	baseDir, err = filepath.Abs(baseDir)
	if err != nil {
		log.Fatal(err)
	}

	// The photo directory is the one machine-specific value the operator must
	// provide, so require it to exist — don't auto-create it (a typo'd path
	// shouldn't silently spin up an empty library). The sub-folders below ARE
	// created automatically. On Windows there's no operator pre-provisioning a
	// volume, so a missing/invalid path on first run isn't fatal — the app
	// starts anyway and the UI walks the user through onboarding to pick one.
	info, statErr := os.Stat(baseDir)
	validBaseDir := statErr == nil && info.IsDir()
	if !validBaseDir {
		if runtime.GOOS != "windows" {
			log.Fatalf("photo directory does not exist: %s — set it via -dir or the config file", baseDir)
		}
		log.Printf("No valid photo directory configured yet (%s) — waiting for setup", baseDir)
		baseDir = ""
	}

	// Trash/uploads/thumbs live inside the photo folder; skip until one is set.
	if validBaseDir {
		// Trash lives inside the photo folder, exactly like the uploads inbox —
		// no separate path to configure.
		trashDir = filepath.Join(baseDir, "_Trash")
		if err := os.MkdirAll(trashDir, 0755); err != nil {
			log.Fatal("cannot create trash dir:", err)
		}
		log.Printf("Trash folder: %s", trashDir)

		// Create uploads inbox folder
		uploadFull := filepath.Join(baseDir, uploadDir)
		if err := os.MkdirAll(uploadFull, 0755); err != nil {
			log.Printf("WARNING: cannot create upload folder: %v", err)
		}
		log.Printf("Upload inbox: %s", uploadFull)
		log.Printf("Serving photos from: %s", baseDir)
	} else {
		log.Printf("No photo library configured yet — open the app to finish setup")
	}

	thumbDir = filepath.Join(os.TempDir(), "photo-share-thumbs")
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		log.Fatal("cannot create thumb cache dir:", err)
	}

	log.Printf("Open http://localhost:%s in your browser", port)

	mainMux = http.NewServeMux()
	mux := mainMux
	// protected wraps a content handler so it requires any valid session.
	protected := func(h http.HandlerFunc) http.HandlerFunc {
		return withCORS(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := requireAuth(w, r); !ok {
				return
			}
			h(w, r)
		})
	}

	// ── Auth (open: no session required) ──
	mux.HandleFunc("/api/login", withCORS(loginHandler))
	mux.HandleFunc("/api/logout", withCORS(logoutHandler))
	mux.HandleFunc("/api/me", withCORS(meHandler))

	// ── User management (admin only — enforced inside handlers) ──
	mux.HandleFunc("/api/users", withCORS(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			usersListHandler(w, r)
		case http.MethodPost:
			usersSaveHandler(w, r)
		case http.MethodDelete:
			usersDeleteHandler(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/guest-access", withCORS(guestAccessHandler))

	// ── Admin/write actions (each enforces admin role internally) ──
	mux.HandleFunc("/api/admin/delete", withCORS(adminDeleteHandler))
	mux.HandleFunc("/api/admin/folder/create", withCORS(adminCreateFolderHandler))
	mux.HandleFunc("/api/admin/folder/rename", withCORS(adminRenameFolderHandler))
	mux.HandleFunc("/api/admin/folder/delete", withCORS(adminDeleteFolderHandler))
	mux.HandleFunc("/api/admin/file/move", withCORS(adminMoveFileHandler))
	mux.HandleFunc("/api/admin/batch/delete", withCORS(adminBatchDeleteHandler))
	mux.HandleFunc("/api/admin/batch/copy", withCORS(adminBatchCopyHandler))
	mux.HandleFunc("/api/admin/batch/move", withCORS(adminBatchMoveHandler))
	mux.HandleFunc("/api/admin/batch/rename", withCORS(adminBatchRenameHandler))
	mux.HandleFunc("/api/thumbs/clear", withCORS(thumbClearHandler))
	mux.HandleFunc("/api/admin/rotate", withCORS(adminRotateHandler))
	mux.HandleFunc("/api/settings", withCORS(settingsHandler))
	mux.HandleFunc("/api/upload", withCORS(uploadHandler))
	mux.HandleFunc("/api/trash/restore", withCORS(trashRestoreHandler))
	mux.HandleFunc("/api/trash/purge", withCORS(trashPurgeHandler))
	mux.HandleFunc("/api/trash/purge-all", withCORS(trashPurgeAllHandler))
	// /api/fs/* expose the whole filesystem (not just the library), so they
	// require an admin session — except during first-run setup, before any
	// session can exist yet, while baseDir is still unset. During that setup
	// window they're further restricted to local requests (isLocalRequest)
	// so a remote device on the LAN can't enumerate the filesystem before
	// the person at the machine finishes onboarding.
	mux.HandleFunc("/api/fs/roots", withCORS(func(w http.ResponseWriter, r *http.Request) {
		if baseDir == "" {
			if !isLocalRequest(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		} else if !requireAdmin(w, r) {
			return
		}
		fsRootsHandler(w, r)
	}))
	mux.HandleFunc("/api/fs/browse", withCORS(func(w http.ResponseWriter, r *http.Request) {
		if baseDir == "" {
			if !isLocalRequest(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		} else if !requireAdmin(w, r) {
			return
		}
		fsBrowseHandler(w, r)
	}))
	mux.HandleFunc("/api/autostart", withCORS(autostartHandler))
	mux.HandleFunc("/api/update/check", withCORS(updateCheckHandler))
	mux.HandleFunc("/api/update/run", withCORS(updateRunHandler))

	// ── Desktop/onboarding (open: no session required) ──
	mux.HandleFunc("/api/onboarding-status", withCORS(onboardingStatusHandler))
	mux.HandleFunc("/api/onboarding", withCORS(onboardingHandler))
	mux.HandleFunc("/api/platform", withCORS(platformHandler))
	mux.HandleFunc("/api/show", withCORS(showHandler))
	mux.HandleFunc("/api/quit", withCORS(quitHandler))

	// ── Content (require a valid session — any role) ──
	mux.HandleFunc("/api/stats", protected(statsHandler))
	mux.HandleFunc("/api/thumbs/status", protected(thumbStatusHandler))
	mux.HandleFunc("/api/duplicates", protected(duplicatesHandler))
	mux.HandleFunc("/api/inbox-upload", protected(inboxUploadHandler))
	mux.HandleFunc("/api/open-folder", protected(openFolderHandler))
	mux.HandleFunc("/api/trash", protected(trashListHandler))
	mux.HandleFunc("/api/trash/thumb", protected(trashThumbHandler))
	mux.HandleFunc("/api/server-info", protected(serverInfoHandler))
	mux.HandleFunc("/api/qr", protected(qrHandler))
	mux.HandleFunc("/api/on-this-day", protected(onThisDayHandler))
	mux.HandleFunc("/api/geo", protected(geoHandler))
	mux.HandleFunc("/api/search", protected(searchHandler))
	mux.HandleFunc("/api/search/semantic", protected(semanticSearchHandler))
	mux.HandleFunc("/api/ai/status", protected(aiStatusHandler))
	mux.HandleFunc("/api/faces/status", protected(facesStatusHandler))
	mux.HandleFunc("/api/faces/people", protected(peopleHandler))
	mux.HandleFunc("/api/faces/photos", protected(personPhotosHandler))
	mux.HandleFunc("/api/faces/name", protected(nameFaceHandler))
	mux.HandleFunc("/api/browse", protected(browseHandler))
	mux.HandleFunc("/api/meta", protected(metaHandler))
	mux.HandleFunc("/api/folder-info", protected(folderInfoHandler))
	mux.HandleFunc("/api/thumb", protected(thumbHandler))
	mux.HandleFunc("/api/photo", protected(photoHandler))
	mux.HandleFunc("/api/video", protected(photoHandler))

	// ── Open / public (PWA assets + setup helper) ──
	mux.HandleFunc("/manifest.json", manifestHandler)
	mux.HandleFunc("/icon-192.png", func(w http.ResponseWriter, r *http.Request) { servePWAIcon(w, 192) })
	mux.HandleFunc("/icon-512.png", func(w http.ResponseWriter, r *http.Request) { servePWAIcon(w, 512) })

	// Serve embedded React build; fall back to index.html for client-side routing
	distFS, err := fs.Sub(clientDist, "client/dist")
	if err != nil {
		log.Fatal("embed error:", err)
	}
	fileServer := http.FileServer(http.FS(distFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Try to open the file from the embedded FS
		f, ferr := distFS.Open(strings.TrimPrefix(r.URL.Path, "/"))
		if ferr == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fall back to index.html (SPA client routing)
		index, _ := clientDist.ReadFile("client/dist/index.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(index)
	})

	// Certificate paths
	certPath := filepath.Join(dataDir, "cert.pem")
	keyPath  := filepath.Join(dataDir, "key.pem")

	// Generate cert if missing or if server IP changed (skipped entirely in HTTP-only mode)
	needCert := false
	if httpOnly {
		// no cert needed
	} else if _, err := os.Stat(certPath); os.IsNotExist(err) {
		needCert = true
	} else {
		// Check cert still valid for current IP
		if data, err := os.ReadFile(certPath); err == nil {
			if block, _ := pem.Decode(data); block != nil {
				if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
					currentIP := net.ParseIP(serverIP())
					found := false
					for _, ip := range cert.IPAddresses {
						if ip.Equal(currentIP) {
							found = true
							break
						}
					}
					if !found {
						log.Printf("Server IP changed — regenerating certificate")
						needCert = true
					}
				}
			}
		}
	}

	useHTTPS := false
	if httpOnly {
		log.Printf("HTTP-only mode — serving plain HTTP (no TLS)")
	} else if needCert {
		if err := generateSelfSignedCert(certPath, keyPath); err != nil {
			log.Printf("WARNING: cannot generate cert (%v) — falling back to HTTP", err)
		} else {
			useHTTPS = true
		}
	} else if _, err := os.Stat(certPath); err == nil {
		useHTTPS = true
	}

	// Shareable LAN address (what other devices use) — shown in the QR code
	// and the QR code. In Docker the container listens on an internal port but is
	// published on a different host port, so PUBLIC_PORT (or a full PUBLIC_URL)
	// lets the advertised address match what clients actually reach.
	scheme := "http"
	if useHTTPS {
		scheme = "https"
	}
	netURL = scheme + "://" + serverIP() + ":" + envOr("PUBLIC_PORT", port)
	if u := strings.TrimSpace(os.Getenv("PUBLIC_URL")); u != "" {
		netURL = u
	}

	// windowURL is what the native desktop window (and the already-running
	// "show" ping) loads: always loopback, never the LAN IP. Browsers treat
	// 127.0.0.1 as a secure context even over plain HTTP, so the in-window UI
	// shows no "Not Secure" warning — unlike netURL, which points at the LAN
	// address for sharing/QR and would trip the warning in the window.
	windowURL := scheme + "://127.0.0.1:" + port

	// Bind to all interfaces (LAN reachable) unless the web UI is disabled, in
	// which case bind loopback only — the native window still reaches it.
	host := ""
	if !lanAccess {
		host = "127.0.0.1"
		log.Printf("Web UI restricted to this machine (loopback only)")
	}

	// Start server in background
	go func() {
		if useHTTPS {
			log.Printf("Starting HTTPS server on https://localhost:%s", port)
			srv := &http.Server{
				Addr:    host + ":" + port,
				Handler: recoverMW(mux),
				TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			}
			if err := srv.ListenAndServeTLS(certPath, keyPath); err != nil {
				log.Fatal(err)
			}
		} else {
			log.Printf("Starting HTTP server on http://localhost:%s", port)
			if err := http.ListenAndServe(host+":"+port, recoverMW(mux)); err != nil {
				log.Fatal(err)
			}
		}
	}()

	// Start background thumbnail pre-generation
	go pregenThumbs()

	// Auto-purge trashed items older than the retention window
	go trashAutoPurger()

	// Auto-file inbox uploads into Year/Month folders (no-op unless enabled)
	go uploadSorter()

	// Build the "On This Day" date index in the background
	go dateIndexer()

	// AI semantic search — starts a background embedder only if ML_URL is set.
	aiInit()
	// Face recognition — opt-in via the Settings toggle (or FACES=1); no-op
	// otherwise. Its detector yields to the CLIP indexer so they never compete.
	faceInit(cfg.FacesEnabled)

	// On Windows, refuse to start a second copy — ask the existing instance to
	// show its window instead. Everywhere else this is a no-op (true). The
	// exception is a self-relaunch (restartProcess sets PHOTOSHARE_RESTART):
	// the old instance is on its way out, so proceed even if its mutex hasn't
	// been reaped yet — otherwise a setup/settings save would exit into
	// nothing and the user would have to relaunch by hand.
	if !acquireSingleInstanceLock() && os.Getenv("PHOTOSHARE_RESTART") == "" {
		log.Println("PhotoShare is already running — asking it to show its window")
		// Loopback + skip-verify so this works whether the running instance
		// serves plain HTTP or self-signed HTTPS.
		c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		c.Post(windowURL+"/api/show", "text/plain", nil)
		return
	}

	// runGUI blocks on the OS main thread: on Windows it opens the native
	// WebView2 window and the tray icon and returns when the user quits; on
	// every other platform it's a stub that just blocks forever (equivalent
	// to the old bare `select {}`), so Linux/Docker behavior is unchanged.
	// The window loads loopback (no "Not Secure"); the tray uses the LAN URL
	// for its share/copy actions.
	go startTray(netURL)
	runGUI(windowURL)
}
