package main

// Regression tests for the security remediation: HTTP-method + CSRF enforcement,
// session revalidation/revocation, symlink containment, the guest-login
// rate-limit bypass, partial-upload cleanup, and hardened config persistence.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// withUsers swaps the global user list for the duration of a test.
func withUsers(t *testing.T, u []User) {
	t.Helper()
	usersMu.Lock()
	prev := users
	users = u
	usersMu.Unlock()
	t.Cleanup(func() { usersMu.Lock(); users = prev; usersMu.Unlock() })
}

func adminUser(t *testing.T, name string) User {
	t.Helper()
	h, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	return User{Username: name, PassHash: string(h), Role: "admin"}
}

// ── 1. Method enforcement + CSRF ─────────────────────────────────────────────

func TestMutateEnforcesMethod(t *testing.T) {
	called := false
	h := mutate(http.MethodDelete, func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/api/admin/delete?path=x", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on a DELETE endpoint = %d, want 405", w.Code)
	}
	if got := w.Header().Get("Allow"); got != http.MethodDelete {
		t.Errorf("Allow header = %q, want DELETE", got)
	}
	if called {
		t.Error("handler ran for a disallowed method — a GET must never reach a mutation")
	}
}

func TestMutateRejectsCrossOrigin(t *testing.T) {
	h := mutate(http.MethodPost, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	cases := []struct {
		name, origin string
		host         string
		want         int
	}{
		{"same origin", "http://photos.local", "photos.local", http.StatusOK},
		{"cross origin", "http://evil.example", "photos.local", http.StatusForbidden},
		{"no origin (non-browser)", "", "photos.local", http.StatusOK},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/upload", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, w.Code, c.want)
		}
	}
}

// The two endpoints the audit specifically called out: a GET must not be able to
// alter files. mutate() short-circuits with 405 before the handler runs.
func TestDangerousDeletesRejectGET(t *testing.T) {
	for _, tc := range []struct {
		path    string
		method  string
		handler http.HandlerFunc
	}{
		{"/api/admin/delete", http.MethodDelete, adminDeleteHandler},
		{"/api/trash/purge-all", http.MethodDelete, trashPurgeAllHandler},
	} {
		ran := false
		guarded := mutate(tc.method, func(w http.ResponseWriter, r *http.Request) { ran = true })
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		guarded(w, req)
		if w.Code != http.StatusMethodNotAllowed || ran {
			t.Errorf("GET %s reached the handler (code %d) — must be blocked", tc.path, w.Code)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, referer, host string
		want                  bool
	}{
		{"", "", "h", true},                             // neither header → allowed
		{"http://h", "", "h", true},                     // origin matches
		{"http://h:8088", "", "h:8088", true},           // host+port matches
		{"http://other", "", "h", false},                // origin mismatch
		{"", "http://h/page", "h", true},                // referer fallback matches
		{"", "http://other/page", "h", false},           // referer mismatch
		{"garbage", "", "h", false},                     // unparseable origin
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.referer != "" {
			req.Header.Set("Referer", c.referer)
		}
		if got := sameOrigin(req); got != c.want {
			t.Errorf("sameOrigin(origin=%q referer=%q host=%q) = %v, want %v", c.origin, c.referer, c.host, got, c.want)
		}
	}
}

// ── 2. Session revalidation + revocation ─────────────────────────────────────

func TestResolveSessionRejectsDeletedUser(t *testing.T) {
	withUsers(t, []User{adminUser(t, "alice")})
	tok := sessions.create("alice", "admin")
	defer sessions.revoke(tok)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	if _, ok := resolveSession(req); !ok {
		t.Fatal("valid admin session rejected before deletion")
	}

	// Delete the account; the still-live token must stop authorizing.
	withUsers(t, []User{})
	if _, ok := resolveSession(req); ok {
		t.Error("session for a deleted user still resolves — deletion must revoke access")
	}
}

func TestResolveSessionUsesLiveRole(t *testing.T) {
	withUsers(t, []User{adminUser(t, "bob")})
	tok := sessions.create("bob", "admin") // logged in as admin
	defer sessions.revoke(tok)

	// Demote bob to viewer in the live list; the admin token must lose admin.
	h, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	withUsers(t, []User{{Username: "bob", PassHash: string(h), Role: "viewer"}})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	sess, ok := resolveSession(req)
	if !ok || sess.Role != "viewer" {
		t.Errorf("resolveSession role = %v (ok=%v), want live role 'viewer'", sess, ok)
	}
	w := httptest.NewRecorder()
	if requireAdmin(w, req) {
		t.Error("requireAdmin still granted admin to a demoted user")
	}
}

func TestRevokeUser(t *testing.T) {
	withUsers(t, []User{adminUser(t, "carol"), adminUser(t, "dave")})
	tokC := sessions.create("carol", "admin")
	tokD := sessions.create("dave", "admin")
	defer sessions.revoke(tokD)

	sessions.revokeUser("carol")
	if _, ok := sessions.get(tokC); ok {
		t.Error("carol's session survived revokeUser")
	}
	if _, ok := sessions.get(tokD); !ok {
		t.Error("dave's session was wrongly revoked")
	}
}

func TestGuestSessionHonorsToggle(t *testing.T) {
	prev := guestAccess
	defer func() { guestAccess = prev }()

	tok := sessions.create("guest", "viewer")
	defer sessions.revoke(tok)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})

	guestAccess = true
	if _, ok := resolveSession(req); !ok {
		t.Error("guest session rejected while guest access is enabled")
	}
	guestAccess = false
	if _, ok := resolveSession(req); ok {
		t.Error("guest session still valid after guest access disabled")
	}
}

