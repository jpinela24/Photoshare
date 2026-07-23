package main

// Duplicate finder.
//
// Two kinds of duplicates are reported:
//
//   exact   — byte-identical files, found with a three-stage funnel:
//             group by size (stat only) → head+tail sample hash → full MD5.
//   similar — visually near-identical photos (a resized/re-compressed copy,
//             a WhatsApp export, HEIC vs JPEG of the same shot). Found with a
//             64-bit perceptual hash (dHash) clustered by Hamming distance.
//
// Everything that touches the disk runs on a worker pool, and every hash is
// memoized in a small on-disk cache keyed by path+size+mtime, so a re-scan only
// pays for files that are new or changed.

import (
	"crypto/md5"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"math/bits"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disintegration/imaging"
)

type DupFile struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Mod    string `json:"mod"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// Best marks the copy we recommend keeping (highest resolution / most
	// original-looking name / oldest). The UI pre-selects the rest for cleanup.
	Best bool `json:"best,omitempty"`
	// Copies is how many byte-identical copies this entry stands for inside a
	// "similar" group (it is the representative of its own exact group).
	Copies int `json:"copies,omitempty"`
}

type DuplicateGroup struct {
	Hash string    `json:"hash"`
	Size int64     `json:"size"`
	Files []DupFile `json:"files"`
	// Kind is "exact" or "similar"; Similarity is the % match for similar groups.
	Kind       string `json:"kind"`
	Similarity int    `json:"similarity,omitempty"`
}

// ── Scan state (async, polled) ───────────────────────────────────────────────

type dupeState struct {
	mu         sync.Mutex
	running    bool
	cancel     bool
	phase      string // indexing | sampling | hashing | comparing | done
	processed  int64
	total      int64
	groups     []DuplicateGroup
	similar    []DuplicateGroup
	totalWaste int64
	finishedAt time.Time
	err        string
}

var dupes = &dupeState{}

func (d *dupeState) canceled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel
}

func (d *dupeState) setPhase(p string, total int64) {
	d.mu.Lock()
	d.phase = p
	d.total = total
	atomic.StoreInt64(&d.processed, 0)
	d.mu.Unlock()
}

// ── Hash cache ───────────────────────────────────────────────────────────────

type dupEntry struct {
	Size     int64
	ModNs    int64
	Sample   string
	Full     string
	PHash    uint64
	HasPHash bool
	Width    int
	Height   int
}

var (
	dupCacheMu sync.RWMutex
	dupCache   = map[string]dupEntry{} // key: path relative to baseDir
	dupCacheOK bool
)

func dupCachePath() string { return filepath.Join(dataDir, "dupe-cache.gob") }

func loadDupCache() {
	dupCacheMu.Lock()
	defer dupCacheMu.Unlock()
	if dupCacheOK {
		return
	}
	dupCacheOK = true
	f, err := os.Open(dupCachePath())
	if err != nil {
		return
	}
	defer f.Close()
	m := map[string]dupEntry{}
	if err := gob.NewDecoder(f).Decode(&m); err != nil {
		log.Printf("[DUPES] cache unreadable (%v) — starting fresh", err)
		return
	}
	dupCache = m
	log.Printf("[DUPES] loaded %d cached file hashes", len(m))
}

func saveDupCache() {
	dupCacheMu.RLock()
	snapshot := make(map[string]dupEntry, len(dupCache))
	for k, v := range dupCache {
		snapshot[k] = v
	}
	dupCacheMu.RUnlock()

	tmp := dupCachePath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	if err := gob.NewEncoder(f).Encode(snapshot); err != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	f.Close()
	os.Rename(tmp, dupCachePath())
}

// cachedEntry returns the cache row for rel if it still matches size+mtime.
func cachedEntry(rel string, size, modNs int64) (dupEntry, bool) {
	dupCacheMu.RLock()
	e, ok := dupCache[rel]
	dupCacheMu.RUnlock()
	if !ok || e.Size != size || e.ModNs != modNs {
		return dupEntry{}, false
	}
	return e, true
}

func updateEntry(rel string, size, modNs int64, mut func(*dupEntry)) {
	dupCacheMu.Lock()
	e := dupCache[rel]
	if e.Size != size || e.ModNs != modNs {
		e = dupEntry{Size: size, ModNs: modNs}
	}
	mut(&e)
	dupCache[rel] = e
	dupCacheMu.Unlock()
}

// ── Hashing ──────────────────────────────────────────────────────────────────

// sampleHash fingerprints a file from its first and last 64 KiB. Sampling the
// tail as well as the head matters for video: clips from the same camera share
// long identical headers, so a head-only sample collides constantly and forces
// a needless full read of multi-GB files.
func sampleHash(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	const chunk = 64 * 1024
	h := md5.New()
	buf := make([]byte, chunk)

	n, _ := io.ReadFull(f, buf)
	h.Write(buf[:n])
	if size > 2*chunk {
		if _, err := f.Seek(-chunk, io.SeekEnd); err == nil {
			n, _ := io.ReadFull(f, buf)
			h.Write(buf[:n])
		}
	}
	fmt.Fprintf(h, "|%d", size)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// fullHash is the identity hash used to decide that two files are the same and
// that one can be deleted. It stays a full 128-bit MD5 over the whole file
// deliberately: the speed here comes from parallelism and caching, not from
// weakening the hash that a delete decision rests on.
func fullHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// dHash computes a 64-bit perceptual hash: shrink to 9x8 greyscale, then record
// whether each pixel is brighter than its right-hand neighbour. Robust to
// rescaling and re-compression, which is exactly what a "same photo, smaller
// copy" duplicate looks like.
func dHash(img image.Image) uint64 {
	small := imaging.Resize(imaging.Grayscale(img), 9, 8, imaging.Linear)
	var h uint64
	bit := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			l, _, _, _ := small.At(x, y).RGBA()
			r, _, _, _ := small.At(x+1, y).RGBA()
			if l > r {
				h |= 1 << uint(bit)
			}
			bit++
		}
	}
	return h
}

// perceptualHash prefers the already-generated thumbnail (small, fast to
// decode) and only falls back to the original when no thumb is cached.
func perceptualHash(full string) (uint64, bool) {
	thumb := filepath.Join(thumbDir, fmt.Sprintf("%x", md5.Sum([]byte(full)))+".jpg")
	for _, p := range []string{thumb, full} {
		if p == full && !isImage(filepath.Base(full)) {
			continue
		}
		img, err := imaging.Open(p, imaging.AutoOrientation(true))
		if err == nil {
			return dHash(img), true
		}
	}
	return 0, false
}

func imageDims(full string) (int, int) {
	f, err := os.Open(full)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f) // header only — no full decode
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// parallelFor runs fn over items on a pool sized to the machine, bumping the
// progress counter and bailing out early if the scan was canceled.
func parallelFor[T any](items []T, fn func(T)) {
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8 // disk-bound past this; more threads just thrash
	}
	if workers < 1 {
		workers = 1
	}
	ch := make(chan T)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				if dupes.canceled() {
					return
				}
				fn(it)
				atomic.AddInt64(&dupes.processed, 1)
			}
		}()
	}
	for _, it := range items {
		if dupes.canceled() {
			break
		}
		ch <- it
	}
	close(ch)
	wg.Wait()
}

// ── Scan ─────────────────────────────────────────────────────────────────────

type dupCandidate struct {
	path  string // absolute
	rel   string // relative to baseDir, slash-separated
	name  string
	size  int64
	modNs int64
	mod   string
	info  os.FileInfo
}

func runDuplicateScan() {
	exact, similar, waste := computeDuplicates()
	saveDupCache()
	dupes.mu.Lock()
	dupes.running = false
	dupes.phase = "done"
	if dupes.cancel {
		dupes.err = "scan canceled"
	} else {
		dupes.groups, dupes.similar, dupes.totalWaste = exact, similar, waste
	}
	dupes.cancel = false
	dupes.finishedAt = time.Now()
	dupes.mu.Unlock()
}

func computeDuplicates() ([]DuplicateGroup, []DuplicateGroup, int64) {
	loadDupCache()
	trashLower := strings.ToLower(filepath.Base(trashDir))
	uploadLower := strings.ToLower(uploadDir)

	// Pass 1 (index): walk once, stat only. WalkDir avoids the extra stat per
	// entry that Walk does.
	var all []dupCandidate
	filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			lower := strings.ToLower(name)
			if lower == trashLower || lower == uploadLower {
				return filepath.SkipDir
			}
			return nil
		}
		if !isImage(name) && !isVideo(name) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 {
			return nil
		}
		rel, _ := filepath.Rel(baseDir, path)
		all = append(all, dupCandidate{
			path: path, rel: filepath.ToSlash(rel), name: name,
			size: info.Size(), modNs: info.ModTime().UnixNano(),
			mod: info.ModTime().Format("Jan 2, 2006"), info: info,
		})
		return nil
	})
	if dupes.canceled() {
		return nil, nil, 0
	}

	// Group by size — files of different sizes can't be byte-identical.
	bySize := map[int64][]dupCandidate{}
	for _, c := range all {
		bySize[c.size] = append(bySize[c.size], c)
	}
	var sampleWork []dupCandidate
	for _, files := range bySize {
		if len(files) > 1 {
			sampleWork = append(sampleWork, files...)
		}
	}

	// Pass 2 (sample): head+tail fingerprint, in parallel, cache-backed.
	dupes.setPhase("sampling", int64(len(sampleWork)))
	var sampleMu sync.Mutex
	bySample := map[string][]dupCandidate{}
	parallelFor(sampleWork, func(c dupCandidate) {
		sh := ""
		if e, ok := cachedEntry(c.rel, c.size, c.modNs); ok && e.Sample != "" {
			sh = e.Sample
		} else {
			sh = sampleHash(c.path, c.size)
			if sh != "" {
				updateEntry(c.rel, c.size, c.modNs, func(e *dupEntry) { e.Sample = sh })
			}
		}
		if sh == "" {
			return
		}
		key := fmt.Sprintf("%d:%s", c.size, sh)
		sampleMu.Lock()
		bySample[key] = append(bySample[key], c)
		sampleMu.Unlock()
	})
	if dupes.canceled() {
		return nil, nil, 0
	}

	// Pass 3 (confirm): full MD5 on the survivors only.
	var fullWork []dupCandidate
	for _, files := range bySample {
		if len(files) > 1 {
			fullWork = append(fullWork, files...)
		}
	}
	dupes.setPhase("hashing", int64(len(fullWork)))
	var fullMu sync.Mutex
	byFull := map[string][]dupCandidate{}
	parallelFor(fullWork, func(c dupCandidate) {
		fh := ""
		if e, ok := cachedEntry(c.rel, c.size, c.modNs); ok && e.Full != "" {
			fh = e.Full
		} else {
			fh = fullHash(c.path)
			if fh != "" {
				updateEntry(c.rel, c.size, c.modNs, func(e *dupEntry) { e.Full = fh })
			}
		}
		if fh == "" {
			return
		}
		fullMu.Lock()
		byFull[fh] = append(byFull[fh], c)
		fullMu.Unlock()
	})
	if dupes.canceled() {
		return nil, nil, 0
	}

	// Build exact groups. Hardlinks (two names, one inode) are collapsed: they
	// look identical but deleting one reclaims nothing.
	var exact []DuplicateGroup
	var totalWaste int64
	inExact := map[string]bool{}     // rel → part of an exact group
	repOf := map[string]dupCandidate{} // exact-group hash → representative file
	for hash, files := range byFull {
		uniq := files[:0:0]
		for _, c := range files {
			dup := false
			for _, u := range uniq {
				if os.SameFile(c.info, u.info) {
					dup = true
					break
				}
			}
			if !dup {
				uniq = append(uniq, c)
			}
		}
		if len(uniq) < 2 {
			continue
		}
		sort.Slice(uniq, func(i, j int) bool { return uniq[i].rel < uniq[j].rel })
		dfs := make([]DupFile, 0, len(uniq))
		for _, c := range uniq {
			dfs = append(dfs, DupFile{Path: c.rel, Name: c.name, Size: c.size, Mod: c.mod})
			inExact[c.rel] = true
		}
		markBest(dfs, false)
		exact = append(exact, DuplicateGroup{Hash: hash, Size: uniq[0].size, Files: dfs, Kind: "exact"})
		totalWaste += uniq[0].size * int64(len(uniq)-1)
		// The representative that stands in for this group during similar-photo
		// clustering must be the copy we'd keep — otherwise "trash the rest" on
		// a similar group could bin the good original and keep a "(1)" copy.
		repOf[hash] = uniq[0]
		for i, df := range dfs {
			if df.Best {
				repOf[hash] = uniq[i]
				break
			}
		}
	}
	sort.Slice(exact, func(i, j int) bool { return exact[i].Size > exact[j].Size })

	// Pass 4 (compare): perceptual hashes for near-duplicate photos. Only one
	// representative per exact group takes part, so an exact duplicate is never
	// reported twice.
	similar := findSimilar(all, inExact, repOf, byFull)

	if exact == nil {
		exact = []DuplicateGroup{}
	}
	if similar == nil {
		similar = []DuplicateGroup{}
	}
	log.Printf("[DUPES] %d files → %d sample candidates → %d full-hashed → %d exact groups (%s wasted), %d similar groups",
		len(all), len(sampleWork), len(fullWork), len(exact), fmtSizeGo(totalWaste), len(similar))
	return exact, similar, totalWaste
}

// findSimilar clusters photos whose perceptual hashes are within
// similarThreshold bits of each other.
const similarThreshold = 7 // out of 64 bits (~89% match or better)

func findSimilar(all []dupCandidate, inExact map[string]bool, repOf map[string]dupCandidate, byFull map[string][]dupCandidate) []DuplicateGroup {
	// Candidate set: every image that is not part of an exact group, plus one
	// representative per exact group.
	copiesOf := map[string]int{}
	var pool []dupCandidate
	for _, c := range all {
		if !isImage(c.name) || inExact[c.rel] {
			continue
		}
		pool = append(pool, c)
	}
	for hash, rep := range repOf {
		if isImage(rep.name) {
			copiesOf[rep.rel] = len(byFull[hash])
			pool = append(pool, rep)
		}
	}
	if len(pool) < 2 {
		return nil
	}

	dupes.setPhase("comparing", int64(len(pool)))
	hashes := make([]uint64, len(pool))
	okFlag := make([]bool, len(pool))
	dims := make([][2]int, len(pool))
	idx := make([]int, len(pool))
	for i := range pool {
		idx[i] = i
	}
	parallelFor(idx, func(i int) {
		c := pool[i]
		if e, ok := cachedEntry(c.rel, c.size, c.modNs); ok && e.HasPHash {
			hashes[i], okFlag[i], dims[i] = e.PHash, true, [2]int{e.Width, e.Height}
			return
		}
		h, ok := perceptualHash(c.path)
		if !ok {
			return
		}
		w, ht := imageDims(c.path)
		hashes[i], okFlag[i], dims[i] = h, true, [2]int{w, ht}
		updateEntry(c.rel, c.size, c.modNs, func(e *dupEntry) {
			e.PHash, e.HasPHash, e.Width, e.Height = h, true, w, ht
		})
	})
	if dupes.canceled() {
		return nil
	}

	// Union-find over candidate pairs. To avoid an O(n^2) sweep we band the
	// 64-bit hash into 8 bytes: two hashes within 7 bits must agree on at least
	// one whole byte (pigeonhole), so only files sharing a band are compared.
	parent := make([]int, len(pool))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	for band := 0; band < 8; band++ {
		buckets := map[byte][]int{}
		for i := range pool {
			if !okFlag[i] {
				continue
			}
			b := byte(hashes[i] >> (uint(band) * 8))
			buckets[b] = append(buckets[b], i)
		}
		for _, ids := range buckets {
			if len(ids) < 2 || len(ids) > 4000 { // degenerate bucket (e.g. all-black frames)
				continue
			}
			for a := 0; a < len(ids); a++ {
				for b := a + 1; b < len(ids); b++ {
					if bits.OnesCount64(hashes[ids[a]]^hashes[ids[b]]) <= similarThreshold {
						union(ids[a], ids[b])
					}
				}
			}
		}
	}

	clusters := map[int][]int{}
	for i := range pool {
		if okFlag[i] {
			r := find(i)
			clusters[r] = append(clusters[r], i)
		}
	}

	var groups []DuplicateGroup
	for _, ids := range clusters {
		if len(ids) < 2 {
			continue
		}
		sort.Slice(ids, func(a, b int) bool { return pool[ids[a]].rel < pool[ids[b]].rel })
		files := make([]DupFile, 0, len(ids))
		var maxSize int64
		for _, i := range ids {
			c := pool[i]
			if c.size > maxSize {
				maxSize = c.size
			}
			files = append(files, DupFile{
				Path: c.rel, Name: c.name, Size: c.size, Mod: c.mod,
				Width: dims[i][0], Height: dims[i][1], Copies: copiesOf[c.rel],
			})
		}
		markBest(files, true)
		// Waste here is an estimate: keeping the best copy frees the others.
		var wasted int64
		for _, f := range files {
			if !f.Best {
				wasted += f.Size
			}
		}
		// Report the loosest match in the cluster, so the percentage is the
		// weakest claim we're making rather than the strongest.
		worst := 0
		for _, i := range ids {
			if d := bits.OnesCount64(hashes[ids[0]] ^ hashes[i]); d > worst {
				worst = d
			}
		}
		groups = append(groups, DuplicateGroup{
			Hash:       fmt.Sprintf("sim-%x", hashes[ids[0]]),
			Size:       wasted,
			Files:      files,
			Kind:       "similar",
			Similarity: (64 - worst) * 100 / 64,
		})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Size > groups[j].Size })
	return groups
}

// ── "Which copy should I keep?" ──────────────────────────────────────────────

var copyNameRe = regexp.MustCompile(`(?i)(\bcopy\b|\(\d+\)|[-_ ]\d+$|~\d+$)`)

// markBest flags the file we recommend keeping. For similar groups the highest
// resolution (then largest file) wins, because that's the original rather than
// the re-compressed share. For exact groups every copy is identical, so it
// comes down to which one has the most "original" name and location.
func markBest(files []DupFile, preferBigger bool) {
	if len(files) == 0 {
		return
	}
	score := func(f DupFile) (int, int64, int64, int) {
		penalty := 0
		stem := strings.TrimSuffix(f.Name, filepath.Ext(f.Name))
		if copyNameRe.MatchString(stem) {
			penalty += 2
		}
		if strings.HasPrefix(strings.ToLower(f.Path), strings.ToLower(uploadDir)+"/") {
			penalty++
		}
		penalty += strings.Count(f.Path, "/") // prefer shallower, curated folders
		area := int64(f.Width) * int64(f.Height)
		size := f.Size
		if !preferBigger {
			area, size = 0, 0
		}
		return penalty, area, size, len(f.Name)
	}
	bestIdx := 0
	bp, ba, bs, bn := score(files[0])
	for i := 1; i < len(files); i++ {
		p, a, s, n := score(files[i])
		better := false
		switch {
		case a != ba:
			better = a > ba // higher resolution wins first (similar groups)
		case s != bs:
			better = s > bs
		case p != bp:
			better = p < bp
		default:
			better = n < bn
		}
		if better {
			bestIdx, bp, ba, bs, bn = i, p, a, s, n
		}
	}
	for i := range files {
		files[i].Best = i == bestIdx
	}
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// GET /api/duplicates          — current state; starts a scan if none has run.
// GET /api/duplicates?rescan=1 — force a fresh scan.
func duplicatesHandler(w http.ResponseWriter, r *http.Request) {
	rescan := r.URL.Query().Get("rescan") == "1"
	dupes.mu.Lock()
	if !dupes.running && (dupes.finishedAt.IsZero() || rescan) {
		dupes.running = true
		dupes.cancel = false
		dupes.phase = "indexing"
		dupes.total = 0
		atomic.StoreInt64(&dupes.processed, 0)
		dupes.groups, dupes.similar, dupes.totalWaste, dupes.err = nil, nil, 0, ""
		go runDuplicateScan()
	}
	resp := map[string]any{
		"scanning":   dupes.running,
		"phase":      dupes.phase,
		"processed":  atomic.LoadInt64(&dupes.processed),
		"total":      dupes.total,
		"groups":     []DuplicateGroup{},
		"similar":    []DuplicateGroup{},
		"totalWaste": dupes.totalWaste,
	}
	if !dupes.running {
		if dupes.groups != nil {
			resp["groups"] = dupes.groups
		}
		if dupes.similar != nil {
			resp["similar"] = dupes.similar
		}
	}
	if dupes.err != "" {
		resp["error"] = dupes.err
	}
	dupes.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// POST /api/duplicates/cancel — stop an in-flight scan.
func dupesCancelHandler(w http.ResponseWriter, r *http.Request) {
	dupes.mu.Lock()
	if dupes.running {
		dupes.cancel = true
	}
	dupes.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// POST /api/duplicates/resolve {groups:[hash...], all:bool, kind:"exact"|"similar"}
// Trashes every file in the named groups except the recommended keeper.
func dupesResolveHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Groups []string `json:"groups"`
		All    bool     `json:"all"`
		Kind   string   `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	want := map[string]bool{}
	for _, g := range body.Groups {
		want[g] = true
	}

	dupes.mu.Lock()
	var pool []DuplicateGroup
	if body.Kind != "similar" {
		pool = append(pool, dupes.groups...)
	}
	if body.Kind != "exact" {
		pool = append(pool, dupes.similar...)
	}
	dupes.mu.Unlock()

	trashed, freed := 0, int64(0)
	var errs []string
	for _, g := range pool {
		if !body.All && !want[g.Hash] {
			continue
		}
		for _, f := range g.Files {
			if f.Best {
				continue // always keep the recommended copy
			}
			full, err := safePath(baseDir, f.Path)
			if err != nil || full == baseDir {
				errs = append(errs, f.Path+": invalid path")
				continue
			}
			if err := moveToTrash(full, f.Path); err != nil {
				errs = append(errs, f.Path+": "+err.Error())
				continue
			}
			trashed++
			freed += f.Size
		}
	}
	if trashed > 0 {
		log.Printf("[DUPES] cleanup trashed %d file(s), freeing %s", trashed, fmtSizeGo(freed))
		// Results now reference trashed files — drop them so the UI re-scans.
		dupes.mu.Lock()
		dupes.groups, dupes.similar, dupes.totalWaste = nil, nil, 0
		dupes.finishedAt = time.Time{}
		dupes.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"trashed": trashed, "freed": freed, "errors": errs})
}
