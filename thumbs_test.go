package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withThumbDir points the derivative cache at a temp directory for one test.
func withThumbDir(t *testing.T) string {
	t.Helper()
	prev := thumbDir
	thumbDir = t.TempDir()
	t.Cleanup(func() { thumbDir = prev })
	return thumbDir
}

// touch writes content to a file and gives it a distinct mtime, so tests don't
// depend on the filesystem's timestamp granularity.
func touch(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// Keying the cache on the path alone meant that editing a file in place kept
// serving the derivative built from the old contents forever — the grid showed
// the previous picture and no amount of reloading fixed it.
func TestCacheStemChangesWhenFileChanges(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pic.jpg")

	touch(t, p, "original", time.Hour)
	before := cacheStem(p, variantThumb)

	// Same path, same length, different content and mtime.
	touch(t, p, "replaced", 0)
	if after := cacheStem(p, variantThumb); after == before {
		t.Error("cache stem unchanged after the file was replaced — the stale derivative would still be served")
	}

	// Same path, different size.
	touch(t, p, "a much longer replacement", 0)
	if after := cacheStem(p, variantThumb); after == before {
		t.Error("cache stem unchanged after the file's size changed")
	}

	// An untouched file must keep its cache, or every restart would rebuild
	// the whole library.
	stable := cacheStem(p, variantThumb)
	if again := cacheStem(p, variantThumb); again != stable {
		t.Error("cache stem is not stable for an unchanged file — thumbnails would regenerate constantly")
	}
}

// Each variant of the same source must land in its own cache entry.
func TestCacheStemVariantsAreDistinct(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "clip.mp4")
	touch(t, p, "video", 0)

	seen := map[string]string{}
	for _, v := range cacheVariants {
		stem := cacheStem(p, v)
		if prev, dup := seen[stem]; dup {
			t.Errorf("variants %q and %q share a cache stem", prev, v)
		}
		seen[stem] = v
	}
}

// Moving or trashing a file must drop its derivatives. They are keyed by the
// source, so once it has moved the old entries are unreachable and would
// otherwise accumulate forever.
func TestInvalidateCacheRemovesEveryVariant(t *testing.T) {
	withThumbDir(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "pic.jpg")
	touch(t, p, "photo", 0)

	var written []string
	for _, v := range cacheVariants {
		cp := cachePathFor(p, v)
		if err := os.WriteFile(cp, []byte("derivative"), 0644); err != nil {
			t.Fatal(err)
		}
		written = append(written, cp)
	}
	// A derivative of an unrelated file must survive.
	other := filepath.Join(dir, "other.jpg")
	touch(t, other, "other", 0)
	keep := cachePathFor(other, variantThumb)
	if err := os.WriteFile(keep, []byte("derivative"), 0644); err != nil {
		t.Fatal(err)
	}

	invalidateCache(p)

	for _, cp := range written {
		if _, err := os.Stat(cp); !os.IsNotExist(err) {
			t.Errorf("%s survived invalidation", filepath.Base(cp))
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("invalidation removed an unrelated file's cache entry: %v", err)
	}
}

// The sweeper reclaims entries nothing points at, and must leave live ones
// alone — including the trash's, which the library walk never sees.
func TestSweepCacheKeepsLiveEntriesAndRemovesOrphans(t *testing.T) {
	cache := withThumbDir(t)
	dir := t.TempDir()

	livePath := filepath.Join(dir, "live.jpg")
	touch(t, livePath, "live", 0)
	trashPath := filepath.Join(dir, "trashed.jpg")
	touch(t, trashPath, "trashed", 0)

	liveThumb := cachePathFor(livePath, variantThumb)
	liveVideo := cachePathFor(livePath, variantH264)
	trashThumb := cachePathFor(trashPath, variantTrash)
	orphan := filepath.Join(cache, "deadbeefdeadbeefdeadbeefdeadbeef.jpg")
	orphanMP4 := filepath.Join(cache, "cafecafecafecafecafecafecafecafe.mp4")
	// Something that isn't ours at all — the sweeper must not touch it.
	foreign := filepath.Join(cache, "notes.txt")

	for _, p := range []string{liveThumb, liveVideo, trashThumb, orphan, orphanMP4, foreign} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	live := map[string]struct{}{}
	for _, v := range cacheVariants {
		live[cacheStem(livePath, v)] = struct{}{}
		live[cacheStem(trashPath, v)] = struct{}{}
	}

	if n := sweepCache(live); n != 2 {
		t.Errorf("swept %d entries, want 2", n)
	}
	for _, p := range []string{liveThumb, liveVideo, trashThumb} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sweep removed a live entry %s: %v", filepath.Base(p), err)
		}
	}
	for _, p := range []string{orphan, orphanMP4} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("sweep left orphan %s behind", filepath.Base(p))
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("sweep deleted a non-cache file: %v", err)
	}
}

// A cache inside the library gets walked as if it were content, so the startup
// check has to recognise every way THUMB_DIR can land there — and must not
// misfire on a sibling directory, which is the recommended layout.
func TestDirWithin(t *testing.T) {
	lib := filepath.Join("/srv", "photos")
	cases := []struct {
		name  string
		child string
		want  bool
	}{
		{"the library itself", lib, true},
		{"directly inside", filepath.Join(lib, "cache"), true},
		{"nested inside", filepath.Join(lib, "a", "b", "cache"), true},
		{"sibling", filepath.Join("/srv", "photoshare-cache"), false},
		{"parent", "/srv", false},
		{"unrelated", filepath.Join("/var", "cache"), false},
		// A sibling whose name merely starts with the library's name must not
		// be mistaken for a child.
		{"name-prefix sibling", "/srv/photos-cache", false},
	}
	for _, c := range cases {
		if got := dirWithin(lib, c.child); got != c.want {
			t.Errorf("%s: dirWithin(%q, %q) = %v, want %v", c.name, lib, c.child, got, c.want)
		}
	}
}
