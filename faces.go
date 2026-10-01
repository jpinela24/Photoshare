package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
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
	// Suggesting a merge, which is a much weaker claim than making one: the
	// user looks at two faces and decides. Sits between "different people"
	// (below ~0.35) and the 0.60 that would have merged them automatically,
	// so it surfaces exactly the gap the clusterer leaves — one person split
	// across ages — without dragging in strangers.
	faceSuggestThreshold = 0.45
	// Faces sampled per group when scoring a pair properly. All of them would
	// be O(|A|x|B|) against groups that run to thousands of faces.
	faceSuggestSample = 24
	// Pairs kept from the cheap centroid pass for that proper scoring.
	faceSuggestShortlist = 250
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
	// Dismissed remembers "these two are not the same person", keyed by a pair
	// of face ids rather than group ids: unnamed groups are deleted and
	// renumbered on every clustering pass, so a dismissal keyed on c:3 would
	// silently come to mean a different pair of people. Faces outlive that.
	Dismissed map[string]bool
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
	// Absent from any store written before suggestions existed.
	if faces.Dismissed == nil {
		faces.Dismissed = map[string]bool{}
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

// sidecarError is a reply from a sidecar that is up and working — it looked at
// this file and refused it. That is a completely different thing from the
// sidecar being unreachable, and conflating the two is what made one
// unsupported format look like an outage.
type sidecarError struct {
	Status int
	Msg    string
}

func (e *sidecarError) Error() string { return fmt.Sprintf("sidecar %d: %s", e.Status, e.Msg) }

// fatal reports whether this reply means every other file will fail too.
// 501 is "no face model installed"; everything else is about this one file.
func (e *sidecarError) fatal() bool { return e.Status == http.StatusNotImplemented }

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
		return nil, &sidecarError{Status: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
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
		if n := pruneMissingFaces(); n > 0 {
			log.Printf("[FACES] dropped %d face(s) whose photo is gone", n)
		}
		clusterFaces()
		return
	}
	log.Printf("[FACES] scanning %d new photo(s)", len(todo))
	rejected, unreachable := 0, 0

	for _, p := range todo {
		found, err := detectFaces(mlURL, p)
		if err != nil {
			faceIndex.mu.Lock()
			faceIndex.errors++
			faceIndex.mu.Unlock()

			// Distinguish "this file was refused" from "the sidecar is gone".
			// A rejected file is normal — an unreadable photo, or a format the
			// sidecar can't decode — and the scan must carry on past it.
			// Treating the two the same aborted whole runs over one bad format.
			var se *sidecarError
			if errors.As(err, &se) {
				if se.fatal() {
					log.Printf("[FACES] stopping: %v", err)
					return
				}
				rejected++
				// Don't print the same complaint thousands of times; the count
				// is reported at the end.
				if rejected <= 10 {
					log.Printf("[FACES] skipped %s: %v", filepath.Base(p), err)
				} else if rejected == 11 {
					log.Printf("[FACES] …further skipped files will be counted, not listed")
				}
				continue
			}

			// Anything else is transport: connection refused, timeout, a reply
			// that isn't JSON. Those do mean the sidecar is unreachable.
			unreachable++
			log.Printf("[FACES] %s: %v", filepath.Base(p), err)
			if unreachable >= 10 {
				log.Printf("[FACES] giving up — the sidecar is unreachable after %d attempts", unreachable)
				return
			}
			continue
		}
		unreachable = 0 // a success means it is alive; only a run of failures counts
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
	// Prune here rather than in clusterFaces: this pass is already reading the
	// filesystem, and clustering is otherwise a pure operation on embeddings
	// that has no business stat-ing 20,000 files.
	if n := pruneMissingFaces(); n > 0 {
		log.Printf("[FACES] dropped %d face(s) whose photo is gone", n)
	}
	clusterFaces()

	faceIndex.mu.Lock()
	done := faceIndex.done
	faceIndex.mu.Unlock()
	log.Printf("[FACES] done — %d scanned, %d skipped", done, rejected)
	if rejected > 0 {
		log.Printf("[FACES] %d file(s) the sidecar could not read — usually an unsupported format", rejected)
	}
}

// renameFacePath follows a photo that moved.
//
// Faces are keyed by path three times over — the face id, Face.Path, and the
// Seen record — so a move without this leaves the crops pointing at nothing AND
// makes the photo look unscanned, so the next pass detects it again and the
// person quietly gains a duplicate of every face in it.
func renameFacePath(oldRel, newRel string) {
	oldRel, newRel = filepath.ToSlash(oldRel), filepath.ToSlash(newRel)
	if oldRel == newRel {
		return
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	if renameFaceLocked(oldRel, newRel) {
		saveFacesLocked()
	}
}

// renameFaceLocked rewrites one path. The caller must hold faceMu.
func renameFaceLocked(oldRel, newRel string) bool {
	changed := false
	for id, fc := range faces.Faces {
		if fc.Path != oldRel {
			continue
		}
		delete(faces.Faces, id)
		// The id embeds the path, so it has to be rebuilt or the crop endpoint
		// would look the face up under a name nothing else uses.
		idx := id[strings.LastIndex(id, "#"):]
		fc.Path = newRel
		fc.ID = newRel + idx
		faces.Faces[fc.ID] = fc
		changed = true
	}
	if seen, ok := faces.Seen[oldRel]; ok {
		delete(faces.Seen, oldRel)
		faces.Seen[newRel] = seen
		changed = true
	}
	return changed
}

// renameFacePrefix follows a whole folder, for every photo underneath it.
func renameFacePrefix(oldDir, newDir string) {
	oldDir = strings.TrimSuffix(filepath.ToSlash(oldDir), "/")
	newDir = strings.TrimSuffix(filepath.ToSlash(newDir), "/")
	if oldDir == newDir || oldDir == "" {
		return
	}
	prefix := oldDir + "/"
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()

	// Collect first: renameFaceLocked mutates the maps being ranged over.
	var moves [][2]string
	seenPaths := map[string]bool{}
	collect := func(p string) {
		// Match on path segments, never a raw prefix — moving "Trip" must not
		// drag "Trips-other" along with it.
		if p != oldDir && !strings.HasPrefix(p, prefix) {
			return
		}
		if seenPaths[p] {
			return
		}
		seenPaths[p] = true
		moves = append(moves, [2]string{p, newDir + strings.TrimPrefix(p, oldDir)})
	}
	for _, fc := range faces.Faces {
		collect(fc.Path)
	}
	for p := range faces.Seen {
		collect(p)
	}
	changed := false
	for _, m := range moves {
		if renameFaceLocked(m[0], m[1]) {
			changed = true
		}
	}
	if changed {
		saveFacesLocked()
	}
}

// forgetFaces drops everything recorded for a photo that is gone, so a deleted
// file doesn't leave broken crops sitting in a person's group forever.
func forgetFaces(rel string) {
	rel = filepath.ToSlash(rel)
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	changed := false
	for id, fc := range faces.Faces {
		if fc.Path == rel {
			delete(faces.Faces, id)
			changed = true
		}
	}
	if _, ok := faces.Seen[rel]; ok {
		delete(faces.Seen, rel)
		changed = true
	}
	if changed {
		saveFacesLocked()
	}
}

// pruneMissingFaces drops faces whose photo is no longer on disk. A backstop
// for anything that bypasses the app — a file removed over SMB, say.
func pruneMissingFaces() int {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	gone := map[string]bool{}
	for _, fc := range faces.Faces {
		if _, checked := gone[fc.Path]; checked {
			continue
		}
		full, err := safePath(baseDir, fc.Path)
		missing := err != nil
		if !missing {
			if fi, serr := os.Lstat(full); serr != nil || !fi.Mode().IsRegular() {
				missing = true
			}
		}
		gone[fc.Path] = missing
	}
	n := 0
	for id, fc := range faces.Faces {
		if gone[fc.Path] {
			delete(faces.Faces, id)
			delete(faces.Seen, fc.Path)
			n++
		}
	}
	if n > 0 {
		saveFacesLocked()
	}
	return n
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

// nameConflict reports that another person already holds the name being
// assigned. Naming two groups "Alex" is how someone naturally expresses "these
// are the same person", but acting on that silently is wrong: families reuse
// names, and an accidental merge is tedious to unpick. So the caller is told,
// and asks.
type nameConflict struct {
	ID    string
	Name  string
	Count int
}

func (e *nameConflict) Error() string { return "a person called " + e.Name + " already exists" }

// findNamedLocked returns another person with this name, if any. Comparison is
// case- and space-insensitive: "alex" and "Alex " are the same intent.
// The caller must hold faceMu.
func findNamedLocked(name, excludeID string) *Person {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range faces.Persons {
		if p.ID == excludeID || !p.Named {
			continue
		}
		if strings.ToLower(strings.TrimSpace(p.Name)) == want {
			return p
		}
	}
	return nil
}

// namePerson turns an automatic cluster into a confirmed person. Naming is what
// protects a group from being rebuilt by the next clustering pass.
//
// If another person already holds the name and merge is false, nothing is
// changed and a *nameConflict is returned so the caller can ask. With merge
// true the two groups are folded together, which is the usual answer when the
// same person has been split across ages.
func namePerson(id, name string, merge bool) error {
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
	if other := findNamedLocked(name, id); other != nil {
		if !merge {
			n := 0
			for _, f := range faces.Faces {
				if f.Person == other.ID {
					n++
				}
			}
			return &nameConflict{ID: other.ID, Name: other.Name, Count: n}
		}
		// Fold this group into the existing person, so one name means one
		// person and later photos match against every age of them.
		for _, f := range faces.Faces {
			if f.Person == id {
				f.Person = other.ID
			}
		}
		delete(faces.Persons, id)
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
// mergePeople folds several groups into one and returns the surviving id.
//
// The id is returned because merging promotes the survivor out of the
// automatic namespace, so the caller's "c:4" no longer exists afterwards —
// without this, naming the result means guessing which group it became.
func mergePeople(ids []string) (string, error) {
	if len(ids) < 2 {
		return "", fmt.Errorf("need at least two groups")
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()

	target := faces.Persons[ids[0]]
	if target == nil {
		return "", fmt.Errorf("no such group")
	}
	keep := map[string]bool{}
	for _, id := range ids[1:] {
		if faces.Persons[id] == nil {
			return "", fmt.Errorf("no such group: %s", id)
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
	final := promoteLocked(target.ID) // a merge is a decision; give it a stable id
	saveFacesLocked()
	return final, nil
}

// assignFaces moves faces to a person, by id or by name.
//
// detachFaces only ever says "not them", which leaves the face unassigned and
// waiting to be regrouped. Auditing needs the positive form too — "this one IS
// Alex" — otherwise a face in the wrong group can only be pushed out, never
// put right, and the correction is lost at the next pass.
//
// An empty personID with a name creates the person; naming an existing one
// targets them, so assigning to "Alex" always means the Alex you already have.
func assignFaces(faceIDs []string, personID, name string) (string, error) {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()

	name = strings.TrimSpace(name)
	target := faces.Persons[personID]
	if target == nil && name != "" {
		if existing := findNamedLocked(name, ""); existing != nil {
			target = existing
		} else {
			faces.NextID++
			id := fmt.Sprintf("p:%d", faces.NextID)
			target = &Person{ID: id, Name: name, Named: true}
			faces.Persons[id] = target
		}
	}
	if target == nil {
		return "", fmt.Errorf("no such person")
	}
	// Assigning by hand is a decision, so the group must survive re-clustering
	// — otherwise the correction silently evaporates on the next scan.
	target.Named = true
	if target.Name == "" && name != "" {
		target.Name = name
	}
	moved := 0
	for _, id := range faceIDs {
		if f := faces.Faces[id]; f != nil {
			f.Person = target.ID
			moved++
		}
	}
	if moved == 0 {
		return "", fmt.Errorf("no such face")
	}
	saveFacesLocked()
	return target.ID, nil
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
		// configured: an ML sidecar is set up at all.
		// enabled: that sidecar actually has the face model loaded. The two
		// differ when the sidecar is up for CLIP but faces are unavailable,
		// which needs a different message than "not configured".
		"configured": faceMLURL() != "",
		"enabled":    sidecarHasFaces(),
		"running":    running,
		"done":       done,
		"total":      total,
		"errors":     errs,
		"faces":      nFaces,
		"scanned":    nSeen,
		"people":     len(listPeople()),
		"minGroup":   faceMinClusterSize,
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
	sizeKey := clampAtoi(r.URL.Query().Get("size"), 160, 96, 400)
	cache := cachePathFor(full, fmt.Sprintf("_face%s_%d",
		strings.TrimPrefix(f.ID[strings.LastIndex(f.ID, "#"):], "#"), sizeKey))
	if _, err := os.Stat(cache); err == nil {
		http.ServeFile(w, r, cache)
		return
	}
	// imaging has no HEIC decoder, so on an iPhone library every face crop
	// failed here and the group rendered as a grid of empty tiles — while the
	// same photos showed fine in the grid, which converts first.
	//
	// Reuse the viewer's _display derivative rather than converting to a temp
	// file: opening a person and opening one of their photos then share the
	// one conversion, and it is already invalidated with the rest of the
	// file's cache when the photo moves or changes.
	src := full
	if isHeic(filepath.Base(full)) {
		disp := cachePathFor(full, variantDisplay)
		if _, serr := os.Stat(disp); serr != nil {
			if cerr := heicToJPEG(full, disp); cerr != nil {
				log.Printf("face crop: heic convert failed for %s: %v", full, cerr)
				http.Error(w, "cannot convert HEIC", http.StatusInternalServerError)
				return
			}
		}
		src = disp
	}
	img, err := imaging.Open(src, imaging.AutoOrientation(true))
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
	// Reviewing a face means deciding whether it really is that person, which
	// 160px does not support. Callers can ask for larger; capped so this can't
	// be used to make the server render arbitrarily big images.
	size := clampAtoi(r.URL.Query().Get("size"), 160, 96, 400)
	crop := imaging.Thumbnail(imaging.Crop(img, rect), size, size, imaging.Lanczos)
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
	var body struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Merge bool   `json:"merge"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	err := namePerson(body.ID, body.Name, body.Merge)
	if c, ok := err.(*nameConflict); ok {
		// 409: nothing was changed. The client asks, then retries with merge.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{
			"conflict": map[string]any{"id": c.ID, "name": c.Name, "count": c.Count},
		})
		return
	}
	if err != nil {
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
	id, err := mergePeople(body.IDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The surviving id, so the caller can name what it just created.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"id": id})
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

// POST /api/people/assign  {"faceIds":[...], "personId":"p:2"}  or  {"faceIds":[...], "name":"Alex"}
func peopleAssignHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		FaceIDs  []string `json:"faceIds"`
		PersonID string   `json:"personId"`
		Name     string   `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, err := assignFaces(body.FaceIDs, body.PersonID, body.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"personId": id})
}

// POST /api/faces/scan — re-run detection over anything new, then regroup.
func facesScanHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ml := faceMLURL()
	if ml == "" {
		http.Error(w, "no ML sidecar configured (set ML_URL)", http.StatusNotImplemented)
		return
	}
	if !sidecarHasFaces() {
		http.Error(w, "the ML sidecar is running but has no face model", http.StatusNotImplemented)
		return
	}
	go faceIndexer(ml)
	w.WriteHeader(http.StatusAccepted)
}

func faceMLURL() string { return strings.TrimSpace(os.Getenv("ML_URL")) }

// sidecarHasFaces asks the sidecar whether the face model actually loaded.
//
// ML_URL being set only means a sidecar was configured. It can be running and
// serving CLIP while the face model is missing — if its download failed at
// build time, say. Without this the People view would sit empty with no
// explanation, which is the most confusing possible failure.
//
// Cached briefly: this is on the status path, which the UI polls.
var faceHealth struct {
	mu      sync.Mutex
	ok      bool
	checked time.Time
}

func sidecarHasFaces() bool {
	ml := faceMLURL()
	if ml == "" {
		return false
	}
	faceHealth.mu.Lock()
	defer faceHealth.mu.Unlock()
	if time.Since(faceHealth.checked) < 30*time.Second {
		return faceHealth.ok
	}
	faceHealth.checked = time.Now()
	faceHealth.ok = false

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(strings.TrimRight(ml, "/") + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		Faces bool `json:"faces"`
	}
	if json.NewDecoder(resp.Body).Decode(&h) != nil {
		return false
	}
	faceHealth.ok = h.Faces
	return h.Faces
}

// ── Merge suggestions ────────────────────────────────────────────────────────
//
// The clusterer splits one person across ages on purpose: ArcFace similarity
// falls a long way over a childhood, and a threshold loose enough to bridge
// that merges strangers instead. The cost is that a big library grows a tail
// of groups that are the same person, and finding them means scrolling.
//
// Suggestions close that gap without loosening anything: pairs that are close
// but not close enough to have been merged automatically are put in front of
// the user, who decides. Nothing here changes the grouping on its own.

type mergeSuggestion struct {
	A      string  `json:"a"`
	B      string  `json:"b"`
	AName  string  `json:"aName"`
	BName  string  `json:"bName"`
	ACount int     `json:"aCount"`
	BCount int     `json:"bCount"`
	ACover string  `json:"aCover"`
	BCover string  `json:"bCover"`
	AYears string  `json:"aYears"`
	BYears string  `json:"bYears"`
	Score  float32 `json:"score"`
}

// pairKey orders a pair so that (a,b) and (b,a) are the same entry.
func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// yearSpan describes when a group's photos were taken, which is usually what
// settles a suggestion: two faces can look alike, but "2011-2013" next to
// "2019-2024" tells you whether it can be the same child.
func yearSpan(members []*Face) string {
	var lo, hi int64
	for _, m := range members {
		if m.Taken == 0 {
			continue
		}
		if lo == 0 || m.Taken < lo {
			lo = m.Taken
		}
		if m.Taken > hi {
			hi = m.Taken
		}
	}
	if lo == 0 {
		return ""
	}
	a, b := time.Unix(lo, 0).Year(), time.Unix(hi, 0).Year()
	if a == b {
		return fmt.Sprintf("%d", a)
	}
	return fmt.Sprintf("%d–%d", a, b)
}

// centroid averages a group's embeddings and renormalises.
//
// Used only to shortlist candidate pairs cheaply. Scoring on a centroid alone
// would repeat the mistake clusterFaces warns about: averaging a person across
// several ages produces a vector that matches none of them.
func centroid(members []*Face) []float32 {
	if len(members) == 0 {
		return nil
	}
	dim := len(members[0].Emb)
	if dim == 0 {
		return nil
	}
	sum := make([]float32, dim)
	for _, m := range members {
		if len(m.Emb) != dim {
			continue
		}
		for i, v := range m.Emb {
			sum[i] += v
		}
	}
	var norm float32
	for _, v := range sum {
		norm += v * v
	}
	if norm == 0 {
		return nil
	}
	norm = float32(math.Sqrt(float64(norm)))
	for i := range sum {
		sum[i] /= norm
	}
	return sum
}

// bestPairSimilarity is the closest any face in one group comes to any face in
// the other — the same max-over-members rule the clusterer uses, rather than a
// centroid, so a suggestion means "these two faces really do look alike" and
// not "these two averages do".
//
// Both sides are sampled by detector confidence: the clearest faces are the
// ones worth comparing, and it keeps a pair of 3,000-face groups from costing
// nine million comparisons.
func bestPairSimilarity(a, b []*Face) float32 {
	sa, sb := sampleFaces(a), sampleFaces(b)
	var best float32
	for _, x := range sa {
		for _, y := range sb {
			if s := cosine(x.Emb, y.Emb); s > best {
				best = s
			}
		}
	}
	return best
}

// sampleFaces takes the highest-confidence faces from a group.
func sampleFaces(m []*Face) []*Face {
	if len(m) <= faceSuggestSample {
		return m
	}
	cp := append([]*Face(nil), m...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Score > cp[j].Score })
	return cp[:faceSuggestSample]
}

// suggestMerges returns groups that look like the same person, best first.
func suggestMerges(limit int) []mergeSuggestion {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()

	members := map[string][]*Face{}
	for _, f := range faces.Faces {
		if f.Person == "" {
			continue
		}
		members[f.Person] = append(members[f.Person], f)
	}

	// Resolve dismissals to whatever groups hold those faces now. A pass of
	// re-clustering may have moved them, in which case the decision follows
	// the faces rather than being lost with the old group ids.
	dismissed := map[string]bool{}
	for key := range faces.Dismissed {
		fa, fb, ok := strings.Cut(key, "|")
		if !ok {
			continue
		}
		x, y := faces.Faces[fa], faces.Faces[fb]
		if x == nil || y == nil || x.Person == "" || y.Person == "" {
			continue
		}
		dismissed[pairKey(x.Person, y.Person)] = true
	}

	ids := make([]string, 0, len(members))
	cents := map[string][]float32{}
	for id, m := range members {
		if faces.Persons[id] == nil {
			continue
		}
		if c := centroid(m); c != nil {
			ids = append(ids, id)
			cents[id] = c
		}
	}
	sort.Strings(ids) // deterministic output for the same store

	// Pass 1: centroids, to shortlist without comparing every face to every
	// other face across the whole library.
	type cand struct {
		a, b string
		s    float32
	}
	var shortlist []cand
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := ids[i], ids[j]
			pa, pb := faces.Persons[a], faces.Persons[b]
			// Both named means the user has already decided twice. Suggesting
			// they are one person contradicts both decisions; the same-name
			// merge prompt already covers the case where they agree.
			if pa.Named && pb.Named {
				continue
			}
			if dismissed[pairKey(a, b)] {
				continue
			}
			shortlist = append(shortlist, cand{a, b, cosine(cents[a], cents[b])})
		}
	}
	sort.Slice(shortlist, func(i, j int) bool { return shortlist[i].s > shortlist[j].s })
	if len(shortlist) > faceSuggestShortlist {
		shortlist = shortlist[:faceSuggestShortlist]
	}

	// Pass 2: score the shortlist properly.
	out := make([]mergeSuggestion, 0, limit)
	for _, c := range shortlist {
		score := bestPairSimilarity(members[c.a], members[c.b])
		if score < faceSuggestThreshold {
			continue
		}
		ma, mb := members[c.a], members[c.b]
		sort.Slice(ma, func(i, j int) bool { return ma[i].Score > ma[j].Score })
		sort.Slice(mb, func(i, j int) bool { return mb[i].Score > mb[j].Score })
		out = append(out, mergeSuggestion{
			A: c.a, B: c.b,
			AName: faces.Persons[c.a].Name, BName: faces.Persons[c.b].Name,
			ACount: len(ma), BCount: len(mb),
			ACover: ma[0].ID, BCover: mb[0].ID,
			AYears: yearSpan(ma), BYears: yearSpan(mb),
			Score: score,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })

	// One group can look like several others; showing the same face in five
	// rows makes the list feel longer than the work actually is.
	seen := map[string]bool{}
	kept := out[:0]
	for _, s := range out {
		if seen[s.A] || seen[s.B] {
			continue
		}
		seen[s.A], seen[s.B] = true, true
		kept = append(kept, s)
		if len(kept) >= limit {
			break
		}
	}
	return kept
}

// dismissSuggestion records that two groups are not the same person.
//
// Stored against each group's clearest face, so the decision survives the
// renumbering that every clustering pass does to unnamed groups.
func dismissSuggestion(a, b string) error {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	if faces.Persons[a] == nil || faces.Persons[b] == nil {
		return fmt.Errorf("no such group")
	}
	rep := func(id string) *Face {
		var best *Face
		for _, f := range faces.Faces {
			if f.Person == id && (best == nil || f.Score > best.Score) {
				best = f
			}
		}
		return best
	}
	fa, fb := rep(a), rep(b)
	if fa == nil || fb == nil {
		return fmt.Errorf("group has no faces")
	}
	faces.Dismissed[pairKey(fa.ID, fb.ID)] = true
	pruneDismissedLocked()
	saveFacesLocked()
	return nil
}

// pruneDismissedLocked drops entries whose faces have left the library, so the
// list can't grow without bound as photos are deleted.
func pruneDismissedLocked() {
	for key := range faces.Dismissed {
		fa, fb, ok := strings.Cut(key, "|")
		if !ok || faces.Faces[fa] == nil || faces.Faces[fb] == nil {
			delete(faces.Dismissed, key)
		}
	}
}

// GET /api/people/suggestions — pairs that look like the same person.
func peopleSuggestionsHandler(w http.ResponseWriter, r *http.Request) {
	// While a scan is running the groups are still moving, so anything
	// computed here would be about to change.
	faceIndex.mu.Lock()
	running := faceIndex.running
	faceIndex.mu.Unlock()

	out := []mergeSuggestion{}
	if !running {
		out = suggestMerges(clampAtoi(r.URL.Query().Get("limit"), 12, 1, 50))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"suggestions": out, "scanning": running})
}

// POST /api/people/suggestions/dismiss  {"a":"c:3","b":"p:1"}
func peopleDismissSuggestionHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct{ A, B string }
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := dismissSuggestion(body.A, body.B); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// photosOfNamedPeople returns the library-relative paths of photos containing
// someone whose name matches q, best-known first.
//
// Only named people: an automatic group has no name to search for, and
// matching on "c:4" would be noise.
//
// Paths come back deduplicated — one photo can hold several faces of the same
// person, and more than one matching person can appear in the same photo.
func photosOfNamedPeople(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()

	match := map[string]bool{}
	for id, p := range faces.Persons {
		if p.Named && p.Name != "" && strings.Contains(strings.ToLower(p.Name), q) {
			match[id] = true
		}
	}
	if len(match) == 0 {
		return nil
	}

	// Newest first, which is the order the rest of the app shows photos in.
	var hits []*Face
	for _, f := range faces.Faces {
		if match[f.Person] {
			hits = append(hits, f)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Taken != hits[j].Taken {
			return hits[i].Taken > hits[j].Taken
		}
		return hits[i].ID < hits[j].ID
	})

	seen := map[string]bool{}
	out := make([]string, 0, len(hits))
	for _, f := range hits {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		out = append(out, f.Path)
	}
	return out
}
