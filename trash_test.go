package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trashEnv gives the test its own library and trash directory.
func trashEnv(t *testing.T) string {
	t.Helper()
	lib := t.TempDir()
	prevBase, prevTrash := baseDir, trashDir
	baseDir = lib
	trashDir = filepath.Join(lib, "_Trash")
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		t.Fatal(err)
	}
	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()
	t.Cleanup(func() {
		baseDir, trashDir = prevBase, prevTrash
		rootMu.Lock()
		rootCacheBase, rootCacheReal = "", ""
		rootMu.Unlock()
	})
	return lib
}

// inTrash drops a file into the trash directory as if it had been deleted.
func inTrash(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(trashDir, name), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
}

func restore(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/trash/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionFor(t, "admin"))
	rec := httptest.NewRecorder()
	trashRestoreHandler(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The Recycle Bin restores a selection in one request, and a single failure
// must not abandon the rest of the batch.
func TestTrashRestoreBatchIsPartial(t *testing.T) {
	withUsers(t, nil)
	lib := trashEnv(t)
	inTrash(t, "a.jpg")
	inTrash(t, "b.jpg")

	code, out := restore(t, `{"items":[
		{"name":"a.jpg","originalPath":"Album/a.jpg"},
		{"name":"b.jpg","originalPath":"Album/b.jpg"},
		{"name":"ghost.jpg","originalPath":"Album/ghost.jpg"}
	]}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := out["restored"]; got != float64(2) {
		t.Errorf("restored = %v, want 2", got)
	}
	if errs, _ := out["errors"].([]any); len(errs) != 1 {
		t.Errorf("errors = %v, want one entry for the missing file", out["errors"])
	}
	for _, p := range []string{"Album/a.jpg", "Album/b.jpg"} {
		if _, err := os.Stat(filepath.Join(lib, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s was reported restored but isn't there: %v", p, err)
		}
	}
}

// The single-item shape predates the batch one and is still accepted.
func TestTrashRestoreSingleStillWorks(t *testing.T) {
	withUsers(t, nil)
	lib := trashEnv(t)
	inTrash(t, "solo.jpg")

	code, out := restore(t, `{"name":"solo.jpg","originalPath":"One/solo.jpg"}`)
	if code != http.StatusOK || out["restored"] != float64(1) {
		t.Fatalf("status = %d, restored = %v, want 200 and 1", code, out["restored"])
	}
	if _, err := os.Stat(filepath.Join(lib, "One", "solo.jpg")); err != nil {
		t.Errorf("single restore didn't land: %v", err)
	}
}

// originalPath comes from the client, so a crafted one must not be able to
// write outside the library — this is the only thing standing between the
// Recycle Bin and an arbitrary file write.
func TestTrashRestoreCannotEscapeTheLibrary(t *testing.T) {
	withUsers(t, nil)
	lib := trashEnv(t)
	inTrash(t, "evil.jpg")

	outside := filepath.Join(t.TempDir(), "pwned.jpg")
	restore(t, `{"items":[{"name":"evil.jpg","originalPath":"../../../../`+filepath.ToSlash(outside)+`"}]}`)

	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("restore escaped the library and wrote %s", outside)
	}
	// Whatever happened, it must have stayed inside the library.
	escaped := true
	filepath.Walk(lib, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, "pwned.jpg") {
			escaped = false
		}
		return nil
	})
	if escaped {
		if _, err := os.Stat(filepath.Join(trashDir, "evil.jpg")); err != nil {
			t.Error("the file left the trash but landed nowhere inside the library")
		}
	}
}

// Purge takes a repeated ?file= so a selection can be emptied in one request,
// and must confine itself to the trash directory.
func TestTrashPurgeBatch(t *testing.T) {
	withUsers(t, nil)
	lib := trashEnv(t)
	inTrash(t, "x.jpg")
	inTrash(t, "y.jpg")
	inTrash(t, "keep.jpg")
	// A file outside the trash that a crafted name might try to reach.
	bystander := filepath.Join(lib, "bystander.jpg")
	os.WriteFile(bystander, []byte("keep me"), 0644)

	req := httptest.NewRequest(http.MethodDelete,
		"/api/trash/purge?file=x.jpg&file=y.jpg&file=../bystander.jpg&file=ghost.jpg", nil)
	req.AddCookie(sessionFor(t, "admin"))
	rec := httptest.NewRecorder()
	trashPurgeHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	// x and y only: the traversal is confined by Base() and lands on a name
	// that isn't there, and the ghost never existed.
	if out["purged"] != float64(2) {
		t.Errorf("purged = %v, want 2 (only the two real trash files)", out["purged"])
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Error("purge reached a file outside the trash directory")
	}
	if _, err := os.Stat(filepath.Join(trashDir, "keep.jpg")); err != nil {
		t.Error("purge removed an item that wasn't asked for")
	}
}

// Restoring and purging both rewrite the library, so they're admin-only.
func TestTrashBatchRequiresAdmin(t *testing.T) {
	withUsers(t, nil)
	trashEnv(t)
	inTrash(t, "a.jpg")

	req := httptest.NewRequest(http.MethodPost, "/api/trash/restore",
		strings.NewReader(`{"items":[{"name":"a.jpg","originalPath":"a.jpg"}]}`))
	req.AddCookie(sessionFor(t, "viewer"))
	rec := httptest.NewRecorder()
	trashRestoreHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer restore = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(trashDir, "a.jpg")); err != nil {
		t.Error("a viewer's request moved the file anyway")
	}
}
