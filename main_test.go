package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// ── loadConfig ───────────────────────────────────────────────────────────────

func TestLoadConfigFreshFileUsesDefaults(t *testing.T) {
	cfg := loadConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if cfg.Port != "8080" || cfg.UploadFolder != "_Uploads" {
		t.Errorf("fresh-file defaults = %+v, want defaultConfig() values", cfg)
	}
	// Must never carry a fixed default password — that's seeded randomly now.
	if cfg.AdminPass != "" {
		t.Errorf("defaultConfig seeds AdminPass=%q, want empty (no fixed default)", cfg.AdminPass)
	}
}

func TestLoadConfigDoesNotLeakStaleDefaults(t *testing.T) {
	// A real saved config never includes adminPassword/adminPasswordHash once
	// migrated to the users list — this is exactly the shape on disk after
	// onboarding completes.
	path := filepath.Join(t.TempDir(), "photoshare.config.json")
	written := AppConfig{
		PhotoDir: "/real/library/path",
		Port:     "9090",
		Users:    []User{{Username: "admin", PassHash: "somehash", Role: "admin"}},
	}
	data, _ := json.Marshal(written)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig(path)

	// Before the fix, loadConfig started from defaultConfig() and merged on
	// top, so an omitted "adminPassword" field silently resurrected the
	// "123456" default and "photoDir" would never read back wrong here, but
	// AdminPass leaking is exactly what caused a spurious resave (and nearly
	// an account reset) every single restart.
	if cfg.AdminPass != "" {
		t.Errorf("AdminPass = %q, want empty — defaultConfig() leaked into a loaded existing config", cfg.AdminPass)
	}
	if cfg.PhotoDir != "/real/library/path" {
		t.Errorf("PhotoDir = %q, want the real saved value", cfg.PhotoDir)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Username != "admin" {
		t.Errorf("Users = %+v, want the one saved admin account preserved", cfg.Users)
	}
}

func TestLoadConfigBackfillsOperationalDefaultsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photoshare.config.json")
	// Port/UploadFolder omitted entirely — should be backfilled with sane
	// operational defaults, unlike AdminPass/PhotoDir which must stay empty.
	os.WriteFile(path, []byte(`{"users":[{"username":"a","passwordHash":"h","role":"admin"}]}`), 0644)

	cfg := loadConfig(path)
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want backfilled \"8080\"", cfg.Port)
	}
	if cfg.UploadFolder != "_Uploads" {
		t.Errorf("UploadFolder = %q, want backfilled \"_Uploads\"", cfg.UploadFolder)
	}
	if cfg.PhotoDir != "" {
		t.Errorf("PhotoDir = %q, want empty (must never be backfilled)", cfg.PhotoDir)
	}
	if cfg.AdminPass != "" {
		t.Errorf("AdminPass = %q, want empty (must never be backfilled)", cfg.AdminPass)
	}
}

// ── authenticate ─────────────────────────────────────────────────────────────

func TestAuthenticate(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	usersMu.Lock()
	prevUsers := users
	users = []User{{Username: "tester", PassHash: string(hash), Role: "admin"}}
	usersMu.Unlock()
	defer func() { usersMu.Lock(); users = prevUsers; usersMu.Unlock() }()

	if _, ok := authenticate("tester", "correct-password"); !ok {
		t.Error("authenticate with correct password = false, want true")
	}
	if _, ok := authenticate("tester", "wrong-password"); ok {
		t.Error("authenticate with wrong password = true, want false")
	}
	if _, ok := authenticate("nobody", "correct-password"); ok {
		t.Error("authenticate with unknown username = true, want false")
	}
}

// ── hasDefaultAdminPassword ───────────────────────────────────────────────────

