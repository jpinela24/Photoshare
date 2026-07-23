package main

// Tests for the duplicate finder: the exact three-stage funnel, the perceptual
// near-duplicate clustering, the keep-best recommendation, and the hash cache.

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// dupeTestEnv points the scanner at a throwaway library and resets shared state.
func dupeTestEnv(t *testing.T) string {
	t.Helper()
	lib := t.TempDir()
	data := t.TempDir()
	thumbs := t.TempDir()

	prevBase, prevTrash, prevUpload, prevData, prevThumb := baseDir, trashDir, uploadDir, dataDir, thumbDir
	baseDir = lib
	trashDir = filepath.Join(lib, "_Trash")
	uploadDir = "_Uploads"
	dataDir = data
	thumbDir = thumbs
	os.MkdirAll(trashDir, 0755)

	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()

	dupCacheMu.Lock()
	dupCache = map[string]dupEntry{}
	dupCacheOK = false
	dupCacheMu.Unlock()

	dupes.mu.Lock()
	dupes.groups, dupes.similar, dupes.cancel, dupes.running = nil, nil, false, false
	dupes.mu.Unlock()

	t.Cleanup(func() {
		baseDir, trashDir, uploadDir, dataDir, thumbDir = prevBase, prevTrash, prevUpload, prevData, prevThumb
		dupCacheMu.Lock()
		dupCache = map[string]dupEntry{}
		dupCacheOK = false
		dupCacheMu.Unlock()
	})
	return lib
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// gradientJPEG writes a deterministic image at the requested size, so the same
// "photo" can be saved at two resolutions/qualities.
func gradientJPEG(t *testing.T, path string, w, h, quality int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Smooth diagonal ramp + a block, scaled to the image size so the
			// picture *content* is identical regardless of resolution.
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			v := uint8((fx + fy) / 2 * 255)
			c := color.RGBA{v, uint8(fx * 255), uint8(fy * 255), 255}
			if fx > 0.6 && fy > 0.6 {
				c = color.RGBA{255, 255, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
}

// ── Exact duplicates ─────────────────────────────────────────────────────────

func TestComputeDuplicatesFindsExactCopies(t *testing.T) {
	lib := dupeTestEnv(t)
	content := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 5000)...)
	for i := range content[4:] {
		content[4+i] = byte(i % 251)
	}
	writeFile(t, filepath.Join(lib, "a", "photo.jpg"), content)
	writeFile(t, filepath.Join(lib, "b", "photo copy.jpg"), content)

	// Same size, different content — must NOT be grouped.
	other := make([]byte, len(content))
	copy(other, content)
	other[len(other)-1] ^= 0xFF
	writeFile(t, filepath.Join(lib, "c", "different.jpg"), other)

	// A unique file of its own size.
	writeFile(t, filepath.Join(lib, "d", "lonely.jpg"), []byte("not-a-duplicate-at-all"))

	exact, _, waste := computeDuplicates()

	if len(exact) != 1 {
		t.Fatalf("got %d exact groups, want 1: %+v", len(exact), exact)
	}
	if len(exact[0].Files) != 2 {
		t.Errorf("group has %d files, want 2", len(exact[0].Files))
	}
	if exact[0].Kind != "exact" {
		t.Errorf("kind = %q, want exact", exact[0].Kind)
	}
	if waste != int64(len(content)) {
		t.Errorf("waste = %d, want %d (one redundant copy)", waste, len(content))
	}
}

// Files that share a long identical header but differ at the tail must still be
// separated — this is what the tail half of the sample hash exists for.
func TestSampleHashDistinguishesSharedHeaders(t *testing.T) {
	dir := t.TempDir()
	head := make([]byte, 200*1024) // > 2 * 64 KiB so the tail sample kicks in
	for i := range head {
		head[i] = byte(i % 97)
	}
	a := append(append([]byte{}, head...), []byte("ENDING-A")...)
	b := append(append([]byte{}, head...), []byte("ENDING-B")...)
	pa, pb := filepath.Join(dir, "a.mp4"), filepath.Join(dir, "b.mp4")
	writeFile(t, pa, a)
	writeFile(t, pb, b)

	ha := sampleHash(pa, int64(len(a)))
	hb := sampleHash(pb, int64(len(b)))
	if ha == "" || hb == "" {
		t.Fatal("sampleHash returned empty")
	}
	if ha == hb {
		t.Error("files differing only in their tail produced the same sample hash — tail sampling is not working")
	}
}

func TestDuplicateScanSkipsTrashAndUploads(t *testing.T) {
	lib := dupeTestEnv(t)
	content := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	writeFile(t, filepath.Join(lib, "keep.jpg"), content)
	writeFile(t, filepath.Join(lib, "_Trash", "old.jpg"), content)
	writeFile(t, filepath.Join(lib, "_Uploads", "inbox.jpg"), content)

	exact, _, _ := computeDuplicates()
	if len(exact) != 0 {
		t.Errorf("got %d groups, want 0 — _Trash/_Uploads must be excluded: %+v", len(exact), exact)
	}
}

// ── Hash cache ───────────────────────────────────────────────────────────────

func TestHashCacheIsPopulatedAndReused(t *testing.T) {
	lib := dupeTestEnv(t)
	content := make([]byte, 4096)
	for i := range content {
		content[i] = byte(i % 13)
	}
	writeFile(t, filepath.Join(lib, "one.jpg"), content)
	writeFile(t, filepath.Join(lib, "two.jpg"), content)

	computeDuplicates()

	dupCacheMu.RLock()
	n := len(dupCache)
	var haveFull bool
	for _, e := range dupCache {
		if e.Full != "" {
			haveFull = true
		}
	}
	dupCacheMu.RUnlock()
	if n == 0 || !haveFull {
		t.Fatalf("cache not populated (%d entries, full-hash present=%v)", n, haveFull)
	}

	// A second scan must reuse the cache and still produce the same answer.
	exact, _, _ := computeDuplicates()
	if len(exact) != 1 || len(exact[0].Files) != 2 {
		t.Errorf("cached re-scan changed the result: %+v", exact)
	}

	// A changed file must invalidate its row (size/mtime no longer match).
	if _, ok := cachedEntry("one.jpg", 999, 999); ok {
		t.Error("cachedEntry returned a hit for a mismatched size/mtime")
	}
}

// ── Near-duplicate (perceptual) detection ────────────────────────────────────

func TestFindsSimilarPhotosAtDifferentSizes(t *testing.T) {
	lib := dupeTestEnv(t)
	// The same picture saved large/high-quality and small/low-quality — the
	// classic "original vs WhatsApp copy" duplicate that MD5 cannot see.
	gradientJPEG(t, filepath.Join(lib, "orig", "sunset.jpg"), 640, 480, 95)
	gradientJPEG(t, filepath.Join(lib, "shared", "sunset-small.jpg"), 160, 120, 40)

	exact, similar, _ := computeDuplicates()

	if len(exact) != 0 {
		t.Errorf("different-sized files should not be exact duplicates, got %d groups", len(exact))
	}
	if len(similar) != 1 {
		t.Fatalf("got %d similar groups, want 1", len(similar))
	}
	g := similar[0]
	if len(g.Files) != 2 {
		t.Fatalf("similar group has %d files, want 2", len(g.Files))
	}
	if g.Kind != "similar" {
		t.Errorf("kind = %q, want similar", g.Kind)
	}
	if g.Similarity < 80 {
		t.Errorf("similarity = %d%%, want >= 80%% for the same picture", g.Similarity)
	}
	// The big original must be the recommended keeper.
	for _, f := range g.Files {
		if f.Best && f.Width != 640 {
			t.Errorf("best copy is %dx%d, want the 640x480 original", f.Width, f.Height)
		}
	}
}

func TestDifferentPhotosAreNotSimilar(t *testing.T) {
	lib := dupeTestEnv(t)
	gradientJPEG(t, filepath.Join(lib, "a.jpg"), 320, 240, 90)

	// A visually unrelated image: inverted checkerboard.
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			if (x/16+y/16)%2 == 0 {
				img.Set(x, y, color.RGBA{0, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{255, 255, 255, 255})
			}
		}
	}
	f, _ := os.Create(filepath.Join(lib, "b.jpg"))
	jpeg.Encode(f, img, &jpeg.Options{Quality: 90})
	f.Close()

	_, similar, _ := computeDuplicates()
	if len(similar) != 0 {
		t.Errorf("unrelated images were clustered as similar: %+v", similar)
	}
}

