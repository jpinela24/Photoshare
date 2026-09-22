package main

// Regression tests for the security remediation: HTTP-method + CSRF enforcement,
// session revalidation/revocation, symlink containment, the guest-login
// rate-limit bypass, partial-upload cleanup, and hardened config persistence.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// ── Concurrent uploads of the same filename ──────────────────────────────────

// Two uploads called "photo.jpg" arriving together must both survive. The old
// code did os.Stat("photo.jpg") and later os.Rename onto it: both uploads saw
// the name as free, and on Unix the second rename silently replaced the first.
func TestConcurrentSameNameUploadsDoNotOverwrite(t *testing.T) {
	dest := t.TempDir()
	const n = 16

	headers := make([]*multipart.FileHeader, n)
	want := map[string]bool{}
	for i := 0; i < n; i++ {
		payload := fmt.Sprintf("payload-%02d-%s", i, strings.Repeat("x", 128))
		headers[i] = makeFileHeader(t, "photo.jpg", []byte(payload))
		want[payload] = true
	}

	paths := make([]string, n)
	errsArr := make([]error, n)
	start := make(chan struct{}) // barrier: release every goroutine at once
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			paths[i], errsArr[i] = saveUploadedFile(headers[i], dest)
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errsArr {
		if err != nil {
			t.Fatalf("upload %d failed: %v", i, err)
		}
		if seen[paths[i]] {
			t.Errorf("two uploads landed on the same path %q", paths[i])
		}
		seen[paths[i]] = true
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
			continue
		}
		b, err := os.ReadFile(filepath.Join(dest, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got[string(b)] = true
	}
	if len(entries) != n {
		t.Errorf("found %d files, want %d — an upload was overwritten", len(entries), n)
	}
	for payload := range want {
		if !got[payload] {
			t.Errorf("a payload is missing or was overwritten: %.20s…", payload)
		}
	}
	// One of them should keep the original, unsuffixed name.
	if _, err := os.Stat(filepath.Join(dest, "photo.jpg")); err != nil {
		t.Errorf("no upload kept the original filename: %v", err)
	}
}

// ── Duplicate endpoints are admin-only ───────────────────────────────────────

// sessionFor returns a cookie for a session with the given role, and registers
// the matching account so session revalidation accepts it.
func sessionFor(t *testing.T, role string) *http.Cookie {
	t.Helper()
	name := "u-" + role
	h, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	usersMu.Lock()
	users = append(users, User{Username: name, PassHash: string(h), Role: role})
	usersMu.Unlock()
	tok := sessions.create(name, role)
	t.Cleanup(func() { sessions.revoke(tok) })
	return &http.Cookie{Name: sessionCookie, Value: tok}
}

// waitForDupeScan blocks until no duplicate scan is running.
//
// dupesScanHandler starts the scan in a goroutine and returns immediately, so a
// test that POSTs to it outlives nothing — the scan keeps running into whatever
// test comes next and races it over the package globals it reads (dataDir, via
// the hash cache it writes on completion). Registered with t.Cleanup, this
// makes a test own the work it kicked off.
func waitForDupeScan(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		dupes.mu.Lock()
		running := dupes.running
		dupes.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("duplicate scan did not finish within 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDuplicateWorkEndpointsRequireAdmin(t *testing.T) {
	withUsers(t, nil)
	prevGuest := guestAccess
	guestAccess = true
	t.Cleanup(func() { guestAccess = prevGuest })

	// This test POSTs to the scan endpoint, which starts the scan in a
	// goroutine. Point dataDir somewhere disposable first — dupCachePath() is
	// relative to it, so the default writes dupe-cache.gob into the repo root
	// and leaves it there as an untracked file after every run.
	prevData := dataDir
	dataDir = t.TempDir()
	// Wait for the scan before restoring dataDir: it must not outlive this
	// test (it would race the next test's setup) and it must not still be
	// running when dataDir points back at the working tree.
	t.Cleanup(func() { waitForDupeScan(t); dataDir = prevData })

	admin := sessionFor(t, "admin")
	viewer := sessionFor(t, "viewer")
	guestTok := sessions.create("guest", "viewer")
	t.Cleanup(func() { sessions.revoke(guestTok) })
	guest := &http.Cookie{Name: sessionCookie, Value: guestTok}

	work := map[string]http.HandlerFunc{
		"/api/duplicates/scan":    dupesScanHandler,
		"/api/duplicates/folder":  dupesFolderHandler,
		"/api/duplicates/cancel":  dupesCancelHandler,
		"/api/duplicates/resolve": dupesResolveHandler,
	}
	for path, h := range work {
		for _, c := range []struct {
			who    string
			cookie *http.Cookie
			want   int
		}{
			{"anonymous", nil, http.StatusUnauthorized},
			{"viewer", viewer, http.StatusForbidden},
			{"guest", guest, http.StatusForbidden},
		} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			if c.cookie != nil {
				req.AddCookie(c.cookie)
			}
			w := httptest.NewRecorder()
			h(w, req)
			if w.Code != c.want {
				t.Errorf("%s as %s: status = %d, want %d", path, c.who, w.Code, c.want)
			}
		}
		// An admin gets past authorization (any non-401/403 outcome is fine —
		// the handler's own validation may still reject the empty body).
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.AddCookie(admin)
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			t.Errorf("%s as admin: status = %d, want authorization to pass", path, w.Code)
		}
	}
}

