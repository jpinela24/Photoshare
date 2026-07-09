package main

// AI semantic search (Phase 1). PhotoShare talks to an external "ML sidecar"
// (Python + CLIP) over HTTP to turn images and text queries into embedding
// vectors, then ranks photos by cosine similarity — all local, no cloud.
//
// Everything here is a no-op unless ML_URL is set, so Docker/Windows builds
// without the sidecar behave exactly as before. Videos and un-decodable files
// are skipped in this phase; images (incl. HEIC) are indexed.

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
)

var aiURL string // ML sidecar base URL (env ML_URL); empty = AI disabled

func aiEnabled() bool { return aiURL != "" }

// ── Vector store ─────────────────────────────────────────────────────────────
// relPath → embedding (+ source mtime for incremental re-indexing). Persisted
// as gob in DATA_DIR so a restart doesn't re-embed the whole library.

type vecEntry struct {
	Vec   []float32
	Mtime int64
}

type vecStore struct {
	mu   sync.RWMutex
	m    map[string]vecEntry
	path string
}

func newVecStore(path string) *vecStore {
	s := &vecStore{m: map[string]vecEntry{}, path: path}
	if data, err := os.ReadFile(path); err == nil {
		gob.NewDecoder(bytes.NewReader(data)).Decode(&s.m)
	}
	return s
}

func (s *vecStore) get(rel string) (vecEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.m[rel]
	return e, ok
}

func (s *vecStore) put(rel string, e vecEntry) {
	s.mu.Lock()
	s.m[rel] = e
	s.mu.Unlock()
}

func (s *vecStore) len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

// prune drops entries whose file no longer exists (deleted/moved photos).
func (s *vecStore) prune(exists func(rel string) bool) {
	s.mu.Lock()
	for rel := range s.m {
		if !exists(rel) {
			delete(s.m, rel)
		}
	}
	s.mu.Unlock()
}

func (s *vecStore) save() {
	s.mu.RLock()
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(s.m)
	s.mu.RUnlock()
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, buf.Bytes(), 0644) == nil {
		os.Rename(tmp, s.path)
	}
}

var aiStore *vecStore

// ── Index state (for the status endpoint) ───────────────────────────────────

var aiState struct {
	mu        sync.Mutex
	running   bool
	total     int // indexable images seen this pass
	processed int // embedded so far this pass
}

// ── ML sidecar client ────────────────────────────────────────────────────────

var aiHTTP = &http.Client{Timeout: 60 * time.Second}

func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	n := math.Sqrt(sum)
	if n == 0 {
		return v
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
	return v
}

func mlEmbedImage(jpeg []byte) ([]float32, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "image.jpg")
	fw.Write(jpeg)
	w.Close()
	req, _ := http.NewRequest(http.MethodPost, aiURL+"/clip/image", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return mlEmbedDo(req)
}

func mlEmbedText(text string) ([]float32, error) {
	b, _ := json.Marshal(map[string]string{"text": text})
	req, _ := http.NewRequest(http.MethodPost, aiURL+"/clip/text", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return mlEmbedDo(req)
}

func mlEmbedDo(req *http.Request) ([]float32, error) {
	resp, err := aiHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ml %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding")
	}
	return normalize(out.Embedding), nil
}

