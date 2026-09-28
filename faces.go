package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
)

// People — face detection, grouping, and the person model behind it.
//
// The design point that matters: a PERSON OWNS SEVERAL CLUSTERS, and is never
// assumed to be one face. ArcFace embeddings are trained mostly on adults and
// similarity across a big age gap — especially in childhood, where a face
// changes more between one and five than an adult's does in thirty years —
// falls a long way. Any threshold low enough to match a baby to their school
// photos is far too low to keep other people out.
//
// So clustering is deliberately tight. One person will produce several groups,
// and the user merges them once. After that a new photo is matched against
// every cluster the person owns rather than an average of them, which is what
// makes age progression work without ever loosening the threshold.
//
// This replaces the face recognition removed in 2.10.2, which auto-labelled
// with no way to correct it. Precision over recall throughout: a missed face
// costs one photo, a wrong one poisons a group and costs trust in all of them.

const (
	// Cosine similarity for buffalo_l (w600k_r50). Same-person pairs typically
	// land 0.5–0.8, different-person below ~0.35. 0.60 is deliberately above
	// the usual 0.5 operating point: it splits one person into more groups,
	// which the user can merge, instead of quietly merging two people, which
	// they cannot easily undo.
	faceSameThreshold = 0.60
	// Matching a face to an already-named person. Same value on purpose —
	// polluting a person the user has confirmed is worse than leaving a face
	// unassigned for them to place.
	facePersonThreshold = 0.60
	// Below this a cluster is noise (one blurry background face), not a person
	// worth showing. They stay in the store and can still be searched.
	faceMinClusterSize = 3
)

type Face struct {
	ID    string    // stable: path + detection index
	Path  string    // library-relative, slash-separated
	Box   [4]int    // x, y, w, h in original-image pixels
	Score float32   // detector confidence
	Emb   []float32 // 512-dim, L2-normalised
	Taken int64     // unix seconds, for age-aware merge suggestions
	// Person is "" when unassigned, "c:N" for an automatic cluster, or "p:N"
	// once the user has named it. Auto clusters become people on naming rather
	// than being a separate concept.
	Person string
}

type Person struct {
	ID    string
	Name  string
	Named bool // user-confirmed, so automatic re-clustering must not touch it
}

type faceSeen struct {
	Size  int64
	ModNs int64
	Faces int
}

type faceStore struct {
	Faces   map[string]*Face
	Persons map[string]*Person
	// Seen records which files have been through detection, keyed the same way
	// as the thumbnail cache (size + mtime) so an edited photo is re-examined
	// and an untouched one never is.
	Seen   map[string]faceSeen
	NextID int
}

var (
	faceMu    sync.Mutex
	faces     *faceStore
	faceIndex struct {
		mu      sync.Mutex
		running bool
		done    int
		total   int
		errors  int
	}
)

func facesPath() string { return filepath.Join(dataDir, "faces.gob") }

// loadFacesLocked reads the store once. A missing or unreadable file just means
// nothing has been indexed yet — never an error worth failing a request over.
func loadFacesLocked() {
	if faces != nil {
		return
	}
	faces = &faceStore{
		Faces:   map[string]*Face{},
		Persons: map[string]*Person{},
		Seen:    map[string]faceSeen{},
	}
	b, err := os.ReadFile(facesPath())
	if err != nil {
		return
	}
	var fs faceStore
	if gob.NewDecoder(bytes.NewReader(b)).Decode(&fs) != nil {
		log.Printf("[FACES] index unreadable; starting over")
		return
	}
	if fs.Faces != nil {
		faces = &fs
	}
	if faces.Persons == nil {
		faces.Persons = map[string]*Person{}
	}
	if faces.Seen == nil {
		faces.Seen = map[string]faceSeen{}
	}
}

func saveFacesLocked() {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(faces); err != nil {
		log.Printf("[FACES] encode failed: %v", err)
		return
	}
	if err := writeFileAtomic(facesPath(), buf.Bytes(), 0600); err != nil {
		log.Printf("[FACES] save failed: %v", err)
	}
}

// cosine of two L2-normalised vectors is their dot product.
func cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return -1
	}
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// ── Detection ────────────────────────────────────────────────────────────────