// Reading status must never kick off a scan — that is what made an expensive
// library-wide hash reachable by any viewer with a GET.
func TestDuplicateStatusGetDoesNotStartScan(t *testing.T) {
	dupes.mu.Lock()
	dupes.running, dupes.finishedAt = false, time.Time{}
	dupes.groups, dupes.similar = nil, nil
	dupes.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/duplicates", nil)
	w := httptest.NewRecorder()
	duplicatesHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	dupes.mu.Lock()
	running := dupes.running
	dupes.mu.Unlock()
	if running {
		t.Error("a status GET started a scan")
	}
}

// The work endpoints go through the shared POST + same-origin middleware.
func TestDuplicateWorkEndpointsEnforceMethodAndOrigin(t *testing.T) {
	for _, path := range []string{
		"/api/duplicates/scan", "/api/duplicates/folder",
		"/api/duplicates/cancel", "/api/duplicates/resolve",
	} {
		guarded := mutate(http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("%s: handler ran when it should have been blocked", path)
		})

		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		guarded(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: GET = %d, want 405", path, w.Code)
		}

		req2 := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req2.Host = "photos.local"
		req2.Header.Set("Origin", "http://evil.example")
		w2 := httptest.NewRecorder()
		guarded(w2, req2)
		if w2.Code != http.StatusForbidden {
			t.Errorf("%s: cross-origin POST = %d, want 403", path, w2.Code)
		}
	}
}

// ── Transactional config updates ─────────────────────────────────────────────
//
// configMu used to cover only the final write, so a settings save and a user
// change could each load the same snapshot and the loser's edit vanished.

// configTestEnv points config persistence at a throwaway file.
func configTestEnv(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "photoshare.config.json")
	prevPath, prevGuest := configFilePath, guestAccess
	configFilePath = path
	usersMu.Lock()
	prevUsers := users
	users = nil
	usersMu.Unlock()
	t.Cleanup(func() {
		configFilePath, guestAccess = prevPath, prevGuest
		usersMu.Lock()
		users = prevUsers
		usersMu.Unlock()
	})
	return path
}

func readConfig(t *testing.T, path string) AppConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config unreadable: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if fi, err := os.Stat(path); err == nil && runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0600 {
			t.Errorf("config perms = %v, want 0600", fi.Mode().Perm())
		}
	}
	return cfg
}

// addUser mutates the in-memory list the way the handlers do, then persists.
func addUser(t *testing.T, name string) error {
	t.Helper()
	h, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	usersMu.Lock()
	users = append(users, User{Username: name, PassHash: string(h), Role: "viewer"})
	usersMu.Unlock()
	return persistUsers()
}