// ── Keep-best recommendation ─────────────────────────────────────────────────

func TestMarkBestPrefersOriginalName(t *testing.T) {
	files := []DupFile{
		{Path: "album/IMG_1234 (1).jpg", Name: "IMG_1234 (1).jpg", Size: 100},
		{Path: "album/IMG_1234.jpg", Name: "IMG_1234.jpg", Size: 100},
		{Path: "album/IMG_1234 copy.jpg", Name: "IMG_1234 copy.jpg", Size: 100},
	}
	markBest(files, false)
	for _, f := range files {
		if f.Best && f.Name != "IMG_1234.jpg" {
			t.Errorf("best = %q, want the un-suffixed original IMG_1234.jpg", f.Name)
		}
	}
}

func TestMarkBestPrefersHigherResolution(t *testing.T) {
	files := []DupFile{
		{Path: "a/small.jpg", Name: "small.jpg", Size: 50_000, Width: 640, Height: 480},
		{Path: "a/big.jpg", Name: "big.jpg", Size: 900_000, Width: 4032, Height: 3024},
	}
	markBest(files, true)
	for _, f := range files {
		if f.Best && f.Name != "big.jpg" {
			t.Errorf("best = %q, want the higher-resolution big.jpg", f.Name)
		}
	}
}

func TestMarkBestAlwaysPicksExactlyOne(t *testing.T) {
	files := []DupFile{
		{Path: "x.jpg", Name: "x.jpg", Size: 10},
		{Path: "y.jpg", Name: "y.jpg", Size: 10},
		{Path: "z.jpg", Name: "z.jpg", Size: 10},
	}
	markBest(files, false)
	n := 0
	for _, f := range files {
		if f.Best {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d files marked best, want exactly 1", n)
	}
}

// ── Hardlinks ────────────────────────────────────────────────────────────────

// Two names for one inode look identical but deleting one frees nothing, so
// they must not be reported as a duplicate.
func TestHardlinksAreNotCountedAsDuplicates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hardlink semantics differ on Windows")
	}
	lib := dupeTestEnv(t)
	orig := filepath.Join(lib, "photo.jpg")
	writeFile(t, orig, []byte("some image bytes that are long enough to matter"))
	if err := os.Link(orig, filepath.Join(lib, "same-photo.jpg")); err != nil {
		t.Skipf("hardlinks unsupported here: %v", err)
	}

	exact, _, waste := computeDuplicates()
	if len(exact) != 0 {
		t.Errorf("hardlinked file reported as a duplicate: %+v", exact)
	}
	if waste != 0 {
		t.Errorf("waste = %d, want 0 — deleting a hardlink frees nothing", waste)
	}
}
