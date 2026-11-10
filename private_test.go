package main

// Tests for "Only Me" privacy. The important property isn't that tagged items
// are missing from listings — it's that the content endpoints refuse to serve
// them even when the exact path is known.

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

// privTestEnv builds a throwaway library with an admin ("owner"), a second
// admin ("other"), and a viewer, and resets the private store.
func privTestEnv(t *testing.T) string {
	t.Helper()
	lib := t.TempDir()
	data := t.TempDir()

	prevBase, prevTrash, prevUpload, prevData, prevThumb := baseDir, trashDir, uploadDir, dataDir, thumbDir
	prevScope := privateScope
	baseDir = lib
	trashDir = filepath.Join(lib, "_Trash")
	uploadDir = "_Uploads"
	dataDir = data
	thumbDir = t.TempDir()
	privateScope = "owner"
	os.MkdirAll(trashDir, 0755)

	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()

	privMu.Lock()
	privs = map[string]string{}
	privLoaded = true
	privMu.Unlock()

	h, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	withUsers(t, []User{
		{Username: "owner", PassHash: string(h), Role: "admin"},
		{Username: "other", PassHash: string(h), Role: "admin"},
		{Username: "viewer", PassHash: string(h), Role: "viewer"},
	})

	t.Cleanup(func() {
		baseDir, trashDir, uploadDir, dataDir, thumbDir = prevBase, prevTrash, prevUpload, prevData, prevThumb
		privateScope = prevScope
		privMu.Lock()
		privs = map[string]string{}
		privLoaded = false
		privMu.Unlock()
	})
	return lib
}

// asUser returns a request carrying a live session for the named user.
func asUser(t *testing.T, user, target string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if user != "" {
		tok := sessions.create(user, "admin")
		t.Cleanup(func() { sessions.revoke(tok) })
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	}
	return req
}

