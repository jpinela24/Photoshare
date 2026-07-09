package main

// Face recognition (Phase 2). Built on the same ML sidecar as semantic search,
// but deliberately the *cheap* configuration and OFF by default:
//
//   - Runs only when FACES=1 (and ML_URL is set). No opt-in ⇒ zero CPU cost.
//   - Uses the sidecar's small buffalo_s model at a 320px detector.
//   - The background detector NEVER runs while the CLIP indexer is working, so
//     the box is never doing both at once, and it throttles harder than CLIP.
//   - Grouping faces into people is pure vector math done on demand (and cached),
//     not a continuous background job.
//
// Detected faces are stored per photo (multiple per image) with L2-normalized
// ArcFace embeddings and boxes normalized to 0..1 so the UI can crop a face out
// of any rendering of the original.

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

var facesOn bool // FACES=1; face work only happens when this AND aiEnabled()

func facesEnabled() bool { return aiEnabled() && facesOn }

// ── Face store ───────────────────────────────────────────────────────────────
// relPath → faces found in it (+ source mtime). We store an entry even when a
// photo has zero faces, so it isn't re-detected every pass.

type faceRec struct {
	Box   [4]float32 // x1,y1,x2,y2 normalized to 0..1 of the source image
	Vec   []float32  // 512-dim, L2-normalized
	Score float32
}

type faceEntry struct {
	Mtime int64
	Faces []faceRec
}

type faceStore struct {
	mu   sync.RWMutex
	m    map[string]faceEntry
	path string
	ver  int // bumped on any change; invalidates the cluster cache
}

func newFaceStore(path string) *faceStore {
	s := &faceStore{m: map[string]faceEntry{}, path: path}
	if data, err := os.ReadFile(path); err == nil {
		gob.NewDecoder(bytes.NewReader(data)).Decode(&s.m)
	}
	return s
}

func (s *faceStore) get(rel string) (faceEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.m[rel]
	return e, ok
}

func (s *faceStore) put(rel string, e faceEntry) {
	s.mu.Lock()
	s.m[rel] = e
	s.ver++
	s.mu.Unlock()
}

// counts returns (images indexed, total faces).
func (s *faceStore) counts() (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	faces := 0
	for _, e := range s.m {
		faces += len(e.Faces)
	}
	return len(s.m), faces
}

func (s *faceStore) prune(exists func(rel string) bool) {
	s.mu.Lock()
	for rel := range s.m {
		if !exists(rel) {
			delete(s.m, rel)
			s.ver++
		}
	}
	s.mu.Unlock()
}

func (s *faceStore) save() {
	s.mu.RLock()
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(s.m)
	s.mu.RUnlock()
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, buf.Bytes(), 0644) == nil {
		os.Rename(tmp, s.path)
	}
}

var faceStoreG *faceStore

// personNames maps a stable face key ("relPath#index") → assigned name, so a
// name sticks to whichever cluster ends up containing that face on re-cluster.
var personNames struct {
	mu   sync.RWMutex
	m    map[string]string
	path string
}

func loadPersonNames(path string) {
	personNames.m = map[string]string{}
	personNames.path = path
	if data, err := os.ReadFile(path); err == nil {
		gob.NewDecoder(bytes.NewReader(data)).Decode(&personNames.m)
	}
}

func savePersonNames() {
	personNames.mu.RLock()
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(personNames.m)
	personNames.mu.RUnlock()
	tmp := personNames.path + ".tmp"
	if os.WriteFile(tmp, buf.Bytes(), 0644) == nil {
		os.Rename(tmp, personNames.path)
	}
}

// ── Index state (for the status endpoint) ────────────────────────────────────

var faceState struct {
	mu        sync.Mutex
	running   bool
	total     int
	processed int
}

// ── Sidecar client ───────────────────────────────────────────────────────────