func TestSettingsSaveConcurrentWithUserCreationKeepsBoth(t *testing.T) {
	path := configTestEnv(t)
	if err := saveConfig(path, AppConfig{Port: "8080", ShareName: "before"}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var settingsErr, userErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		settingsErr = applySettings(path, AppConfig{Port: "9090", ShareName: "after"})
	}()
	go func() { defer wg.Done(); <-start; userErr = addUser(t, "newcomer") }()
	close(start)
	wg.Wait()

	if settingsErr != nil || userErr != nil {
		t.Fatalf("settings=%v user=%v", settingsErr, userErr)
	}
	cfg := readConfig(t, path)
	if cfg.ShareName != "after" || cfg.Port != "9090" {
		t.Errorf("settings change lost: ShareName=%q Port=%q", cfg.ShareName, cfg.Port)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Username != "newcomer" {
		t.Errorf("user change lost: %+v", cfg.Users)
	}
}

func TestSettingsSaveConcurrentWithGuestAccessKeepsBoth(t *testing.T) {
	path := configTestEnv(t)
	guestAccess = false
	if err := saveConfig(path, AppConfig{Port: "8080"}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var sErr, gErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		sErr = applySettings(path, AppConfig{Port: "8080", ShareName: "memories"})
	}()
	go func() { defer wg.Done(); <-start; setGuestAccess(true); gErr = persistUsers() }()
	close(start)
	wg.Wait()

	if sErr != nil || gErr != nil {
		t.Fatalf("settings=%v guest=%v", sErr, gErr)
	}
	cfg := readConfig(t, path)
	if cfg.ShareName != "memories" {
		t.Errorf("settings change lost: ShareName=%q", cfg.ShareName)
	}
	if !cfg.GuestAccess {
		t.Error("guest-access change lost")
	}
}

func TestConcurrentUserChangesLoseNoAccounts(t *testing.T) {
	path := configTestEnv(t)
	if err := saveConfig(path, AppConfig{Port: "8080"}); err != nil {
		t.Fatal(err)
	}

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errsArr := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errsArr[i] = addUser(t, fmt.Sprintf("user%02d", i))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errsArr {
		if err != nil {
			t.Fatalf("persist %d failed: %v", i, err)
		}
	}
	cfg := readConfig(t, path)
	if len(cfg.Users) != n {
		t.Errorf("config has %d accounts, want %d — a concurrent change was lost", len(cfg.Users), n)
	}
}

// A failed write must not leave the running server believing a change landed.
func TestFailedPersistenceRollsBackRuntimeState(t *testing.T) {
	configTestEnv(t)
	// Point at a directory that does not exist so the atomic write cannot even
	// create its temp file.
	configFilePath = filepath.Join(t.TempDir(), "no-such-dir", "photoshare.config.json")

	withUsers(t, []User{adminUser(t, "admin")})
	tok := sessions.create("admin", "admin")
	t.Cleanup(func() { sessions.revoke(tok) })

	body := `{"username":"ghost","password":"pw","role":"viewer"}`
	req := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	usersSaveHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when persistence fails", w.Code)
	}
	if _, idx := findUser("ghost"); idx >= 0 {
		t.Error("runtime state kept an account that was never written to disk")
	}
}

func TestFailedGuestAccessPersistenceRollsBack(t *testing.T) {
	configTestEnv(t)
	configFilePath = filepath.Join(t.TempDir(), "no-such-dir", "photoshare.config.json")
	guestAccess = false

	withUsers(t, []User{adminUser(t, "admin")})
	tok := sessions.create("admin", "admin")
	t.Cleanup(func() { sessions.revoke(tok) })

	req := httptest.NewRequest(http.MethodPost, "/api/guest-access", strings.NewReader(`{"enabled":true}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	guestAccessHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when persistence fails", w.Code)
	}
	if getGuestAccess() {
		t.Error("guestAccess stayed enabled in memory after the write failed")
	}
}

// ── Batch move reports exactly what left the folder ──────────────────────────

// The grid drops a card as soon as the server says that file moved, so `moved`
// has to be the truth: a path that failed, or that was already sitting in the
// destination, must not appear in it. If it did, the card would vanish from the
// UI while the file is still on disk in the original folder.
func TestBatchMoveReportsOnlyFilesThatLeft(t *testing.T) {
	withUsers(t, nil)
	lib := t.TempDir()
	prevBase := baseDir
	baseDir = lib
	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()
	t.Cleanup(func() {
		baseDir = prevBase
		rootMu.Lock()
		rootCacheBase, rootCacheReal = "", ""
		rootMu.Unlock()
	})

	if err := os.MkdirAll(filepath.Join(lib, "Album"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(lib, "Dest"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Album/a.jpg", "Album/b.jpg", "Dest/c.jpg"} {
		if err := os.WriteFile(filepath.Join(lib, filepath.FromSlash(p)), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Mix a real move, a file already at the destination, and a bogus path.
	body := `{"paths":["Album/a.jpg","Dest/c.jpg","Album/missing.jpg"],"destFolder":"Dest"}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/batch/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionFor(t, "admin"))
	rec := httptest.NewRecorder()
	adminBatchMoveHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Errors []string `json:"errors"`
		Moved  []string `json:"moved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(got.Moved) != 1 || got.Moved[0] != "Album/a.jpg" {
		t.Errorf("moved = %v, want exactly [Album/a.jpg]", got.Moved)
	}
	if len(got.Errors) != 1 {
		t.Errorf("errors = %v, want one entry for the missing file", got.Errors)
	}

	// Every path the client would keep on screen must still be where it was.
	for _, p := range []string{"Album/b.jpg", "Dest/c.jpg"} {
		if _, err := os.Stat(filepath.Join(lib, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s should still exist: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(lib, "Dest", "a.jpg")); err != nil {
		t.Errorf("Album/a.jpg was reported moved but is not in Dest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "Album", "a.jpg")); !os.IsNotExist(err) {
		t.Error("Album/a.jpg was reported moved but the original is still there")
	}
}
