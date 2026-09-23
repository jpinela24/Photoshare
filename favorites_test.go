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

// favEnv gives each test its own library and favorites file.
func favEnv(t *testing.T) string {
	t.Helper()
	lib := t.TempDir()
	prevBase, prevData := baseDir, dataDir
	baseDir, dataDir = lib, t.TempDir()
	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()

	favMu.Lock()
	favs, favLoaded = nil, false
	favMu.Unlock()

	t.Cleanup(func() {
		baseDir, dataDir = prevBase, prevData
		rootMu.Lock()
		rootCacheBase, rootCacheReal = "", ""
		rootMu.Unlock()
		favMu.Lock()
		favs, favLoaded = nil, false
		favMu.Unlock()
	})
	return lib
}

func mkPhoto(t *testing.T, lib, rel string) {
	t.Helper()
	full := filepath.Join(lib, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("photo"), 0644); err != nil {
		t.Fatal(err)
	}
}

func postFav(t *testing.T, cookie *http.Cookie, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/favorites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	favoritesSetHandler(rec, req)
	return rec.Code
}

func listFav(t *testing.T, cookie *http.Cookie) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/favorites", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	favoritesListHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	var got struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(got.Items))
	for _, i := range got.Items {
		out = append(out, i.Path)
	}
	return out
}

// Favorites are per account and survive a restart — that is the whole point of
// moving them off localStorage.
func TestFavoritesArePerUserAndPersist(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	mkPhoto(t, lib, "a.jpg")
	mkPhoto(t, lib, "b.jpg")

	alice := sessionFor(t, "admin")
	bob := sessionFor(t, "viewer")

	if code := postFav(t, alice, `{"path":"a.jpg","favorite":true}`); code != http.StatusOK {
		t.Fatalf("star a.jpg = %d", code)
	}
	if code := postFav(t, bob, `{"path":"b.jpg","favorite":true}`); code != http.StatusOK {
		t.Fatalf("star b.jpg = %d", code)
	}

	if got := listFav(t, alice); len(got) != 1 || got[0] != "a.jpg" {
		t.Errorf("alice sees %v, want [a.jpg] — favorites must not leak between users", got)
	}
	if got := listFav(t, bob); len(got) != 1 || got[0] != "b.jpg" {
		t.Errorf("bob sees %v, want [b.jpg]", got)
	}

	// Drop the in-memory copy: a restart must not lose anything.
	favMu.Lock()
	favs, favLoaded = nil, false
	favMu.Unlock()
	if got := listFav(t, alice); len(got) != 1 || got[0] != "a.jpg" {
		t.Errorf("after reload alice sees %v, want [a.jpg]", got)
	}
}

// Unstarring removes it; the file itself is untouched.
func TestFavoritesUnstar(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	mkPhoto(t, lib, "a.jpg")
	c := sessionFor(t, "admin")

	postFav(t, c, `{"path":"a.jpg","favorite":true}`)
	postFav(t, c, `{"path":"a.jpg","favorite":false}`)
	if got := listFav(t, c); len(got) != 0 {
		t.Errorf("got %v, want empty after unstarring", got)
	}
	if _, err := os.Stat(filepath.Join(lib, "a.jpg")); err != nil {
		t.Errorf("unstarring must not touch the file: %v", err)
	}
}

// Only real media inside the library can be starred, so the stored list can't
// be used to smuggle paths the rest of the app would then act on.
func TestFavoritesRejectsBadPaths(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	mkPhoto(t, lib, "ok.jpg")
	mkPhoto(t, lib, "notes.txt")
	if err := os.MkdirAll(filepath.Join(lib, "folder"), 0755); err != nil {
		t.Fatal(err)
	}
	c := sessionFor(t, "admin")

	cases := []struct {
		name, body string
		want       int
	}{
		{"traversal", `{"path":"../../etc/passwd","favorite":true}`, http.StatusNotFound},
		{"missing", `{"path":"nope.jpg","favorite":true}`, http.StatusNotFound},
		{"directory", `{"path":"folder","favorite":true}`, http.StatusNotFound},
		{"not media", `{"path":"notes.txt","favorite":true}`, http.StatusBadRequest},
		{"empty", `{"path":"","favorite":true}`, http.StatusBadRequest},
	}
	for _, c2 := range cases {
		if code := postFav(t, c, c2.body); code != c2.want {
			t.Errorf("%s: status = %d, want %d", c2.name, code, c2.want)
		}
	}
	if got := listFav(t, c); len(got) != 0 {
		t.Errorf("nothing should have been stored, got %v", got)
	}
}

// Favorites are keyed by path, so a move has to carry the star with it or the
// photo silently drops off the list while still sitting in the library.
func TestFavoritesFollowAMove(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	mkPhoto(t, lib, "old/a.jpg")
	c := sessionFor(t, "admin")
	postFav(t, c, `{"path":"old/a.jpg","favorite":true}`)

	// Move on disk, then tell the store — the order the handlers use.
	mkPhoto(t, lib, "new/a.jpg")
	os.Remove(filepath.Join(lib, "old", "a.jpg"))
	renameFavorite("old/a.jpg", "new/a.jpg")

	if got := listFav(t, c); len(got) != 1 || got[0] != "new/a.jpg" {
		t.Errorf("got %v, want [new/a.jpg] — the star should follow the file", got)
	}
}

// A favorite whose file is gone must disappear from the list rather than
// showing as a broken tile forever.
func TestFavoritesPruneDeletedFiles(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	mkPhoto(t, lib, "a.jpg")
	mkPhoto(t, lib, "b.jpg")
	c := sessionFor(t, "admin")
	postFav(t, c, `{"path":"a.jpg","favorite":true}`)
	postFav(t, c, `{"path":"b.jpg","favorite":true}`)

	os.Remove(filepath.Join(lib, "a.jpg"))

	got := listFav(t, c)
	if len(got) != 1 || got[0] != "b.jpg" {
		t.Errorf("got %v, want [b.jpg] — the deleted file should be pruned", got)
	}
	// And the pruning should have been written back, not just filtered on read.
	b, err := os.ReadFile(favPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "a.jpg") {
		t.Error("the deleted path is still in favorites.json")
	}
}

// Favorites belong to an account, so an unauthenticated caller gets nothing.
func TestFavoritesRequireASession(t *testing.T) {
	withUsers(t, nil)
	favEnv(t)
	if code := postFav(t, nil, `{"path":"a.jpg","favorite":true}`); code != http.StatusUnauthorized {
		t.Errorf("POST without a session = %d, want 401", code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/favorites", nil)
	rec := httptest.NewRecorder()
	favoritesListHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET without a session = %d, want 401", rec.Code)
	}
}