func TestHasDefaultAdminPassword(t *testing.T) {
	defaultHash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	realHash, _ := bcrypt.GenerateFromPassword([]byte("a-real-password"), bcrypt.DefaultCost)

	usersMu.Lock()
	prevUsers := users
	users = []User{{Username: "admin", PassHash: string(defaultHash), Role: "admin"}}
	usersMu.Unlock()
	if !hasDefaultAdminPassword() {
		t.Error("hasDefaultAdminPassword() = false with a default-password admin, want true")
	}

	usersMu.Lock()
	users = []User{{Username: "admin", PassHash: string(realHash), Role: "admin"}}
	usersMu.Unlock()
	if hasDefaultAdminPassword() {
		t.Error("hasDefaultAdminPassword() = true with a real-password admin, want false")
	}

	usersMu.Lock()
	users = prevUsers
	usersMu.Unlock()
}

// ── isLocalRequest ───────────────────────────────────────────────────────────

func TestIsLocalRequest(t *testing.T) {
	cases := []struct {
		remoteAddr string
		want       bool
	}{
		{"127.0.0.1:54321", true},
		{"[::1]:54321", true},
		{"203.0.113.7:54321", false}, // arbitrary public IP — not this machine
		{"10.99.99.99:54321", false}, // arbitrary LAN-looking IP not bound here
	}
	for _, c := range cases {
		r := &http.Request{RemoteAddr: c.remoteAddr}
		if got := isLocalRequest(r); got != c.want {
			t.Errorf("isLocalRequest(%q) = %v, want %v", c.remoteAddr, got, c.want)
		}
	}
}

// ── onboardingHandler ─────────────────────────────────────────────────────────

func TestOnboardingHandlerRejectsWhenSetupComplete(t *testing.T) {
	prevBaseDir := baseDir
	baseDir = "/already/configured"
	defer func() { baseDir = prevBaseDir }()

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()

	onboardingHandler(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d (setup already completed)", w.Code, http.StatusConflict)
	}
}

func TestOnboardingHandlerRejectsRemoteDuringSetup(t *testing.T) {
	prevBaseDir := baseDir
	baseDir = ""
	defer func() { baseDir = prevBaseDir }()

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", nil)
	req.RemoteAddr = "203.0.113.7:1234" // not this machine
	w := httptest.NewRecorder()

	onboardingHandler(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (remote request during setup must be rejected)", w.Code, http.StatusForbidden)
	}
}

