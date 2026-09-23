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

func moveBatch(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/batch/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionFor(t, "admin"))
	rec := httptest.NewRecorder()
	adminBatchMoveHandler(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errorsOf(out map[string]any) string {
	errs, _ := out["errors"].([]any)
	var b []string
	for _, e := range errs {
		b = append(b, e.(string))
	}
	return strings.Join(b, "; ")
}

// Moving a folder into itself or its own subtree would nest it inside itself.
// os.Rename usually refuses, but the error differs by platform and the damage
// is bad enough to check for explicitly.
func TestBatchMoveRefusesFolderIntoItself(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t) // gives a temp library + isolated favorites
	mkPhoto(t, lib, "Trip/a.jpg")
	mkPhoto(t, lib, "Trip/Inner/b.jpg")

	for _, dest := range []string{"Trip", "Trip/Inner"} {
		_, out := moveBatch(t, `{"paths":["Trip"],"destFolder":"`+dest+`"}`)
		if got := errorsOf(out); !strings.Contains(got, "into itself") {
			t.Errorf("moving Trip into %q: errors = %q, want a refusal", dest, got)
		}
		if moved, _ := out["moved"].([]any); len(moved) != 0 {
			t.Errorf("moving Trip into %q reported %v as moved", dest, moved)
		}
		if _, err := os.Stat(filepath.Join(lib, "Trip", "a.jpg")); err != nil {
			t.Fatalf("Trip was disturbed by a refused move: %v", err)
		}
	}
}

// A folder move carries its whole subtree.
func TestBatchMoveFolderKeepsContents(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	mkPhoto(t, lib, "Trip/a.jpg")
	mkPhoto(t, lib, "Trip/Inner/b.jpg")
	if err := os.MkdirAll(filepath.Join(lib, "Dest"), 0755); err != nil {
		t.Fatal(err)
	}

	_, out := moveBatch(t, `{"paths":["Trip"],"destFolder":"Dest"}`)
	if got := errorsOf(out); got != "" {
		t.Fatalf("unexpected errors: %s", got)
	}
	for _, p := range []string{"Dest/Trip/a.jpg", "Dest/Trip/Inner/b.jpg"} {
		if _, err := os.Stat(filepath.Join(lib, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s missing after the folder move: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(lib, "Trip")); !os.IsNotExist(err) {
		t.Error("the folder is still at its old location")
	}
}

// Stars are keyed by full path, so a folder move has to re-point every one
// underneath it or they are silently dropped at the next prune.
func TestFolderMoveCarriesFavorites(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	mkPhoto(t, lib, "Trip/a.jpg")
	mkPhoto(t, lib, "Trip/Inner/b.jpg")
	mkPhoto(t, lib, "Trips-other/c.jpg") // name shares a prefix: must not move
	if err := os.MkdirAll(filepath.Join(lib, "Dest"), 0755); err != nil {
		t.Fatal(err)
	}
	c := sessionFor(t, "admin")
	for _, p := range []string{"Trip/a.jpg", "Trip/Inner/b.jpg", "Trips-other/c.jpg"} {
		if code := postFav(t, c, `{"path":"`+p+`","favorite":true}`); code != http.StatusOK {
			t.Fatalf("star %s = %d", p, code)
		}
	}

	moveBatch(t, `{"paths":["Trip"],"destFolder":"Dest"}`)

	got := listFav(t, c)
	want := map[string]bool{"Dest/Trip/a.jpg": true, "Dest/Trip/Inner/b.jpg": true, "Trips-other/c.jpg": true}
	if len(got) != len(want) {
		t.Fatalf("favorites = %v, want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected favorite %q — want %v", p, want)
		}
	}
}

// The prefix rewrite must match on path segments, not raw string prefixes, or
// "Trips-other" would be dragged along by a move of "Trip".
func TestRenameFavoritePrefixIsSegmentAware(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	mkPhoto(t, lib, "Trip/a.jpg")
	mkPhoto(t, lib, "Trips-other/c.jpg")
	c := sessionFor(t, "admin")
	postFav(t, c, `{"path":"Trip/a.jpg","favorite":true}`)
	postFav(t, c, `{"path":"Trips-other/c.jpg","favorite":true}`)

	renameFavoritePrefix("Trip", "Moved/Trip")

	favMu.Lock()
	set := favs["u-admin"]
	has := func(p string) bool { return set[p] }
	favMu.Unlock()
	if !has("Moved/Trip/a.jpg") {
		t.Error("the starred file under Trip did not follow the move")
	}
	if !has("Trips-other/c.jpg") {
		t.Error("a sibling whose name merely starts with Trip was rewritten too")
	}
}
