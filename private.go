package main

// "Only Me" — per-item privacy.
//
// An admin can tag a photo, video or whole folder as private. Tagged items are
// invisible to everyone else: they vanish from browsing, search, the map,
// memories, the timeline, people/faces, duplicates and stats, AND the content
// endpoints refuse to serve them. That second half is the important one —
// hiding a file from a listing while /api/photo still happily returns it to
// anyone who guesses the path would be a privacy feature in name only.
//
// Stored in DATA_DIR/private.json as path -> owner username, following the same
// pattern as favorites.go: out of the config file, written atomically, 0600.
//
// Who may see a tagged item depends on the "privateScope" setting:
//   "owner" (default) — only the account that tagged it, even other admins.
//   "admin"           — any admin account.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	privMu     sync.Mutex
	privs      map[string]string // library-relative path -> owner username
	privLoaded bool

	// privateScope comes from AppConfig at startup: "owner" or "admin".
	privateScope = "owner"
)

func privPath() string { return filepath.Join(dataDir, "private.json") }

func loadPrivsLocked() {
	if privLoaded {
		return
	}
	privLoaded = true
	privs = map[string]string{}
	b, err := os.ReadFile(privPath())
	if err != nil {
		return
	}
	var raw map[string]string
	if json.Unmarshal(b, &raw) != nil {
		// Corrupt file: start empty rather than refusing to serve. Note this
		// fails *open* (nothing hidden) — see privacyFor, which treats a load
		// failure as "no private items", so a broken file can never silently
		// expose something it shouldn't... it simply has nothing to hide.
		return
	}
	for p, owner := range raw {
		privs[filepath.ToSlash(p)] = owner
	}
}

func savePrivsLocked() error {
	b, err := json.MarshalIndent(privs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(privPath(), b, 0600)
}

// setPrivate tags or untags one path for one user. Tagging an already-tagged
// path re-points it at the new owner only when the caller is allowed to see it,
// which the handler checks before calling.
func setPrivate(user, rel string, on bool) error {
	rel = filepath.ToSlash(rel)
	privMu.Lock()
	defer privMu.Unlock()
	loadPrivsLocked()
	if on {
		privs[rel] = user
	} else {
		delete(privs, rel)
	}
	return savePrivsLocked()
}

// renamePrivate follows a tagged file that moved.
func renamePrivate(oldRel, newRel string) {
	oldRel, newRel = filepath.ToSlash(oldRel), filepath.ToSlash(newRel)
	if oldRel == newRel {
		return
	}
	privMu.Lock()
	defer privMu.Unlock()
	loadPrivsLocked()
	if owner, ok := privs[oldRel]; ok {
		delete(privs, oldRel)
		privs[newRel] = owner
		savePrivsLocked()
	}
}

// renamePrivatePrefix follows a whole folder that moved, re-pointing the folder
// tag itself and every tagged file underneath it.
//
// Without this a moved folder would drop its tag and its contents would become
// visible to everyone — a privacy leak, not just a lost preference, which is
// why this is wired at every move site alongside renameFavoritePrefix.
func renamePrivatePrefix(oldDir, newDir string) {
	oldDir = strings.TrimSuffix(filepath.ToSlash(oldDir), "/")
	newDir = strings.TrimSuffix(filepath.ToSlash(newDir), "/")
	if oldDir == newDir || oldDir == "" {
		return
	}
	prefix := oldDir + "/"
	privMu.Lock()
	defer privMu.Unlock()
	loadPrivsLocked()
	changed := false
	for p, owner := range privs {
		// The directory itself or anything under it — never a sibling whose
		// name merely starts with the same characters.
		if p != oldDir && !strings.HasPrefix(p, prefix) {
			continue
		}
		delete(privs, p)
		privs[newDir+strings.TrimPrefix(p, oldDir)] = owner
		changed = true
	}
	if changed {
		savePrivsLocked()
	}
}

// forgetPrivate drops the tag for a path and everything under it. Called when
// an item is permanently deleted, so the list doesn't accumulate dead entries
// that could later shadow a brand-new file reusing the same name.
func forgetPrivate(rel string) {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), "/")
	if rel == "" {
		return
	}
	prefix := rel + "/"
	privMu.Lock()
	defer privMu.Unlock()
	loadPrivsLocked()
	changed := false
	for p := range privs {
		if p == rel || strings.HasPrefix(p, prefix) {
			delete(privs, p)
			changed = true
		}
	}
	if changed {
		savePrivsLocked()
	}
}

