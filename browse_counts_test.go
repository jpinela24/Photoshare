package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Folder cards show these counts, so they have to describe the folder itself —
// not the whole subtree, and not counting the housekeeping folders the browser
// hides.
func TestBrowseReportsFolderCounts(t *testing.T) {
	withUsers(t, nil)
	lib := t.TempDir()
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

	mk := func(p string) {
		t.Helper()
		full := filepath.Join(lib, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mk("Trip/a.jpg")
	mk("Trip/b.jpg")
	mk("Trip/c.mp4")
	mk("Trip/notes.txt")    // not media — must not be counted
	mk("Trip/Deeper/d.jpg") // nested — must not inflate Trip's photo count
	if err := os.MkdirAll(filepath.Join(lib, "Trip", "thumbs"), 0755); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/browse?path=", nil)
	req.AddCookie(sessionFor(t, "viewer"))
	rec := httptest.NewRecorder()
	browseHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Photos != 2 {
		t.Errorf("photos = %d, want 2 (the nested one must not count)", e.Photos)
	}
	if e.Videos != 1 {
		t.Errorf("videos = %d, want 1", e.Videos)
	}
	// "thumbs" is a hidden housekeeping folder and must not be counted.
	if e.Folders != 1 {
		t.Errorf("folders = %d, want 1 (thumbs is hidden)", e.Folders)
	}
}

// Counting is best-effort: an unreadable folder must still list, just without
// a caption. A listing is not worth failing over a count.
func TestCountChildrenToleratesMissingDir(t *testing.T) {
	p, v, f := countChildren(filepath.Join(t.TempDir(), "does-not-exist"))
	if p != 0 || v != 0 || f != 0 {
		t.Errorf("got %d/%d/%d, want zeros for a missing directory", p, v, f)
	}
}