func mlDetectFaces(jpeg []byte) ([]faceRec, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "image.jpg")
	fw.Write(jpeg)
	w.Close()
	req, _ := http.NewRequest(http.MethodPost, aiURL+"/faces/detect", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := aiHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("faces %d", resp.StatusCode)
	}
	var out struct {
		Faces []struct {
			Box       [4]float32 `json:"box"`
			Score     float32    `json:"score"`
			Embedding []float32  `json:"embedding"`
		} `json:"faces"`
		W float32 `json:"w"`
		H float32 `json:"h"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.W == 0 || out.H == 0 {
		return nil, fmt.Errorf("bad image dims")
	}
	recs := make([]faceRec, 0, len(out.Faces))
	for _, f := range out.Faces {
		if len(f.Embedding) == 0 {
			continue
		}
		recs = append(recs, faceRec{
			Box: [4]float32{
				clamp01(f.Box[0] / out.W), clamp01(f.Box[1] / out.H),
				clamp01(f.Box[2] / out.W), clamp01(f.Box[3] / out.H),
			},
			Vec:   normalize(f.Embedding),
			Score: f.Score,
		})
	}
	return recs, nil
}

func clamp01(x float32) float32 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// ── Background detector ──────────────────────────────────────────────────────

func aiIsRunning() bool {
	aiState.mu.Lock()
	defer aiState.mu.Unlock()
	return aiState.running
}

func faceIndexer() {
	if !facesEnabled() {
		return
	}
	for i := 0; i < 60 && !mlHealthy(); i++ {
		time.Sleep(5 * time.Second)
	}
	for {
		faceIndexOnce()
		time.Sleep(10 * time.Minute)
	}
}

func faceIndexOnce() {
	if baseDir == "" || !mlHealthy() {
		return
	}
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
		if e, ok := faceStoreG.get(rel); ok && e.Mtime == info.ModTime().Unix() {
			return nil
		}
		jobs = append(jobs, job{path, rel, info.ModTime().Unix()})
		return nil
	})

	faceStoreG.prune(func(rel string) bool { return present[rel] })

	faceState.mu.Lock()
	faceState.running = true
	faceState.total = len(jobs)
	faceState.processed = 0
	faceState.mu.Unlock()
	defer func() {
		faceState.mu.Lock()
		faceState.running = false
		faceState.mu.Unlock()
	}()

	if len(jobs) > 0 {
		log.Printf("[FACES] scanning %d image(s)…", len(jobs))
	}
	for i, j := range jobs {
		// Yield the CPU entirely while CLIP is indexing.
		for aiIsRunning() {
			time.Sleep(30 * time.Second)
		}
		// Faces want more resolution than CLIP's 336px thumbnail, but keep it
		// bounded to stay light — 768px is a reasonable recall/CPU trade.
		jpeg, err := decodeFitJPEG(j.full, filepath.Base(j.rel), 768)
		if err == nil {
			if faces, err := mlDetectFaces(jpeg); err == nil {
				faceStoreG.put(j.rel, faceEntry{Mtime: j.mtime, Faces: faces})
			}
		}
		faceState.mu.Lock()
		faceState.processed = i + 1
		faceState.mu.Unlock()
		if (i+1)%25 == 0 {
			faceStoreG.save()
		}
		time.Sleep(200 * time.Millisecond) // heavier throttle than CLIP
	}
	faceStoreG.save()
	if len(jobs) > 0 {
		imgs, faces := faceStoreG.counts()
		log.Printf("[FACES] scan done — %d faces across %d image(s)", faces, imgs)
	}
}

// ── Clustering (on demand, cached) ───────────────────────────────────────────

type faceLoc struct {
	Rel   string
	Idx   int
	Box   [4]float32
	Score float32
}

type personCluster struct {
	Key     string    // stable key of the representative face ("rel#idx")
	Name    string    // assigned name, if any
	Members []faceLoc // all faces in the cluster
	cover   faceLoc   // highest-scoring face
}

// faceClusterThreshold: cosine similarity above which two faces are the same
// person. ArcFace normed embeddings typically separate same/different around
// ~0.4–0.5; 0.5 favors precision (fewer wrong merges) over recall.
const faceClusterThreshold = 0.5
const faceMinScore = 0.55 // ignore low-confidence detections when clustering

var clusterCache struct {
	mu     sync.Mutex
	ver    int
	people []personCluster
}

func dot(a, b []float32) float32 {
	var s float32
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		s += a[i] * b[i]
	}
	return s
}

// computePeople clusters all stored faces, greedy online. Cached until the face
// store changes, so repeated People-view requests don't recompute.
func computePeople() []personCluster {
	faceStoreG.mu.RLock()
	storeVer := faceStoreG.ver
	faceStoreG.mu.RUnlock()

	clusterCache.mu.Lock()
	defer clusterCache.mu.Unlock()
	if clusterCache.people != nil && clusterCache.ver == storeVer {
		return clusterCache.people
	}

	// Gather qualifying faces (deterministic order for stable clustering).
	type gf struct {
		loc faceLoc
		vec []float32
	}
	faceStoreG.mu.RLock()
	rels := make([]string, 0, len(faceStoreG.m))
	for rel := range faceStoreG.m {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	var all []gf
	for _, rel := range rels {
		for i, f := range faceStoreG.m[rel].Faces {
			if f.Score < faceMinScore {
				continue
			}
			all = append(all, gf{faceLoc{rel, i, f.Box, f.Score}, f.Vec})
		}
	}
	faceStoreG.mu.RUnlock()

	type cl struct {
		sum     []float32 // running sum of member vectors
		members []faceLoc
	}
	var clusters []*cl
	for _, g := range all {
		best, bestDot := -1, float32(faceClusterThreshold)
		for ci, c := range clusters {
			cent := normalize(append([]float32(nil), c.sum...))
			if d := dot(g.vec, cent); d >= bestDot {
				best, bestDot = ci, d
			}
		}
		if best < 0 {
			ns := append([]float32(nil), g.vec...)
			clusters = append(clusters, &cl{sum: ns, members: []faceLoc{g.loc}})
		} else {
			c := clusters[best]
			for i := range c.sum {
				c.sum[i] += g.vec[i]
			}
			c.members = append(c.members, g.loc)
		}
	}

	people := make([]personCluster, 0, len(clusters))
	for _, c := range clusters {
		cover := c.members[0]
		for _, m := range c.members {
			if m.Score > cover.Score {
				cover = m
			}
		}
		key := fmt.Sprintf("%s#%d", cover.Rel, cover.Idx)
		people = append(people, personCluster{Key: key, Members: c.members, cover: cover})
	}
	// Attach any saved names: a name is keyed to a specific face; the cluster
	// holding that face inherits it.
	personNames.mu.RLock()
	for i := range people {
		for _, m := range people[i].Members {
			if name, ok := personNames.m[fmt.Sprintf("%s#%d", m.Rel, m.Idx)]; ok {
				people[i].Name = name
				break
			}
		}
	}
	personNames.mu.RUnlock()

	// Named first, then largest groups first.
	sort.SliceStable(people, func(i, j int) bool {
		ni, nj := people[i].Name != "", people[j].Name != ""
		if ni != nj {
			return ni
		}
		return len(people[i].Members) > len(people[j].Members)
	})

	clusterCache.ver = storeVer
	clusterCache.people = people
	return people
}

// ── HTTP handlers ────────────────────────────────────────────────────────────

// GET /api/faces/status → {enabled, healthy, running, total, processed, images, faces, people}
func facesStatusHandler(w http.ResponseWriter, r *http.Request) {
	faceState.mu.Lock()
	running, total, processed := faceState.running, faceState.total, faceState.processed
	faceState.mu.Unlock()
	images, faces := 0, 0
	people := 0
	if faceStoreG != nil {
		images, faces = faceStoreG.counts()
		// Only report people when clustering is already cached — computing it
		// here could be heavy and status is polled frequently.
		clusterCache.mu.Lock()
		people = len(clusterCache.people)
		clusterCache.mu.Unlock()
	}
	writeJSON(w, map[string]any{
		"enabled":   facesEnabled(),
		"healthy":   facesEnabled() && mlHealthy(),
		"running":   running,
		"total":     total,
		"processed": processed,
		"images":    images,
		"faces":     faces,
		"people":    people,
	})
}

type personOut struct {
	Key   string     `json:"key"`
	Name  string     `json:"name"`
	Count int        `json:"count"`
	Cover string     `json:"cover"` // photo relPath of the representative face
	Box   [4]float32 `json:"box"`   // normalized crop box of the representative face
}

// GET /api/faces/people?min=2 → clustered people, named/largest first
func peopleHandler(w http.ResponseWriter, r *http.Request) {
	if !facesEnabled() {
		http.Error(w, "face recognition is not enabled on this server", http.StatusNotImplemented)
		return
	}
	min := 2
	if n, err := strconv.Atoi(r.URL.Query().Get("min")); err == nil && n > 0 {
		min = n
	}
	people := computePeople()
	out := make([]personOut, 0, len(people))
	for _, p := range people {
		if len(p.Members) < min {
			continue
		}
		out = append(out, personOut{
			Key: p.Key, Name: p.Name, Count: len(p.Members),
			Cover: p.cover.Rel, Box: p.cover.Box,
		})
	}
	writeJSON(w, out)
}

// GET /api/faces/photos?key=… → the photos a given person appears in
func personPhotosHandler(w http.ResponseWriter, r *http.Request) {
	if !facesEnabled() {
		http.Error(w, "face recognition is not enabled on this server", http.StatusNotImplemented)
		return
	}
	key := r.URL.Query().Get("key")
	for _, p := range computePeople() {
		if p.Key != key {
			continue
		}
		seen := map[string]bool{}
		out := make([]SearchEntry, 0, len(p.Members))
		for _, m := range p.Members {
			if seen[m.Rel] {
				continue
			}
			if _, err := os.Stat(filepath.Join(baseDir, filepath.FromSlash(m.Rel))); err != nil {
				continue
			}
			seen[m.Rel] = true
			out = append(out, SearchEntry{
				Name:   filepath.Base(m.Rel),
				Path:   m.Rel,
				Parent: filepath.ToSlash(filepath.Dir(m.Rel)),
			})
		}
		writeJSON(w, out)
		return
	}
	writeJSON(w, []SearchEntry{})
}

// POST /api/faces/name {key, name} → name (or, with empty name, clear) a person
func nameFaceHandler(w http.ResponseWriter, r *http.Request) {
	if !facesEnabled() {
		http.Error(w, "face recognition is not enabled on this server", http.StatusNotImplemented)
		return
	}
	var body struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	personNames.mu.Lock()
	if name == "" {
		delete(personNames.m, body.Key)
	} else {
		personNames.m[body.Key] = name
	}
	personNames.mu.Unlock()
	savePersonNames()
	// Force the cluster cache to re-attach names on next read.
	clusterCache.mu.Lock()
	clusterCache.people = nil
	clusterCache.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

// faceInit wires up faces from the environment. Called once from main(); a cheap
// no-op unless both ML_URL is set and FACES=1.
func faceInit() {
	if !aiEnabled() {
		return
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("FACES")))
	facesOn = v == "1" || v == "true" || v == "yes"
	if !facesOn {
		return
	}
	faceStoreG = newFaceStore(filepath.Join(dataDir, "faces-index.gob"))
	loadPersonNames(filepath.Join(dataDir, "faces-names.gob"))
	imgs, faces := faceStoreG.counts()
	log.Printf("[FACES] enabled — %d faces across %d image(s) loaded", faces, imgs)
	go faceIndexer()
}