// ── Visibility ───────────────────────────────────────────────────────────────

// privacyView is a per-request snapshot of the private list plus who's asking.
// Taken once so a listing of thousands of files doesn't lock the map per entry.
type privacyView struct {
	user   string
	admin  bool
	owners map[string]string
	none   bool // fast path: nothing is tagged, so everything is visible
}

// privacyFor snapshots the private list for this request.
func privacyFor(r *http.Request) *privacyView {
	privMu.Lock()
	loadPrivsLocked()
	n := len(privs)
	var owners map[string]string
	if n > 0 {
		owners = make(map[string]string, n)
		for p, o := range privs {
			owners[p] = o
		}
	}
	privMu.Unlock()

	v := &privacyView{owners: owners, none: n == 0}
	if sess, ok := resolveSession(r); ok {
		v.user, v.admin = sess.Username, sess.Role == "admin"
	}
	return v
}

// ownerOf returns the tag owner for rel, checking the path itself and then each
// ancestor folder — tagging a folder private covers everything inside it.
func (v *privacyView) ownerOf(rel string) (string, bool) {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	if owner, ok := v.owners[rel]; ok {
		return owner, true
	}
	for {
		i := strings.LastIndex(rel, "/")
		if i <= 0 {
			return "", false
		}
		rel = rel[:i]
		if owner, ok := v.owners[rel]; ok {
			return owner, true
		}
	}
}

// canSee reports whether this request may see the given library-relative path.
func (v *privacyView) canSee(rel string) bool {
	if v.none {
		return true
	}
	owner, tagged := v.ownerOf(rel)
	if !tagged {
		return true
	}
	if v.user == "" {
		return false // not signed in
	}
	if strings.EqualFold(v.user, owner) {
		return true
	}
	// Another account: only an admin, and only when the setting allows it.
	return v.admin && privateScope == "admin"
}

// canList reports whether rel belongs in ordinary listings — browsing, search,
// the timeline, the map, people, favourites, duplicates, stats.
//
// This is deliberately stricter than canSee: a tagged item is left out for
// *everyone*, its owner included. That's the "vault" half of the feature — you
// shouldn't have to see your own private photos go past while someone is
// looking over your shoulder. They live in the Private view instead.
//
// canSee still governs whether the bytes may be served, so the owner can open
// what the Private view lists; the two must not be conflated.
func (v *privacyView) canList(rel string) bool {
	if v.none {
		return true
	}
	_, tagged := v.ownerOf(rel)
	return !tagged
}

// hasHidden reports whether anything is tagged at all — lets callers skip
// filtering work entirely on the common case of an untagged library.
func (v *privacyView) hasHidden() bool { return !v.none }

// taggedBy reports the owner shown to this requester, for the lock badge. It
// only ever reports for items the requester can already see.
func (v *privacyView) taggedBy(rel string) (string, bool) {
	if v.none {
		return "", false
	}
	owner, ok := v.ownerOf(rel)
	if !ok || !v.canSee(rel) {
		return "", false
	}
	return owner, true
}

// canTogglePrivate reports whether this session may change a path's tag: admins
// only, and (under "owner" scope) never someone else's tag.
func (v *privacyView) canTogglePrivate(rel string) bool {
	if !v.admin {
		return false
	}
	owner, tagged := v.ownerOf(rel)
	if !tagged {
		return true
	}
	return strings.EqualFold(v.user, owner) || privateScope == "admin"
}

