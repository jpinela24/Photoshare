package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jpegBytes is a minimal buffer that passes both the extension and the
// magic-byte check, so tests exercise the real acceptance path.
var jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{' '}, 64)...)

// shareRequest builds the multipart POST the Android share sheet sends.
func shareRequest(t *testing.T, files map[string][]byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		fw, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(content)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/share-target", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// shareEnv points the library at a temp dir with an inbox.
func shareEnv(t *testing.T) string {
	t.Helper()
	lib := t.TempDir()
	inbox := filepath.Join(lib, uploadDir)
	if err := os.MkdirAll(inbox, 0755); err != nil {
		t.Fatal(err)
	}
	prev := baseDir
	baseDir = lib
	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()
	t.Cleanup(func() {
		baseDir = prev
		rootMu.Lock()
		rootCacheBase, rootCacheReal = "", ""
		rootMu.Unlock()
	})
	return inbox
}

func inboxNames(t *testing.T, inbox string) []string {
	t.Helper()
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names
}

// The happy path: files land in the inbox and the browser is redirected with a
// 303 so a refresh of the landing page can't re-submit the upload.
func TestShareTargetSavesFilesForASignedInUser(t *testing.T) {
	withUsers(t, nil)
	inbox := shareEnv(t)

	req := shareRequest(t, map[string][]byte{"a.jpg": jpegBytes, "b.jpg": jpegBytes})
	req.AddCookie(sessionFor(t, "viewer"))
	req.Header.Set("Origin", "null") // what an OS-initiated share may send
	rec := httptest.NewRecorder()
	shareTargetHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "shared=2") {
		t.Errorf("Location = %q, want it to report 2 files shared", loc)
	}
	if got := inboxNames(t, inbox); len(got) != 2 {
		t.Errorf("inbox holds %v, want 2 files", got)
	}
}

// Without a session nothing may be written. Reporting success while silently
// discarding the photos would be the worst possible outcome — the user would
// believe they were backed up.
func TestShareTargetWithoutSessionSavesNothing(t *testing.T) {
	withUsers(t, nil)
	inbox := shareEnv(t)

	rec := httptest.NewRecorder()
	shareTargetHandler(rec, shareRequest(t, map[string][]byte{"a.jpg": jpegBytes}))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "shared=signin") {
		t.Errorf("Location = %q, want the sign-in prompt", loc)
	}
	if got := inboxNames(t, inbox); len(got) != 0 {
		t.Errorf("inbox holds %v, want nothing saved without a session", got)
	}
}

// The share sheet must not become a way to drop arbitrary files into the
// library: the extension and the magic bytes both have to agree.
func TestShareTargetRejectsNonMedia(t *testing.T) {
	withUsers(t, nil)
	inbox := shareEnv(t)

	req := shareRequest(t, map[string][]byte{
		"good.jpg":  jpegBytes,
		"notes.txt": []byte("plain text"),            // wrong extension
		"evil.jpg":  []byte("#!/bin/sh\nrm -rf /\n"), // right extension, wrong contents
	})
	req.AddCookie(sessionFor(t, "viewer"))
	rec := httptest.NewRecorder()
	shareTargetHandler(rec, req)

	got := inboxNames(t, inbox)
	if len(got) != 1 || got[0] != "good.jpg" {
		t.Errorf("inbox holds %v, want only good.jpg", got)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "skipped=2") {
		t.Errorf("Location = %q, want it to report 2 skipped", loc)
	}
}

// A real cross-site POST is refused. The session cookie is SameSite=Lax so it
// wouldn't carry a session anyway, but the origin check is the explicit gate.
func TestShareTargetRefusesCrossSiteOrigin(t *testing.T) {
	withUsers(t, nil)
	inbox := shareEnv(t)

	req := shareRequest(t, map[string][]byte{"a.jpg": jpegBytes})
	req.AddCookie(sessionFor(t, "admin"))
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	shareTargetHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if got := inboxNames(t, inbox); len(got) != 0 {
		t.Errorf("inbox holds %v, want nothing saved for a cross-site post", got)
	}
}

// An OS-initiated share has no Origin we control, so those must pass; only a
// genuine foreign host is refused.
func TestShareOriginOK(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"", true},                       // omitted — OS-initiated
		{"null", true},                   // opaque origin
		{"https://photos.example", true}, // matches Host below
		{"https://evil.example", false},
		{"http://photos.example.evil.com", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/share-target", nil)
		r.Host = "photos.example"
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := shareOriginOK(r); got != c.want {
			t.Errorf("shareOriginOK(Origin: %q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

// GET must not be accepted — the manifest declares POST, and a GET here would
// mean something other than the share sheet is calling it.
func TestShareTargetRejectsNonPost(t *testing.T) {
	rec := httptest.NewRecorder()
	shareTargetHandler(rec, httptest.NewRequest(http.MethodGet, "/share-target", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Errorf("Allow = %q, want POST", allow)
	}
}
