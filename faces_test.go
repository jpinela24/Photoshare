package main

// End-to-end test of the face pipeline (detect → store → cluster → name)
// against a STUB sidecar — no insightface needed. The stub returns a face whose
// embedding is a fixed unit vector chosen by the image's dominant color, so
// photos of the "same person" (same color) must land in one cluster and naming
// must stick across a re-cluster. This proves the plumbing; real ArcFace quality
// is validated on the homelab.

import (
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func stubFaceSidecar(t *testing.T) *httptest.Server {
	// Each "person" is a unit vector in a distinct axis; the stub picks one by
	// the image's average red/green/blue dominance.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/faces/detect", func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		defer f.Close()
		img, err := jpeg.Decode(f)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b := img.Bounds()
		cr, cg, cb, _ := img.At(b.Min.X, b.Min.Y).RGBA()
		var emb []float32
		switch {
		case cr >= cg && cr >= cb:
			emb = []float32{1, 0, 0}
		case cg >= cr && cg >= cb:
			emb = []float32{0, 1, 0}
		default:
			emb = []float32{0, 0, 1}
		}
		resp := map[string]any{
			"w": float32(b.Dx()), "h": float32(b.Dy()),
			"faces": []map[string]any{{
				"box":       []float32{0, 0, float32(b.Dx()) / 2, float32(b.Dy()) / 2},
				"score":     0.9,
				"embedding": emb,
			}},
		}
		json.NewEncoder(w).Encode(resp)
	})
	return httptest.NewServer(mux)
}

func TestFacePipeline(t *testing.T) {
	srv := stubFaceSidecar(t)
	defer srv.Close()

	lib := t.TempDir()
	data := t.TempDir()
	// Two photos of "red person", one of "green person".
	write := func(name string, c color.Color) {
		img := image.NewRGBA(image.Rect(0, 0, 8, 8))
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				img.Set(x, y, c)
			}
		}
		fp, _ := os.Create(filepath.Join(lib, name))
		defer fp.Close()
		jpeg.Encode(fp, img, &jpeg.Options{Quality: 100})
	}
	write("a.jpg", color.RGBA{255, 0, 0, 255})
	write("b.jpg", color.RGBA{255, 0, 0, 255})
	write("c.jpg", color.RGBA{0, 255, 0, 255})

	aiURL = srv.URL
	facesOn = true
	baseDir = lib
	dataDir = data
	faceStoreG = newFaceStore(filepath.Join(data, "faces-index.gob"))
	loadPersonNames(filepath.Join(data, "faces-names.gob"))

	if !mlHealthy() {
		t.Fatal("stub not healthy")
	}

	// Detect + store.
	faceIndexOnce()
	imgs, faces := faceStoreG.counts()
	if imgs != 3 || faces != 3 {
		t.Fatalf("indexed images=%d faces=%d, want 3/3", imgs, faces)
	}

	// mtime-skip: a second pass changes nothing.
	faceIndexOnce()
	if i2, f2 := faceStoreG.counts(); i2 != 3 || f2 != 3 {
		t.Fatalf("after re-index images=%d faces=%d, want 3/3", i2, f2)
	}

	// Cluster: 2 people (red x2, green x1).
	people := computePeople()
	if len(people) != 2 {
		t.Fatalf("clustered into %d people, want 2", len(people))
	}
	// Largest first ⇒ the red cluster (2 members).
	if len(people[0].Members) != 2 {
		t.Fatalf("top cluster has %d members, want 2", len(people[0].Members))
	}

	// Name the red person; the name must survive a re-cluster and attach to the
	// same 2-member group.
	redKey := people[0].Key
	personNames.mu.Lock()
	personNames.m[redKey] = "Alice"
	personNames.mu.Unlock()
	clusterCache.people = nil // invalidate as the handler would
	people = computePeople()
	var named *personCluster
	for i := range people {
		if people[i].Name == "Alice" {
			named = &people[i]
		}
	}
	if named == nil {
		t.Fatal("name did not attach to any cluster after re-cluster")
	}
	if len(named.Members) != 2 {
		t.Fatalf("named cluster has %d members, want 2", len(named.Members))
	}

	// Photos-of-person: the red person appears in a.jpg and b.jpg (deduped).
	seen := map[string]bool{}
	for _, m := range named.Members {
		seen[m.Rel] = true
	}
	if !seen["a.jpg"] || !seen["b.jpg"] || len(seen) != 2 {
		t.Fatalf("red person photos = %v, want {a.jpg,b.jpg}", seen)
	}

	// Prune on delete.
	os.Remove(filepath.Join(lib, "c.jpg"))
	faceIndexOnce()
	if i3, _ := faceStoreG.counts(); i3 != 2 {
		t.Fatalf("after delete images=%d, want 2", i3)
	}
}