// denyIfPrivate reports true (and has already written the response) when this
// request may not see rel. Guards every endpoint that serves or describes a
// single file, so hiding an item from the listings is actually enforced.
//
// It answers 404, not 403: a 403 would confirm something exists at that path,
// which is precisely what a private item must not reveal.
func denyIfPrivate(w http.ResponseWriter, r *http.Request, rel string) bool {
	if privacyFor(r).canSee(rel) {
		return false
	}
	http.Error(w, "not found", http.StatusNotFound)
	return true
}

// visibleDated filters the shared date index — which backs On This Day, the
// timeline and the map — down to what this requester may see. Returns idx
// untouched when nothing is tagged, so the common case copies nothing.
func visibleDated(r *http.Request, idx []datedFile) []datedFile {
	v := privacyFor(r)
	if !v.hasHidden() {
		return idx
	}
	out := make([]datedFile, 0, len(idx))
	for _, d := range idx {
		if v.canList(d.Path) {
			out = append(out, d)
		}
	}
	return out
}

// visibleGroups strips files this requester may not see out of duplicate
// groups, dropping any group left with fewer than two — a "duplicate pair"
// where one half is hidden isn't a pair, and showing it would reveal that
// something else matching it exists.
func visibleGroups(r *http.Request, groups []DuplicateGroup) []DuplicateGroup {
	v := privacyFor(r)
	if !v.hasHidden() {
		return groups
	}
	out := make([]DuplicateGroup, 0, len(groups))
	for _, g := range groups {
		files := make([]DupFile, 0, len(g.Files))
		for _, f := range g.Files {
			if v.canList(f.Path) {
				files = append(files, f)
			}
		}
		if len(files) < 2 {
			continue
		}
		g.Files = files
		out = append(out, g)
	}
	return out
}

// dirSize totals the regular files under dir, so a tagged folder can report
// how much it's holding. Errors are skipped rather than failing the listing.
func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// GET /api/private — the caller's tagged items (admins only).
func privateListHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	v := privacyFor(r)

	type item struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		IsDir   bool   `json:"isDir"`
		IsVideo bool   `json:"isVideo"`
		Owner   string `json:"owner"`
		Size    int64  `json:"size,omitempty"`
		Mod     int64  `json:"mod,omitempty"`
	}
	items := []item{}
	var stale []string
	var totalSize int64
	for rel, owner := range v.owners {
		if !v.canSee(rel) {
			continue // another admin's tag under "owner" scope
		}
		full, err := safePath(baseDir, rel)
		if err != nil {
			stale = append(stale, rel)
			continue
		}
		fi, err := os.Lstat(full)
		if err != nil {
			stale = append(stale, rel)
			continue
		}
		name := filepath.Base(rel)
		it := item{
			Path: rel, Name: name, IsDir: fi.IsDir(),
			IsVideo: !fi.IsDir() && isVideo(name), Owner: owner,
			Mod: fi.ModTime().Unix(),
		}
		if fi.IsDir() {
			it.Size = dirSize(full) // a tagged folder hides everything inside it
		} else {
			it.Size = fi.Size()
		}
		totalSize += it.Size
		items = append(items, it)
	}
	// Drop tags whose file is gone, so the list stays honest.
	for _, rel := range stale {
		forgetPrivate(rel)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"items": items, "scope": privateScope, "totalSize": totalSize,
	})
}

// POST /api/private  {"path":"...","private":true}
func privateSetHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Private bool   `json:"private"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rel := filepath.ToSlash(strings.TrimSpace(body.Path))
	if rel == "" || rel == "." {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	full, err := safePath(baseDir, rel)
	if err != nil || full == baseDir {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if _, err := os.Lstat(full); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	v := privacyFor(r)
	// Don't let one admin quietly untag (and thereby expose) another's item
	// when the setting says tags are owner-private.
	if !v.canTogglePrivate(rel) {
		http.Error(w, "this item is private to another account", http.StatusForbidden)
		return
	}
	if err := setPrivate(v.user, rel, body.Private); err != nil {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"path": rel, "private": body.Private})
}