func seedPhoto(t *testing.T, lib, rel string) {
	t.Helper()
	full := filepath.Join(lib, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(full), 0755)
	// A real JPEG header, so the media checks accept it.
	data := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...)
	if err := os.WriteFile(full, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// ── The core guarantee ───────────────────────────────────────────────────────

// Knowing the exact path must not be enough. This is the test that matters: a
// privacy feature that only hides rows in a listing isn't one.
func TestPrivatePhotoIsNotServedToOthersEvenWithExactPath(t *testing.T) {
	lib := privTestEnv(t)
	seedPhoto(t, lib, "Album/secret.jpg")
	if err := setPrivate("owner", "Album/secret.jpg", true); err != nil {
		t.Fatal(err)
	}

	for _, endpoint := range []struct {
		name string
		h    http.HandlerFunc
		url  string
	}{
		{"photo", photoHandler, "/api/photo?path=Album/secret.jpg"},
		{"thumb", thumbHandler, "/api/thumb?path=Album/secret.jpg"},
		{"meta", metaHandler, "/api/meta?path=Album/secret.jpg"},
	} {
		for _, who := range []string{"viewer", "other", ""} {
			label := who
			if label == "" {
				label = "anonymous"
			}
			w := httptest.NewRecorder()
			endpoint.h(w, asUser(t, who, endpoint.url))
			if w.Code != http.StatusNotFound {
				t.Errorf("%s as %s = %d, want 404 (private item must not be served)",
					endpoint.name, label, w.Code)
			}
		}
		// The owner still gets it.
		w := httptest.NewRecorder()
		endpoint.h(w, asUser(t, "owner", endpoint.url))
		if w.Code == http.StatusNotFound {
			t.Errorf("%s as owner = 404, want the owner to still see their own item", endpoint.name)
		}
	}
}

// ── Listings ─────────────────────────────────────────────────────────────────

func TestPrivateItemHiddenFromBrowse(t *testing.T) {
	lib := privTestEnv(t)
	seedPhoto(t, lib, "Album/public.jpg")
	seedPhoto(t, lib, "Album/secret.jpg")
	setPrivate("owner", "Album/secret.jpg", true)

	names := func(user string) []string {
		w := httptest.NewRecorder()
		browseHandler(w, asUser(t, user, "/api/browse?path=Album"))
		var out []Entry
		json.NewDecoder(w.Body).Decode(&out)
		var n []string
		for _, e := range out {
			n = append(n, e.Name)
		}
		return n
	}

	got := names("viewer")
	if len(got) != 1 || got[0] != "public.jpg" {
		t.Errorf("viewer sees %v, want only [public.jpg]", got)
	}
	if o := names("owner"); len(o) != 2 {
		t.Errorf("owner sees %v, want both files", o)
	}
}

// A tagged folder hides everything inside it, at any depth.
func TestPrivateFolderHidesContents(t *testing.T) {
	lib := privTestEnv(t)
	seedPhoto(t, lib, "Vault/a.jpg")
	seedPhoto(t, lib, "Vault/Deep/b.jpg")
	seedPhoto(t, lib, "Open/c.jpg")
	setPrivate("owner", "Vault", true)

	v := privacyFor(asUser(t, "viewer", "/"))
	for _, rel := range []string{"Vault", "Vault/a.jpg", "Vault/Deep", "Vault/Deep/b.jpg"} {
		if v.canSee(rel) {
			t.Errorf("viewer can see %q inside a private folder", rel)
		}
	}
	if !v.canSee("Open/c.jpg") {
		t.Error("viewer lost access to an untagged file")
	}
	// A sibling whose name merely starts the same must not be caught.
	seedPhoto(t, lib, "VaultOpen/d.jpg")
	if !v.canSee("VaultOpen/d.jpg") {
		t.Error("prefix match leaked onto a sibling folder")
	}
}

// Folder counts must not announce items the viewer can't open.
func TestFolderCountsExcludePrivate(t *testing.T) {
	lib := privTestEnv(t)
	seedPhoto(t, lib, "Album/a.jpg")
	seedPhoto(t, lib, "Album/b.jpg")
	seedPhoto(t, lib, "Album/secret.jpg")
	setPrivate("owner", "Album/secret.jpg", true)

	count := func(user string) int {
		w := httptest.NewRecorder()
		browseHandler(w, asUser(t, user, "/api/browse?path="))
		var out []Entry
		json.NewDecoder(w.Body).Decode(&out)
		for _, e := range out {
			if e.Name == "Album" {
				return e.Photos
			}
		}
		return -1
	}
	if got := count("viewer"); got != 2 {
		t.Errorf("viewer sees photo count %d, want 2 (private one excluded)", got)
	}
	if got := count("owner"); got != 3 {
		t.Errorf("owner sees photo count %d, want 3", got)
	}
}

func TestPrivateItemHiddenFromSearch(t *testing.T) {
	lib := privTestEnv(t)
	seedPhoto(t, lib, "Album/sunset-public.jpg")
	seedPhoto(t, lib, "Album/sunset-secret.jpg")
	setPrivate("owner", "Album/sunset-secret.jpg", true)

	hits := func(user string) []string {
		w := httptest.NewRecorder()
		searchHandler(w, asUser(t, user, "/api/search?q=sunset"))
		var out []SearchEntry
		json.NewDecoder(w.Body).Decode(&out)
		var n []string
		for _, e := range out {
			n = append(n, e.Name)
		}
		return n
	}
	for _, n := range hits("viewer") {
		if strings.Contains(n, "secret") {
			t.Errorf("search leaked a private item to a viewer: %v", hits("viewer"))
		}
	}
	if len(hits("owner")) != 2 {
		t.Errorf("owner search = %v, want both", hits("owner"))
	}
}

// ── Scope setting ────────────────────────────────────────────────────────────

func TestPrivateScopeSetting(t *testing.T) {
	lib := privTestEnv(t)
	seedPhoto(t, lib, "a.jpg")
	setPrivate("owner", "a.jpg", true)

	// owner scope: a second admin is shut out too.
	privateScope = "owner"
	if privacyFor(asUser(t, "other", "/")).canSee("a.jpg") {
		t.Error("under owner scope, another admin could see the item")
	}
	// admin scope: any admin may see it, but a viewer still may not.
	privateScope = "admin"
	if !privacyFor(asUser(t, "other", "/")).canSee("a.jpg") {
		t.Error("under admin scope, another admin could not see the item")
	}
	if privacyFor(asUser(t, "viewer", "/")).canSee("a.jpg") {
		t.Error("a viewer could see a private item under admin scope")
	}
}

// Under owner scope one admin must not be able to untag (and so expose)
// another's item.
func TestOtherAdminCannotUntagUnderOwnerScope(t *testing.T) {
	lib := privTestEnv(t)
	seedPhoto(t, lib, "a.jpg")
	setPrivate("owner", "a.jpg", true)
	privateScope = "owner"

	body := `{"path":"a.jpg","private":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/private", strings.NewReader(body))
	tok := sessions.create("other", "admin")
	defer sessions.revoke(tok)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	privateSetHandler(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for another admin untagging an owned item", w.Code)
	}
	if privacyFor(asUser(t, "viewer", "/")).canSee("a.jpg") {
		t.Error("item became visible after a rejected untag")
	}
}

// Only admins can tag at all.
func TestViewerCannotTagPrivate(t *testing.T) {
	lib := privTestEnv(t)
	seedPhoto(t, lib, "a.jpg")

	req := httptest.NewRequest(http.MethodPost, "/api/private", strings.NewReader(`{"path":"a.jpg","private":true}`))
	tok := sessions.create("viewer", "viewer")
	defer sessions.revoke(tok)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	privateSetHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — viewers must not be able to tag", w.Code)
	}
}

// ── Tag follows the file ─────────────────────────────────────────────────────

func TestTagFollowsRenameAndFolderMove(t *testing.T) {
	privTestEnv(t)
	setPrivate("owner", "Album/a.jpg", true)
	renamePrivate("Album/a.jpg", "Album/renamed.jpg")

	v := privacyFor(asUser(t, "viewer", "/"))
	if v.canSee("Album/renamed.jpg") {
		t.Error("tag lost on rename — the file became public")
	}

	setPrivate("owner", "Vault/x.jpg", true)
	renamePrivatePrefix("Vault", "Archive/Vault")
	v2 := privacyFor(asUser(t, "viewer", "/"))
	if v2.canSee("Archive/Vault/x.jpg") {
		t.Error("tag lost on folder move — the file became public")
	}
}

func TestForgetPrivateDropsTagAndDescendants(t *testing.T) {
	privTestEnv(t)
	setPrivate("owner", "Vault", true)
	setPrivate("owner", "Vault/a.jpg", true)
	setPrivate("owner", "Other/b.jpg", true)

	forgetPrivate("Vault")

	privMu.Lock()
	_, hasDir := privs["Vault"]
	_, hasChild := privs["Vault/a.jpg"]
	_, hasOther := privs["Other/b.jpg"]
	privMu.Unlock()
	if hasDir || hasChild {
		t.Error("forgetPrivate left the folder tag or a descendant behind")
	}
	if !hasOther {
		t.Error("forgetPrivate removed an unrelated tag")
	}
}