func TestOnboardingStatusHandlerHidesNeedsSetupFromRemote(t *testing.T) {
	prevBaseDir := baseDir
	baseDir = ""
	defer func() { baseDir = prevBaseDir }()

	req := httptest.NewRequest(http.MethodGet, "/api/onboarding-status", nil)
	req.RemoteAddr = "203.0.113.7:1234" // not this machine
	w := httptest.NewRecorder()

	onboardingStatusHandler(w, req)

	var body struct {
		NeedsSetup bool `json:"needsSetup"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if body.NeedsSetup {
		t.Error("needsSetup = true for a remote request during setup, want false (must not reveal the server is unclaimed)")
	}

	// Same state, but from "this machine" — should report the real status.
	req2 := httptest.NewRequest(http.MethodGet, "/api/onboarding-status", nil)
	req2.RemoteAddr = "127.0.0.1:1234"
	w2 := httptest.NewRecorder()
	onboardingStatusHandler(w2, req2)
	var body2 struct {
		NeedsSetup bool `json:"needsSetup"`
	}
	json.NewDecoder(w2.Body).Decode(&body2)
	if !body2.NeedsSetup {
		t.Error("needsSetup = false for a local request during setup, want true")
	}
}

// ── Path boundaries ──────────────────────────────────────────────────────────

func TestSafePath(t *testing.T) {
	base := t.TempDir()
	// safePath confines everything to base: traversal is neutralized (the
	// input is anchored at base and cleaned), not necessarily errored. The
	// security property is that the result can never escape base — verify
	// that for ordinary paths and for traversal attempts alike.
	inputs := []string{
		"", ".", "a/b.jpg", "sub/dir", "weird name.png",
		"../escape", "../../etc/passwd", "sub/../../out", "/etc/passwd",
	}
	for _, rel := range inputs {
		full, err := safePath(base, rel)
		if err != nil {
			continue // rejecting outright is also acceptable
		}
		if full != base && !strings.HasPrefix(full, base+string(filepath.Separator)) {
			t.Errorf("safePath(%q) = %q, escaped base %q", rel, full, base)
		}
	}
}

func TestIsSafeFilename(t *testing.T) {
	good := []string{"photo.jpg", "Vacation_001.png", "a b c.mov", "café.heic"}
	for _, n := range good {
		if !isSafeFilename(n) {
			t.Errorf("isSafeFilename(%q) = false, want true", n)
		}
	}
	bad := []string{"", ".", "..", "a/b.jpg", `a\b.jpg`, "../x", "x/../y",
		"a:b.jpg", "q?.png", "p*.png", "le\x00gal.jpg", `na"me.jpg`}
	for _, n := range bad {
		if isSafeFilename(n) {
			t.Errorf("isSafeFilename(%q) = true, want false", n)
		}
	}
}

// trashRestoreHandler must not restore outside the library even with a
// crafted originalPath — safePath confines it inside baseDir instead.
func TestTrashRestoreConfinedToLibrary(t *testing.T) {
	prevBase, prevTrash := baseDir, trashDir
	baseDir = t.TempDir()
	trashDir = filepath.Join(baseDir, "_Trash")
	os.MkdirAll(trashDir, 0755)
	defer func() { baseDir, trashDir = prevBase, prevTrash }()

	// A real trashed file to attempt to restore.
	os.WriteFile(filepath.Join(trashDir, "evil.jpg"), []byte("x"), 0644)

	// Admin session so we get past requireAdmin to the path logic.
	usersMu.Lock()
	prevUsers := users
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.DefaultCost)
	users = []User{{Username: "admin", PassHash: string(hash), Role: "admin"}}
	usersMu.Unlock()
	defer func() { usersMu.Lock(); users = prevUsers; usersMu.Unlock() }()
	tok := sessions.create("admin", "admin")

	body := `{"name":"evil.jpg","originalPath":"../../escape.jpg"}`
	req := httptest.NewRequest(http.MethodPost, "/api/trash/restore", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	trashRestoreHandler(w, req)

	// The key property: nothing is ever written ABOVE the library, no matter
	// what originalPath says (it gets confined to baseDir instead).
	if _, err := os.Stat(filepath.Join(filepath.Dir(baseDir), "escape.jpg")); err == nil {
		t.Error("restore escaped the library — file written outside baseDir")
	}
}

// ── Authorization ────────────────────────────────────────────────────────────

func TestRequireAdminRejectsViewer(t *testing.T) {
	usersMu.Lock()
	prevUsers := users
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.DefaultCost)
	users = []User{{Username: "viewer", PassHash: string(hash), Role: "viewer"}}
	usersMu.Unlock()
	defer func() { usersMu.Lock(); users = prevUsers; usersMu.Unlock() }()
	tok := sessions.create("viewer", "viewer")

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	if requireAdmin(w, req) {
		t.Error("requireAdmin allowed a viewer, want rejected")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a viewer", w.Code)
	}

	// No session at all → 401.
	w2 := httptest.NewRecorder()
	if requireAdmin(w2, httptest.NewRequest(http.MethodGet, "/x", nil)) {
		t.Error("requireAdmin allowed an unauthenticated request, want rejected")
	}
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 with no session", w2.Code)
	}
}

// /api/quit must be local-only + POST so a remote client can't stop the app.
func TestQuitHandlerRejectsRemoteAndGet(t *testing.T) {
	// Remote POST → forbidden.
	req := httptest.NewRequest(http.MethodPost, "/api/quit", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	w := httptest.NewRecorder()
	quitHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("remote quit status = %d, want 403", w.Code)
	}

	// Local GET → forbidden (wrong method).
	req2 := httptest.NewRequest(http.MethodGet, "/api/quit", nil)
	req2.RemoteAddr = "127.0.0.1:5555"
	w2 := httptest.NewRecorder()
	quitHandler(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("local GET quit status = %d, want 403", w2.Code)
	}
}
