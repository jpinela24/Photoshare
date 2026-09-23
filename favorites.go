package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Favorites are stored server-side, per user, in DATA_DIR/favorites.json.
//
// The sidebar's pinned folders live in localStorage, which means they are per
// browser and vanish with site data. Favorites are the thing you'd be annoyed
// to lose, so they belong on the server where every device sees the same list
// and a cleared cache costs nothing.
//
// Kept out of photoshare.config.json deliberately: that file is server
// configuration, rewritten wholesale under a lock on every settings save.
// Starring a photo shouldn't contend with that, and a corrupt favorites file
// shouldn't be able to take the server's configuration with it.

var (
	favMu     sync.Mutex
	favs      map[string]map[string]bool // username -> set of library-relative paths
	favLoaded bool
)

// writeFileAtomic writes via a temp file and a rename, so a crash mid-write
// can't leave a truncated list behind. (The config saver does the same thing
// inline; this is the reusable form.)
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".favorites.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func favPath() string { return filepath.Join(dataDir, "favorites.json") }

// loadFavsLocked reads the file once. A missing or unreadable file is not an
// error: it just means nobody has starred anything yet.
func loadFavsLocked() {
	if favLoaded {
		return
	}
	favLoaded = true
	favs = map[string]map[string]bool{}
	b, err := os.ReadFile(favPath())
	if err != nil {
		return
	}
	var raw map[string][]string
	if json.Unmarshal(b, &raw) != nil {
		return // corrupt; start empty rather than refusing to serve
	}
	for user, paths := range raw {
		set := make(map[string]bool, len(paths))
		for _, p := range paths {
			set[p] = true
		}
		favs[user] = set
	}
}

func saveFavsLocked() error {
	raw := map[string][]string{}
	for user, set := range favs {
		if len(set) == 0 {
			continue // don't persist users with nothing starred
		}
		paths := make([]string, 0, len(set))
		for p := range set {
			paths = append(paths, p)
		}
		sort.Strings(paths) // stable on disk, so diffs and backups stay readable
		raw[user] = paths
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(favPath(), b, 0600)
}

// userFavorites returns the user's starred paths, dropping any whose file has
// since disappeared. Pruning on read keeps the view honest without needing to
// hook every possible way a file can leave the library.
func userFavorites(user string) []string {
	favMu.Lock()
	defer favMu.Unlock()
	loadFavsLocked()
	set := favs[user]
	if len(set) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(set))
	pruned := false
	for rel := range set {
		full, err := safePath(baseDir, rel)
		if err != nil {
			delete(set, rel)
			pruned = true
			continue
		}
		if fi, err := os.Lstat(full); err != nil || !fi.Mode().IsRegular() {
			delete(set, rel)
			pruned = true
			continue
		}
		out = append(out, rel)
	}
	if pruned {
		saveFavsLocked()
	}
	sort.Strings(out)
	return out
}

func isFavorite(user, rel string) bool {
	favMu.Lock()
	defer favMu.Unlock()
	loadFavsLocked()
	return favs[user][rel]
}

// setFavorite stars or unstars one path for one user.
func setFavorite(user, rel string, on bool) error {
	favMu.Lock()
	defer favMu.Unlock()
	loadFavsLocked()
	set := favs[user]
	if set == nil {
		set = map[string]bool{}
		favs[user] = set
	}
	if on {
		set[rel] = true
	} else {
		delete(set, rel)
	}
	return saveFavsLocked()
}

// renameFavorite follows a file that moved, for every user that starred it.
//
// Favorites are keyed by path, so without this a moved photo silently loses its
// star — the file is still there, just no longer where the list says. Call it
// alongside invalidateCache at each move site.
func renameFavorite(oldRel, newRel string) {
	oldRel, newRel = filepath.ToSlash(oldRel), filepath.ToSlash(newRel)
	if oldRel == newRel {
		return
	}
	favMu.Lock()
	defer favMu.Unlock()
	loadFavsLocked()
	changed := false
	for _, set := range favs {
		if set[oldRel] {
			delete(set, oldRel)
			set[newRel] = true
			changed = true
		}
	}
	if changed {
		saveFavsLocked()
	}
}

// renameFavoritePrefix follows a whole folder that moved, re-pointing every
// starred file underneath it.
//
// A folder move relocates all of its contents at once, and favorites are keyed
// by full path, so without this every star inside a moved folder would be
// silently dropped the next time the list was pruned.
func renameFavoritePrefix(oldDir, newDir string) {
	oldDir = strings.TrimSuffix(filepath.ToSlash(oldDir), "/")
	newDir = strings.TrimSuffix(filepath.ToSlash(newDir), "/")
	if oldDir == newDir || oldDir == "" {
		return
	}
	prefix := oldDir + "/"
	favMu.Lock()
	defer favMu.Unlock()
	loadFavsLocked()
	changed := false
	for _, set := range favs {
		for p := range set {
			// Match the directory itself or anything under it, never a sibling
			// whose name merely starts with the same characters.
			if p != oldDir && !strings.HasPrefix(p, prefix) {
				continue
			}
			delete(set, p)
			set[newDir+strings.TrimPrefix(p, oldDir)] = true
			changed = true
		}
	}
	if changed {
		saveFavsLocked()
	}
}

// sessionUser identifies who a request belongs to. Favorites are per account,
// so an unauthenticated request has nothing to return.
func sessionUser(r *http.Request) (string, bool) {
	s, ok := sessionFromRequest(r)
	if !ok {
		return "", false
	}
	return s.Username, true
}

// GET /api/favorites — the current user's starred paths, newest-first by name.
func favoritesListHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := sessionUser(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	paths := userFavorites(user)
	type item struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		IsVideo bool   `json:"isVideo"`
	}
	items := make([]item, 0, len(paths))
	for _, p := range paths {
		name := filepath.Base(p)
		items = append(items, item{Path: p, Name: name, IsVideo: isVideo(name)})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": items})
}

// POST /api/favorites  {"path":"...","favorite":true}
func favoritesSetHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := sessionUser(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		Path     string `json:"path"`
		Favorite bool   `json:"favorite"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Validate against the library even though nothing is written to disk here:
	// it keeps junk and traversal attempts out of the stored list.
	rel := filepath.ToSlash(strings.TrimSpace(body.Path))
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	full, err := safePath(baseDir, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if fi, err := os.Lstat(full); err != nil || !fi.Mode().IsRegular() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if name := filepath.Base(full); !isImage(name) && !isVideo(name) {
		http.Error(w, "not a photo or video", http.StatusBadRequest)
		return
	}
	if err := setFavorite(user, rel, body.Favorite); err != nil {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"path": rel, "favorite": body.Favorite})
}