type detectedFace struct {
	Box   [4]int    `json:"box"`
	Score float32   `json:"score"`
	Emb   []float32 `json:"embedding"`
}

// detectFaces sends one image to the ML sidecar.
func detectFaces(mlURL, full string) ([]detectedFace, error) {
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filepath.Base(full))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return nil, err
	}
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(mlURL, "/")+"/faces/detect", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// Generous: CPU detection on a large photo is not fast, and a timeout here
	// would silently drop faces rather than reporting a problem.
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return nil, fmt.Errorf("sidecar %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Faces []detectedFace `json:"faces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Faces, nil
}

// ── Indexing ─────────────────────────────────────────────────────────────────

// faceIndexer walks the library and detects faces in anything not seen before.
// Runs once at startup (after the thumbnailer, which is the more urgent job)
// and can be re-run from the API.
func faceIndexer(mlURL string) {
	faceIndex.mu.Lock()
	if faceIndex.running {
		faceIndex.mu.Unlock()
		return
	}
	faceIndex.running = true
	faceIndex.done, faceIndex.total, faceIndex.errors = 0, 0, 0
	faceIndex.mu.Unlock()

	defer func() {
		faceIndex.mu.Lock()
		faceIndex.running = false
		faceIndex.mu.Unlock()
	}()

	var todo []string
	trashLow := strings.ToLower(filepath.Base(trashDir))
	filepath.Walk(baseDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			lower := strings.ToLower(info.Name())
			if strings.HasPrefix(info.Name(), ".") || lower == trashLow || hiddenFolderNames[lower] || p == thumbDir {
				return filepath.SkipDir
			}
			return nil
		}
		// Videos are skipped: a frame grab would need its own pipeline and the
		// payoff is small next to 15k stills.
		if strings.HasPrefix(info.Name(), ".") || !isImage(info.Name()) {
			return nil
		}
		rel, _ := filepath.Rel(baseDir, p)
		rel = filepath.ToSlash(rel)
		faceMu.Lock()
		loadFacesLocked()
		seen, ok := faces.Seen[rel]
		faceMu.Unlock()
		if ok && seen.Size == info.Size() && seen.ModNs == info.ModTime().UnixNano() {
			return nil
		}
		todo = append(todo, p)
		return nil
	})

	faceIndex.mu.Lock()
	faceIndex.total = len(todo)
	faceIndex.mu.Unlock()
	if len(todo) == 0 {
		clusterFaces()
		return
	}
	log.Printf("[FACES] scanning %d new photo(s)", len(todo))

	for _, p := range todo {
		found, err := detectFaces(mlURL, p)
		if err != nil {
			faceIndex.mu.Lock()
			faceIndex.errors++
			faceIndex.mu.Unlock()
			log.Printf("[FACES] %s: %v", filepath.Base(p), err)
			// A sidecar that is down or lacks the model will fail for every
			// file; stop rather than log 15,000 identical errors.
			if faceIndex.errors > 20 && faceIndex.done == 0 {
				log.Printf("[FACES] giving up — the sidecar is not answering")
				return
			}
			continue
		}
		rel, _ := filepath.Rel(baseDir, p)
		rel = filepath.ToSlash(rel)
		info, statErr := os.Stat(p)

		faceMu.Lock()
		loadFacesLocked()
		// Drop any previous faces for this path: the file changed, so its old
		// detections describe an image that no longer exists.
		for id, f := range faces.Faces {
			if f.Path == rel {
				delete(faces.Faces, id)
			}
		}
		var taken int64
		if statErr == nil {
			taken = fileDate(p, info).Unix()
		}
		for i, d := range found {
			id := fmt.Sprintf("%s#%d", rel, i)
			faces.Faces[id] = &Face{
				ID: id, Path: rel, Box: d.Box, Score: d.Score,
				Emb: d.Emb, Taken: taken,
			}
		}
		if statErr == nil {
			faces.Seen[rel] = faceSeen{Size: info.Size(), ModNs: info.ModTime().UnixNano(), Faces: len(found)}
		}
		faceMu.Unlock()

		faceIndex.mu.Lock()
		faceIndex.done++
		n := faceIndex.done
		faceIndex.mu.Unlock()
		// Checkpoint periodically so a restart doesn't throw away an hour.
		if n%200 == 0 {
			faceMu.Lock()
			saveFacesLocked()
			faceMu.Unlock()
			log.Printf("[FACES] %d/%d", n, len(todo))
		}
	}

	faceMu.Lock()
	saveFacesLocked()
	faceMu.Unlock()
	clusterFaces()

	faceIndex.mu.Lock()
	log.Printf("[FACES] done — %d scanned, %d errors", faceIndex.done, faceIndex.errors)
	faceIndex.mu.Unlock()
}

