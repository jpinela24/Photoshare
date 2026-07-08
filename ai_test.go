package main

// End-to-end test of the AI pipeline (indexer → vector store → cosine search)
// against a STUB ML sidecar — no real CLIP needed. The stub embeds an image as
// its average RGB and a text query as a color, both in the same 3-D space, so a
// red photo must rank first for the query "red". This proves the plumbing
// (multipart upload, gob store, mtime skip, cosine ranking, handlers) works;
// only the CLIP quality itself is untested here (that runs on the homelab).

import (
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// stubSidecar returns a fake CLIP: image → avg RGB, text → named color.
func stubSidecar(t *testing.T) *httptest.Server {
	colors := map[string][]float32{
		"red":   {1, 0, 0},
		"green": {0, 1, 0},
		"blue":  {0, 0, 1},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/clip/image", func(w http.ResponseWriter, r *http.Request) {
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
		var rs, gs, bs, n float64
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				cr, cg, cb, _ := img.At(x, y).RGBA()
				rs += float64(cr >> 8)
				gs += float64(cg >> 8)
				bs += float64(cb >> 8)
				n++
			}
		}
		emb := []float32{float32(rs / n), float32(gs / n), float32(bs / n)}
		json.NewEncoder(w).Encode(map[string]any{"embedding": emb})
	})
	mux.HandleFunc("/clip/text", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Text string }
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &in)
		emb, ok := colors[in.Text]
		if !ok {
			emb = []float32{1, 1, 1}
		}
		json.NewEncoder(w).Encode(map[string]any{"embedding": emb})
	})
	return httptest.NewServer(mux)
}

func writeSolidJPEG(t *testing.T, path string, c color.Color) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestAIPipeline(t *testing.T) {
	srv := stubSidecar(t)
	defer srv.Close()

	lib := t.TempDir()
	data := t.TempDir()
	writeSolidJPEG(t, filepath.Join(lib, "red.jpg"), color.RGBA{255, 0, 0, 255})
	writeSolidJPEG(t, filepath.Join(lib, "green.jpg"), color.RGBA{0, 255, 0, 255})
	writeSolidJPEG(t, filepath.Join(lib, "blue.jpg"), color.RGBA{0, 0, 255, 255})

	// Wire the globals the AI code reads.
	aiURL = srv.URL
	baseDir = lib
	dataDir = data
	aiStore = newVecStore(filepath.Join(data, "ai-index.gob"))

	if !mlHealthy() {
		t.Fatal("stub sidecar not healthy")
	}

	// Index synchronously (one pass) and confirm all three were embedded.
	aiIndexOnce()
	if got := aiStore.len(); got != 3 {
		t.Fatalf("indexed %d images, want 3", got)
	}

	// Re-index: nothing changed, so the store stays at 3 (mtime-skip works).
	aiIndexOnce()
	if got := aiStore.len(); got != 3 {
		t.Fatalf("after re-index have %d, want 3", got)
	}

	// The gob file should have been persisted and reload cleanly.
	if _, err := os.Stat(filepath.Join(data, "ai-index.gob")); err != nil {
		t.Fatalf("index not persisted: %v", err)
	}
	if reloaded := newVecStore(filepath.Join(data, "ai-index.gob")); reloaded.len() != 3 {
		t.Fatalf("reloaded store has %d, want 3", reloaded.len())
	}

	// Search must rank the matching color first.
	for _, tc := range []struct{ q, want string }{
		{"red", "red.jpg"},
		{"green", "green.jpg"},
		{"blue", "blue.jpg"},
	} {
		res, err := aiSearch(tc.q, 3)
		if err != nil {
			t.Fatalf("search %q: %v", tc.q, err)
		}
		if len(res) == 0 || res[0].Name != tc.want {
			t.Fatalf("search %q top = %v, want %s", tc.q, res, tc.want)
		}
	}

	// Prune: delete a file, re-index, and confirm it drops out of the store.
	os.Remove(filepath.Join(lib, "blue.jpg"))
	aiIndexOnce()
	if got := aiStore.len(); got != 2 {
		t.Fatalf("after delete have %d, want 2", got)
	}
}