// mlHealthy reports whether the sidecar answers, so the UI only offers smart
// search when it will actually work.
func mlHealthy() bool {
	if !aiEnabled() {
		return false
	}
	req, _ := http.NewRequest(http.MethodGet, aiURL+"/health", nil)
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ── Embedding input: a small JPEG for any indexable image (HEIC included) ─────

func embeddingJPEG(full, name string) ([]byte, error) {
	return decodeFitJPEG(full, name, 336)
}

// decodeFitJPEG decodes any indexable image (HEIC included), fits it within
// size×size, and re-encodes as JPEG — the compact input both the CLIP and face
// endpoints consume. Larger size = better recall, more CPU.
func decodeFitJPEG(full, name string, size int) ([]byte, error) {
	var img image.Image
	var err error
	if isHeic(name) {
		tmp := filepath.Join(os.TempDir(), fmt.Sprintf("ai-%d.jpg", time.Now().UnixNano()))
		if e := heicToJPEG(full, tmp); e != nil {
			return nil, e
		}
		defer os.Remove(tmp)
		img, err = imaging.Open(tmp, imaging.AutoOrientation(true))
	} else {
		img, err = imaging.Open(full, imaging.AutoOrientation(true))
	}
	if err != nil {
		return nil, err
	}
	img = imaging.Fit(img, size, size, imaging.Lanczos)
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.JPEG, imaging.JPEGQuality(85)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── Background indexer ───────────────────────────────────────────────────────
// Sequential + throttled: embedding is CPU-heavy on a homelab box, so we do one
// image at a time with a small pause, and persist periodically so it's
// resumable across restarts.

func aiIndexer() {
	if !aiEnabled() {
		return
	}
	// Wait for the sidecar to come up (it loads a model on boot).
	for i := 0; i < 60 && !mlHealthy(); i++ {
		time.Sleep(5 * time.Second)
	}
	for {
		aiIndexOnce()
		time.Sleep(5 * time.Minute) // pick up newly added photos
	}
}

func aiIndexOnce() {
	if baseDir == "" || !mlHealthy() {
		return
	}
	// Collect indexable images (skip trash/upload/hidden, videos, and files
	// already embedded at their current mtime).
	type job struct {
		full, rel string
		mtime     int64
	}
	trashLower := strings.ToLower(filepath.Base(trashDir))
	var jobs []job
	present := map[string]bool{}
	filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			l := strings.ToLower(name)
			if l == trashLower || l == strings.ToLower(uploadDir) || hiddenFolderNames[l] {
				return filepath.SkipDir
			}
			return nil
		}
		if !isImage(name) {
			return nil
		}
		rel, _ := filepath.Rel(baseDir, path)
		rel = filepath.ToSlash(rel)
		present[rel] = true
		if e, ok := aiStore.get(rel); ok && e.Mtime == info.ModTime().Unix() {
			return nil // already embedded, unchanged
		}
		jobs = append(jobs, job{path, rel, info.ModTime().Unix()})
		return nil
	})

	// Drop embeddings for files that vanished.
	aiStore.prune(func(rel string) bool { return present[rel] })

	aiState.mu.Lock()
	aiState.running = true
	aiState.total = len(jobs)
	aiState.processed = 0
	aiState.mu.Unlock()

	if len(jobs) > 0 {
		log.Printf("[AI] indexing %d image(s)…", len(jobs))
	}
	for i, j := range jobs {
		jpeg, err := embeddingJPEG(j.full, filepath.Base(j.rel))
		if err == nil {
			if vec, err := mlEmbedImage(jpeg); err == nil {
				aiStore.put(j.rel, vecEntry{Vec: vec, Mtime: j.mtime})
			}
		}
		aiState.mu.Lock()
		aiState.processed = i + 1
		aiState.mu.Unlock()
		if (i+1)%50 == 0 {
			aiStore.save()
		}
		time.Sleep(60 * time.Millisecond) // be gentle on a weak CPU
	}
	aiStore.save()
	aiState.mu.Lock()
	aiState.running = false
	aiState.mu.Unlock()
	if len(jobs) > 0 {
		log.Printf("[AI] indexing done (%d total embedded)", aiStore.len())
	}
}

// ── Search ───────────────────────────────────────────────────────────────────

func aiSearch(query string, limit int) ([]SearchEntry, error) {
	qv, err := mlEmbedText(query)
	if err != nil {
		return nil, err
	}
	type scored struct {
		rel   string
		score float32
	}
	aiStore.mu.RLock()
	scores := make([]scored, 0, len(aiStore.m))
	for rel, e := range aiStore.m {
		var dot float32
		for i := range qv {
			if i < len(e.Vec) {
				dot += qv[i] * e.Vec[i]
			}
		}
		scores = append(scores, scored{rel, dot})
	}
	aiStore.mu.RUnlock()

	sort.Slice(scores, func(i, j int) bool { return scores[i].score > scores[j].score })
	if limit <= 0 || limit > len(scores) {
		limit = len(scores)
	}
	out := make([]SearchEntry, 0, limit)
	for _, s := range scores[:limit] {
		// Skip results the file for which is gone (defensive; prune usually handles it).
		if _, err := os.Stat(filepath.Join(baseDir, filepath.FromSlash(s.rel))); err != nil {
			continue
		}
		out = append(out, SearchEntry{
			Name:   filepath.Base(s.rel),
			Path:   s.rel,
			Parent: filepath.ToSlash(filepath.Dir(s.rel)),
		})
	}
	return out, nil
}

// ── HTTP handlers ────────────────────────────────────────────────────────────

// GET /api/ai/status → {enabled, healthy, indexed, total, running}
func aiStatusHandler(w http.ResponseWriter, r *http.Request) {
	aiState.mu.Lock()
	running, total, processed := aiState.running, aiState.total, aiState.processed
	aiState.mu.Unlock()
	indexed := 0
	if aiStore != nil {
		indexed = aiStore.len()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"enabled":   aiEnabled(),
		"healthy":   aiEnabled() && mlHealthy(),
		"indexed":   indexed,
		"running":   running,
		"total":     total,
		"processed": processed,
	})
}

// GET /api/search/semantic?q=…&limit=… → ranked SearchEntry list
func semanticSearchHandler(w http.ResponseWriter, r *http.Request) {
	if !aiEnabled() {
		http.Error(w, "AI search is not configured on this server", http.StatusNotImplemented)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	limit := 60
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	results, err := aiSearch(q, limit)
	if err != nil {
		http.Error(w, "search failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if results == nil {
		results = []SearchEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// aiInit wires up AI from the environment. Called once from main(). No-op
// (and cheap) when ML_URL is unset.
func aiInit() {
	aiURL = strings.TrimRight(strings.TrimSpace(os.Getenv("ML_URL")), "/")
	if !aiEnabled() {
		return
	}
	aiStore = newVecStore(filepath.Join(dataDir, "ai-index.gob"))
	log.Printf("[AI] enabled — ML sidecar %s (%d embeddings loaded)", aiURL, aiStore.len())
	go aiIndexer()
}