// ── Clustering ───────────────────────────────────────────────────────────────

// clusterFaces groups unassigned faces.
//
// Faces already belonging to a NAMED person are never re-grouped — the user's
// decision outranks the algorithm, always. Unnamed automatic clusters are
// rebuilt from scratch each pass, so adding photos refines the grouping instead
// of layering onto a stale one.
func clusterFaces() {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()

	// Named people first: anything close enough to one of their faces joins
	// them rather than starting a group of its own.
	named := map[string][]*Face{}
	var loose []*Face
	for _, f := range faces.Faces {
		if p := faces.Persons[f.Person]; p != nil && p.Named {
			named[f.Person] = append(named[f.Person], f)
			continue
		}
		f.Person = "" // clear stale auto-cluster ids before regrouping
		loose = append(loose, f)
	}

	// Deterministic order so repeated runs produce the same groups.
	sort.Slice(loose, func(i, j int) bool { return loose[i].ID < loose[j].ID })

	unmatched := loose[:0]
	for _, f := range loose {
		best, bestSim := "", float32(facePersonThreshold)
		for pid, members := range named {
			for _, m := range members {
				// Max over members, not a centroid: a person spans several ages
				// and averaging them produces a face that matches nobody.
				if s := cosine(f.Emb, m.Emb); s > bestSim {
					best, bestSim = pid, s
				}
			}
		}
		if best != "" {
			f.Person = best
			named[best] = append(named[best], f)
			continue
		}
		unmatched = append(unmatched, f)
	}

	// Greedy agglomeration over what is left. Single-linkage against a high
	// threshold: generous about one person's variation within an age, strict
	// enough that two people rarely chain together.
	var clusters [][]*Face
	for _, f := range unmatched {
		joined := false
		for ci := range clusters {
			for _, m := range clusters[ci] {
				if cosine(f.Emb, m.Emb) >= faceSameThreshold {
					clusters[ci] = append(clusters[ci], f)
					joined = true
					break
				}
			}
			if joined {
				break
			}
		}
		if !joined {
			clusters = append(clusters, []*Face{f})
		}
	}

	// Biggest first, so the People view opens on the people you photograph most.
	sort.Slice(clusters, func(i, j int) bool { return len(clusters[i]) > len(clusters[j]) })

	// Drop automatic (unnamed) people from a previous pass; named ones stay.
	for id, p := range faces.Persons {
		if !p.Named {
			delete(faces.Persons, id)
		}
	}
	n := 0
	for _, c := range clusters {
		if len(c) < faceMinClusterSize {
			continue // noise, or someone who appears once
		}
		n++
		id := fmt.Sprintf("c:%d", n)
		// Belt and braces: named groups live in the "p:" namespace, but never
		// hand out an id that is already taken.
		for faces.Persons[id] != nil {
			n++
			id = fmt.Sprintf("c:%d", n)
		}
		faces.Persons[id] = &Person{ID: id}
		for _, f := range c {
			f.Person = id
		}
	}
	saveFacesLocked()
	log.Printf("[FACES] %d face(s) in %d group(s), %d named", len(faces.Faces), n, len(named))
}

// ── Queries and edits ────────────────────────────────────────────────────────

type personSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Named bool   `json:"named"`
	Count int    `json:"count"`
	Cover string `json:"cover"` // face id to show as the group's thumbnail
	From  int64  `json:"from"`  // earliest photo, unix seconds
	To    int64  `json:"to"`    // latest — the span hints at an age range
}

