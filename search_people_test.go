package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// searchFor runs the handler and returns the paths it found, in order.
func searchFor(t *testing.T, query string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	searchHandler(rec, httptest.NewRequest(http.MethodGet, "/api/search?q="+url.QueryEscape(query), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("search returned %d", rec.Code)
	}
	var got []SearchEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding results: %v (%s)", err, rec.Body.String())
	}
	out := make([]string, 0, len(got))
	for _, e := range got {
		out = append(out, e.Path)
	}
	return out
}

// seedLibrary writes real files and points baseDir at them.
func seedLibrary(t *testing.T, rels ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range rels {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	prev := baseDir
	baseDir = dir
	t.Cleanup(func() { baseDir = prev })
}

// Naming a face is the most deliberate thing you can tell this app about a
// photo, and until now searching that name found nothing: the search walked
// filenames only, and cameras do not put names in filenames.
func TestSearchFindsPhotosOfANamedPerson(t *testing.T) {
	seedLibrary(t, "2019/IMG_1234.jpg", "2020/DSC_0001.jpg", "2021/holiday.jpg")

	st := newStore()
	st.Persons["p:1"] = &Person{ID: "p:1", Name: "Ana", Named: true}
	for _, rel := range []string{"2019/IMG_1234.jpg", "2020/DSC_0001.jpg"} {
		st.Faces[rel+"#0"] = &Face{ID: rel + "#0", Path: rel, Person: "p:1", Taken: 1600000000}
	}
	withFaceStore(t, st)

	got := searchFor(t, "Ana")
	if len(got) != 2 {
		t.Fatalf("searching a person's name returned %v, want their 2 photos", got)
	}
	for _, want := range []string{"2019/IMG_1234.jpg", "2020/DSC_0001.jpg"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from the results", want)
		}
	}
}

// Matching is case-insensitive and partial, like the filename search beside it.
func TestSearchPersonIsCaseInsensitiveAndPartial(t *testing.T) {
	seedLibrary(t, "a/one.jpg")
	st := newStore()
	st.Persons["p:1"] = &Person{ID: "p:1", Name: "Ana Maria", Named: true}
	st.Faces["a/one.jpg#0"] = &Face{ID: "a/one.jpg#0", Path: "a/one.jpg", Person: "p:1"}
	withFaceStore(t, st)

	for _, q := range []string{"ana", "ANA", "maria", "na Mar"} {
		if got := searchFor(t, q); len(got) == 0 {
			t.Errorf("query %q found nothing", q)
		}
	}
}

// An unnamed group has no name to match. Searching must not surface it, and
// must certainly not match on the internal group id.
func TestSearchIgnoresUnnamedGroups(t *testing.T) {
	seedLibrary(t, "a/one.jpg")
	st := newStore()
	st.Persons["c:4"] = &Person{ID: "c:4"} // automatic, unnamed
	st.Faces["a/one.jpg#0"] = &Face{ID: "a/one.jpg#0", Path: "a/one.jpg", Person: "c:4"}
	withFaceStore(t, st)

	if got := searchFor(t, "c:4"); len(got) != 0 {
		t.Errorf("matched an internal group id: %v", got)
	}
}

// One photo holding several faces of the same person is a single result, not
// one per face.
func TestSearchDeduplicatesAPhotoWithSeveralFaces(t *testing.T) {
	seedLibrary(t, "a/family.jpg")
	st := newStore()
	st.Persons["p:1"] = &Person{ID: "p:1", Name: "Ana", Named: true}
	for _, i := range []string{"#0", "#1", "#2"} {
		st.Faces["a/family.jpg"+i] = &Face{ID: "a/family.jpg" + i, Path: "a/family.jpg", Person: "p:1"}
	}
	withFaceStore(t, st)

	if got := searchFor(t, "Ana"); len(got) != 1 {
		t.Errorf("got %v, want the photo listed once", got)
	}
}

// A photo whose filename also matches must not appear twice.
func TestSearchDoesNotDuplicateAcrossBothHalves(t *testing.T) {
	seedLibrary(t, "2019/ana-birthday.jpg")
	st := newStore()
	st.Persons["p:1"] = &Person{ID: "p:1", Name: "Ana", Named: true}
	st.Faces["2019/ana-birthday.jpg#0"] = &Face{
		ID: "2019/ana-birthday.jpg#0", Path: "2019/ana-birthday.jpg", Person: "p:1"}
	withFaceStore(t, st)

	if got := searchFor(t, "ana"); len(got) != 1 {
		t.Errorf("got %v, want one entry — it matches both the person and the filename", got)
	}
}

// Faces outlive the file when something is deleted outside the app, between
// scans. Those must not be served as results that 404 when clicked.
func TestSearchSkipsFacesWhoseFileIsGone(t *testing.T) {
	seedLibrary(t, "a/present.jpg")
	st := newStore()
	st.Persons["p:1"] = &Person{ID: "p:1", Name: "Ana", Named: true}
	st.Faces["a/present.jpg#0"] = &Face{ID: "a/present.jpg#0", Path: "a/present.jpg", Person: "p:1"}
	st.Faces["a/vanished.jpg#0"] = &Face{ID: "a/vanished.jpg#0", Path: "a/vanished.jpg", Person: "p:1"}
	withFaceStore(t, st)

	got := searchFor(t, "Ana")
	for _, g := range got {
		if g == "a/vanished.jpg" {
			t.Error("returned a photo that no longer exists on disk")
		}
	}
	if len(got) != 1 {
		t.Errorf("got %v, want only the file that still exists", got)
	}
}

// Filename search must keep working exactly as before.
func TestSearchStillMatchesFilenames(t *testing.T) {
	seedLibrary(t, "2019/sunset.jpg", "2019/other.jpg")
	withFaceStore(t, newStore())

	got := searchFor(t, "sunset")
	if len(got) != 1 || got[0] != "2019/sunset.jpg" {
		t.Errorf("got %v, want just 2019/sunset.jpg", got)
	}
}

// Smart (semantic) search is a separate endpoint backed by CLIP, which matches
// images by content and has no idea what a person is called. Without this, a
// name typed with Smart search on returned whatever the model free-associated
// to the word — which looks exactly like "search is broken" to someone who has
// just spent an evening naming people.
func TestSemanticSearchAlsoFindsNamedPeople(t *testing.T) {
	seedLibrary(t, "2019/IMG_1234.jpg")

	// Point AI at an address that is configured but dead, so aiSearch fails.
	// The person match must still be returned: it is a fact about the photo,
	// not a guess that depends on the model being up.
	prev := aiURL
	aiURL = "http://127.0.0.1:1"
	t.Cleanup(func() { aiURL = prev })

	st := newStore()
	st.Persons["p:1"] = &Person{ID: "p:1", Name: "Ana", Named: true}
	st.Faces["2019/IMG_1234.jpg#0"] = &Face{
		ID: "2019/IMG_1234.jpg#0", Path: "2019/IMG_1234.jpg", Person: "p:1"}
	withFaceStore(t, st)

	rec := httptest.NewRecorder()
	semanticSearchHandler(rec, httptest.NewRequest(http.MethodGet, "/api/search/semantic?q=Ana", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("semantic search returned %d: %s", rec.Code, rec.Body.String())
	}
	var got []SearchEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
	}
	if len(got) != 1 || got[0].Path != "2019/IMG_1234.jpg" {
		t.Errorf("got %+v, want the named person's photo", got)
	}
}