// ── 3. Symlink containment ───────────────────────────────────────────────────

func TestSafePathBlocksSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	lib := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.jpg"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	prevBase := baseDir
	baseDir = lib
	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()
	defer func() {
		baseDir = prevBase
		rootMu.Lock()
		rootCacheBase, rootCacheReal = "", ""
		rootMu.Unlock()
	}()

	// A symlink inside the library pointing OUT must be refused.
	if err := os.Symlink(outside, filepath.Join(lib, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := safePath(lib, "escape/secret.jpg"); err == nil {
		t.Error("safePath followed a symlink out of the library — escape not blocked")
	}

	// A symlink whose target stays inside the library is allowed.
	inner := filepath.Join(lib, "real")
	os.MkdirAll(inner, 0755)
	os.WriteFile(filepath.Join(inner, "ok.jpg"), []byte("x"), 0644)
	if err := os.Symlink(inner, filepath.Join(lib, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := safePath(lib, "alias/ok.jpg"); err != nil {
		t.Errorf("safePath rejected an in-library symlink: %v", err)
	}
}

func TestPhotoHandlerRejectsNonMedia(t *testing.T) {
	lib := t.TempDir()
	os.WriteFile(filepath.Join(lib, "config.txt"), []byte("secret"), 0644)
	prevBase := baseDir
	baseDir = lib
	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()
	defer func() {
		baseDir = prevBase
		rootMu.Lock()
		rootCacheBase, rootCacheReal = "", ""
		rootMu.Unlock()
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/photo?path=config.txt", nil)
	w := httptest.NewRecorder()
	photoHandler(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("photoHandler served a non-media file (code %d), want 404", w.Code)
	}
}

// ── 4. Guest login rate-limit bypass ─────────────────────────────────────────

func TestGuestLoginDoesNotResetLockout(t *testing.T) {
	prevDelay := loginFailDelay
	loginFailDelay = 0
	defer func() { loginFailDelay = prevDelay }()

	prevGuest := guestAccess
	guestAccess = true
	defer func() { guestAccess = prevGuest }()

	withUsers(t, []User{adminUser(t, "admin")}) // password is "pw"; wrong pw below
	const ip = "198.51.100.42:1111"

	post := func(t *testing.T, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
		req.RemoteAddr = ip
		w := httptest.NewRecorder()
		loginHandler(w, req)
		return w
	}

	// Reset any leftover state for this IP.
	logins.reset("198.51.100.42")

	for i := 0; i < maxLoginAttempts-1; i++ { // 4 failed password attempts
		post(t, `{"username":"admin","password":"nope"}`)
	}
	// A successful guest login must NOT clear the failed-password counter.
	if w := post(t, `{"guest":true}`); w.Code != http.StatusOK {
		t.Fatalf("guest login status = %d, want 200", w.Code)
	}
	// The 5th failed password attempt must now trip the lockout.
	if w := post(t, `{"username":"admin","password":"nope"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("5th failed attempt status = %d, want 401", w.Code)
	}
	if !logins.locked("198.51.100.42") {
		t.Error("IP not locked out — guest login wrongly reset the brute-force counter")
	}
	logins.reset("198.51.100.42")
}

// ── 5. Partial upload cleanup ────────────────────────────────────────────────

// makeFileHeader builds a real *multipart.FileHeader wrapping the given bytes.
func makeFileHeader(t *testing.T, name string, data []byte) *multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("files", name)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(data)
	mw.Close()
	r := multipart.NewReader(&buf, mw.Boundary())
	form, err := r.ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	return form.File["files"][0]
}

func TestSaveUploadedFileCleansUpOnCopyFailure(t *testing.T) {
	prevCopy := uploadCopy
	uploadCopy = func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("disk full") }
	defer func() { uploadCopy = prevCopy }()

	dest := t.TempDir()
	fh := makeFileHeader(t, "photo.jpg", []byte("imagedata"))

	if _, err := saveUploadedFile(fh, dest); err == nil {
		t.Fatal("saveUploadedFile returned nil error on a failed copy")
	}
	// No final file and no leftover temp file.
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("destDir not clean after failed upload: %v", names)
	}
}

func TestSaveUploadedFileSuccessLeavesNoTemp(t *testing.T) {
	dest := t.TempDir()
	fh := makeFileHeader(t, "photo.jpg", []byte("imagedata"))
	got, err := saveUploadedFile(fh, dest)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "photo.jpg" {
		t.Errorf("dest = %q, want .../photo.jpg", got)
	}
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after successful upload: %s", e.Name())
		}
	}
}

// ── 5b. Content-based media validation ───────────────────────────────────────

func TestLooksLikeMedia(t *testing.T) {
	pad := func(prefix []byte) []byte { // pad to >=12 bytes
		b := make([]byte, 32)
		copy(b, prefix)
		return b
	}
	media := map[string][]byte{
		"jpeg": pad([]byte{0xFF, 0xD8, 0xFF, 0xE0}),
		"png":  pad([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}),
		"gif":  pad([]byte("GIF89a")),
		"webp": append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 8)...),
		"heic": append([]byte{0, 0, 0, 0}, append([]byte("ftypheic"), make([]byte, 8)...)...),
		"mp4":  append([]byte{0, 0, 0, 0}, append([]byte("ftypisom"), make([]byte, 8)...)...),
		"mkv":  pad([]byte{0x1A, 0x45, 0xDF, 0xA3}),
	}
	for name, b := range media {
		if !looksLikeMedia(b) {
			t.Errorf("looksLikeMedia(%s) = false, want true", name)
		}
	}
	notMedia := map[string][]byte{
		"html":       []byte("<!DOCTYPE html><script>alert(1)</script>"),
		"elf":        pad([]byte{0x7F, 'E', 'L', 'F'}),
		"pe":         pad([]byte{'M', 'Z', 0x90, 0x00}),
		"zip":        pad([]byte{'P', 'K', 0x03, 0x04}),
		"pdf":        pad([]byte("%PDF-1.7")),
		"plain text": []byte("just some words in a file, not media at all"),
		"too short":  []byte{0xFF, 0xD8},
	}
	for name, b := range notMedia {
		if looksLikeMedia(b) {
			t.Errorf("looksLikeMedia(%s) = true, want false", name)
		}
	}
}

func TestHasMediaMagicRejectsDisguisedFile(t *testing.T) {
	// A malicious HTML page uploaded as "photo.jpg" passes the extension check
	// but must fail the content sniff.
	evil := makeFileHeader(t, "photo.jpg", []byte("<html><script>alert(document.cookie)</script></html>"))
	if hasMediaMagic(evil) {
		t.Error("hasMediaMagic accepted an HTML file disguised as .jpg")
	}
	good := makeFileHeader(t, "photo.jpg", append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...))
	if !hasMediaMagic(good) {
		t.Error("hasMediaMagic rejected a real JPEG")
	}
}

// ── 6. Config persistence hardening ──────────────────────────────────────────

func TestSaveConfigPermissionsAndAtomicity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photoshare.config.json")
	if err := saveConfig(path, AppConfig{Port: "8080", NotifyURL: "https://ntfy.example/token-secret"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		t.Errorf("config perms = %v, want 0600 (may hold secret webhook tokens)", fi.Mode().Perm())
	}
	// No stray temp file left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp config: %s", e.Name())
		}
	}
	// Round-trips as valid JSON.
	var back AppConfig
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
}

func TestConcurrentSaveConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photoshare.config.json")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			saveConfig(path, AppConfig{Port: "8080", ShareName: time.Now().String()})
		}(i)
	}
	wg.Wait()
	// After all the racing writes, the file must still be complete valid JSON
	// (atomic rename guarantees a reader never sees a half-written file).
	var back AppConfig
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("config corrupted by concurrent writes: %v", err)
	}
}