func listPeople() []personSummary {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()

	by := map[string][]*Face{}
	for _, f := range faces.Faces {
		if f.Person == "" {
			continue
		}
		by[f.Person] = append(by[f.Person], f)
	}
	out := make([]personSummary, 0, len(by))
	for pid, members := range by {
		p := faces.Persons[pid]
		if p == nil {
			continue
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Score > members[j].Score })
		s := personSummary{ID: pid, Name: p.Name, Named: p.Named, Count: len(members), Cover: members[0].ID}
		for _, m := range members {
			if m.Taken == 0 {
				continue
			}
			if s.From == 0 || m.Taken < s.From {
				s.From = m.Taken
			}
			if m.Taken > s.To {
				s.To = m.Taken
			}
		}
		out = append(out, s)
	}
	// Named people first, then by size: the ones you've curated stay put as
	// automatic groups come and go with each pass.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Named != out[j].Named {
			return out[i].Named
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// promoteLocked moves a group out of the automatic "c:N" namespace into a
// stable "p:N" id, carrying its faces with it.
//
// This is what keeps a confirmed group alive: clustering renumbers c:1, c:2 …
// from scratch on every pass, so a named person left holding a c: id would be
// silently overwritten by the next pass's cluster of the same number. The
// caller must hold faceMu.
func promoteLocked(oldID string) string {
	p := faces.Persons[oldID]
	if p == nil || strings.HasPrefix(oldID, "p:") {
		return oldID // already stable
	}
	faces.NextID++
	newID := fmt.Sprintf("p:%d", faces.NextID)
	faces.Persons[newID] = &Person{ID: newID, Name: p.Name, Named: p.Named}
	delete(faces.Persons, oldID)
	for _, f := range faces.Faces {
		if f.Person == oldID {
			f.Person = newID
		}
	}
	return newID
}

// namePerson turns an automatic cluster into a confirmed person. Naming is what
// protects a group from being rebuilt by the next clustering pass.
func namePerson(id, name string) error {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	p := faces.Persons[id]
	if p == nil {
		return fmt.Errorf("no such group")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		// Clearing a name releases the group back to automatic clustering.
		p.Named = false
		p.Name = ""
		saveFacesLocked()
		return nil
	}
	p.Name = name
	p.Named = true
	promoteLocked(id) // out of the automatic namespace so re-clustering can't reuse the id
	saveFacesLocked()
	return nil
}

// mergePeople folds several groups into the first one. This is the operation
// that makes different ages work: the merged person keeps every face from every
// group, and later matching runs against all of them.
func mergePeople(ids []string) error {
	if len(ids) < 2 {
		return fmt.Errorf("need at least two groups")
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()

	target := faces.Persons[ids[0]]
	if target == nil {
		return fmt.Errorf("no such group")
	}
	keep := map[string]bool{}
	for _, id := range ids[1:] {
		if faces.Persons[id] == nil {
			return fmt.Errorf("no such group: %s", id)
		}
		keep[id] = true
	}
	// A merge is a decision, so the result is a named person even if the user
	// merged two unnamed clusters — otherwise the next pass would undo it.
	target.Named = true
	if target.Name == "" {
		for _, id := range ids[1:] {
			if n := faces.Persons[id].Name; n != "" {
				target.Name = n
				break
			}
		}
	}
	for _, f := range faces.Faces {
		if keep[f.Person] {
			f.Person = target.ID
		}
	}
	for id := range keep {
		delete(faces.Persons, id)
	}
	promoteLocked(target.ID) // a merge is a decision; give it a stable id
	saveFacesLocked()
	return nil
}

// detachFaces is the "that isn't them" correction: the faces leave the group
// and are left unassigned for the next pass to regroup.
func detachFaces(ids []string) error {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	for _, id := range ids {
		if f := faces.Faces[id]; f != nil {
			f.Person = ""
		}
	}
	saveFacesLocked()
	return nil
}

// facesOf returns a group's faces, newest photo first.
func facesOf(personID string) []*Face {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	var out []*Face
	for _, f := range faces.Faces {
		if f.Person == personID {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Taken != out[j].Taken {
			return out[i].Taken > out[j].Taken
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func faceByID(id string) *Face {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	if f := faces.Faces[id]; f != nil {
		cp := *f
		return &cp
	}
	return nil
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

// GET /api/faces/status — indexing progress and whether faces are available.
func facesStatusHandler(w http.ResponseWriter, r *http.Request) {
	faceIndex.mu.Lock()
	running, done, total, errs := faceIndex.running, faceIndex.done, faceIndex.total, faceIndex.errors
	faceIndex.mu.Unlock()

	faceMu.Lock()
	loadFacesLocked()
	nFaces, nSeen := len(faces.Faces), len(faces.Seen)
	faceMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"enabled":  faceMLURL() != "",
		"running":  running,
		"done":     done,
		"total":    total,
		"errors":   errs,
		"faces":    nFaces,
		"scanned":  nSeen,
		"people":   len(listPeople()),
		"minGroup": faceMinClusterSize,
	})
}

// GET /api/people — the groups, named ones first.
func peopleListHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"people": listPeople()})
}

// GET /api/people/faces?id= — one group's faces.
func peopleFacesHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	type item struct {
		ID    string `json:"id"`
		Path  string `json:"path"`
		Name  string `json:"name"`
		Taken int64  `json:"taken"`
	}
	out := []item{}
	for _, f := range facesOf(id) {
		out = append(out, item{ID: f.ID, Path: f.Path, Name: filepath.Base(f.Path), Taken: f.Taken})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"faces": out})
}

// GET /api/faces/crop?id= — the face, cropped from the original with a little
// context around it so it reads as a portrait rather than a cut-out.
func faceCropHandler(w http.ResponseWriter, r *http.Request) {
	f := faceByID(r.URL.Query().Get("id"))
	if f == nil {
		http.Error(w, "no such face", http.StatusNotFound)
		return
	}
	full, err := safePath(baseDir, f.Path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	// Cached like any other derivative, so a group of 200 faces isn't 200
	// decodes of full-size photos every time it's opened.
	cache := cachePathFor(full, "_face"+strings.TrimPrefix(f.ID[strings.LastIndex(f.ID, "#"):], "#"))
	if _, err := os.Stat(cache); err == nil {
		http.ServeFile(w, r, cache)
		return
	}
	img, err := imaging.Open(full, imaging.AutoOrientation(true))
	if err != nil {
		http.Error(w, "cannot read photo", http.StatusInternalServerError)
		return
	}
	x, y, bw, bh := f.Box[0], f.Box[1], f.Box[2], f.Box[3]
	pad := (bw + bh) / 5 // ~20% of the face on each side
	rect := image.Rect(x-pad, y-pad, x+bw+pad, y+bh+pad).Intersect(img.Bounds())
	if rect.Empty() {
		http.Error(w, "bad crop", http.StatusInternalServerError)
		return
	}
	crop := imaging.Thumbnail(imaging.Crop(img, rect), 160, 160, imaging.Lanczos)
	if err := imaging.Save(crop, cache); err != nil {
		// Serving still works without the cache; only the next request pays.
		var buf bytes.Buffer
		imaging.Encode(&buf, crop, imaging.JPEG)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(buf.Bytes())
		return
	}
	http.ServeFile(w, r, cache)
}

// POST /api/people/name  {"id":"c:3","name":"Alex"}
func peopleNameHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct{ ID, Name string }
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := namePerson(body.ID, body.Name); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// POST /api/people/merge  {"ids":["p:1","c:7"]}
func peopleMergeHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := mergePeople(body.IDs); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// POST /api/people/detach  {"faceIds":[...]} — "that isn't them".
func peopleDetachHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		FaceIDs []string `json:"faceIds"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := detachFaces(body.FaceIDs); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// POST /api/faces/scan — re-run detection over anything new, then regroup.
func facesScanHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ml := faceMLURL()
	if ml == "" {
		http.Error(w, "face detection is not configured", http.StatusNotImplemented)
		return
	}
	go faceIndexer(ml)
	w.WriteHeader(http.StatusAccepted)
}

func faceMLURL() string { return strings.TrimSpace(os.Getenv("ML_URL")) }
