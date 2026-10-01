package main

import (
	"math"
	"math/rand"
	"testing"
)

// identity builds a 512-dim unit embedding for a synthetic person.
//
// stream must differ between two groups of the SAME person, or their faces
// come out byte-identical and every pair scores 1.000 — which tests nothing,
// because real groups that similar would have been merged automatically. The
// jitter is what puts a pair in the 0.45-0.60 band that suggestions are for;
// TestSyntheticSimilarityLandsInTheSuggestionBand pins the actual numbers.
func identity(seed int64, jitter float64, n int, stream int64) []float32 {
	base := rand.New(rand.NewSource(seed))
	raw := make([]float64, 512)
	for i := range raw {
		raw[i] = base.NormFloat64()
	}
	j := rand.New(rand.NewSource(seed*31 + stream*7919 + int64(n)))
	out := make([]float32, 512)
	var norm float64
	for i, v := range raw {
		x := v + j.NormFloat64()*jitter
		out[i] = float32(x)
		norm += x * x
	}
	norm = math.Sqrt(norm)
	for i := range out {
		out[i] /= float32(norm)
	}
	return out
}

// withFaceStore installs a store for one test.
func withFaceStore(t *testing.T, st *faceStore) {
	t.Helper()
	// Point dataDir at a temp directory: anything that calls saveFacesLocked
	// writes faces.gob there, and without this the tests drop one in the repo
	// root and clobber whatever the developer's own store holds.
	prevData := dataDir
	dataDir = t.TempDir()
	faceMu.Lock()
	prev := faces
	faces = st
	if faces.Dismissed == nil {
		faces.Dismissed = map[string]bool{}
	}
	faceMu.Unlock()
	t.Cleanup(func() {
		faceMu.Lock()
		faces = prev
		dataDir = prevData
		faceMu.Unlock()
	})
}

// addGroup puts n faces of one synthetic identity into a group.
func addGroup(st *faceStore, id string, named bool, seed int64, n int, jitter float64) {
	// Each group gets its own jitter stream, so two groups of one identity are
	// similar rather than identical.
	var stream int64
	for i := 0; i < len(id); i++ {
		stream = stream*131 + int64(id[i])
	}
	st.Persons[id] = &Person{ID: id, Named: named}
	if named {
		st.Persons[id].Name = id
	}
	for i := 0; i < n; i++ {
		fid := id + "/img" + string(rune('a'+i)) + ".jpg#0"
		st.Faces[fid] = &Face{
			ID: fid, Path: id + "/img.jpg", Person: id,
			Emb: identity(seed, jitter, i, stream), Score: 0.9, Taken: 1500000000,
		}
	}
}

// Jitter levels, measured by TestSyntheticSimilarityLandsInTheSuggestionBand:
//
//	sameAge  -> two groups of one identity at 0.54: inside the suggestion
//	            band and below the 0.60 that would have merged them already,
//	            which is precisely the case this feature exists for
//	tooFar   -> 0.40, below the suggestion floor
const (
	sameAge = 1.0
	tooFar  = 1.4
)

// The tests below are only meaningful if the synthetic identities actually sit
// where the thresholds are. An earlier version of this fixture produced
// byte-identical faces scoring 1.000, which tested nothing: real groups that
// alike are merged automatically and never become a suggestion.
func TestSyntheticSimilarityLandsInTheSuggestionBand(t *testing.T) {
	grp := func(st *faceStore, id string) []*Face {
		var out []*Face
		for _, f := range st.Faces {
			if f.Person == id {
				out = append(out, f)
			}
		}
		return out
	}
	same := newStore()
	addGroup(same, "c:1", false, 11, 5, sameAge)
	addGroup(same, "c:2", false, 11, 5, sameAge)
	got := bestPairSimilarity(grp(same, "c:1"), grp(same, "c:2"))
	if got < faceSuggestThreshold || got >= faceSameThreshold {
		t.Errorf("same identity scores %.3f, want it inside [%.2f, %.2f) — the fixture no longer exercises the suggestion band",
			got, float32(faceSuggestThreshold), float32(faceSameThreshold))
	}

	diff := newStore()
	addGroup(diff, "c:1", false, 11, 5, sameAge)
	addGroup(diff, "c:3", false, 99, 5, sameAge)
	if got := bestPairSimilarity(grp(diff, "c:1"), grp(diff, "c:3")); got >= faceSuggestThreshold {
		t.Errorf("two different identities score %.3f, at or above the suggestion floor %.2f", got, float32(faceSuggestThreshold))
	}
}

// A pair below the floor must stay out of the list entirely.
func TestNoSuggestionBelowTheFloor(t *testing.T) {
	st := newStore()
	addGroup(st, "c:1", false, 11, 5, tooFar)
	addGroup(st, "c:2", false, 11, 5, tooFar)
	withFaceStore(t, st)
	if got := suggestMerges(10); len(got) != 0 {
		t.Errorf("suggested a pair that scores below %.2f: %+v", float32(faceSuggestThreshold), got)
	}
}

func newStore() *faceStore {
	return &faceStore{
		Faces:     map[string]*Face{},
		Persons:   map[string]*Person{},
		Seen:      map[string]faceSeen{},
		Dismissed: map[string]bool{},
	}
}

// The feature only earns its place if it finds the split-across-ages case and
// stays quiet about strangers. Both halves matter: a suggestion list full of
// unrelated people is worse than no list, because it trains you to ignore it.
func TestSuggestsSamePersonAndNotStrangers(t *testing.T) {
	st := newStore()
	// Two groups of the same identity, far enough apart that the clusterer
	// would have left them separate.
	addGroup(st, "c:1", false, 11, 5, sameAge)
	addGroup(st, "c:2", false, 11, 5, sameAge)
	// An unrelated person.
	addGroup(st, "c:3", false, 99, 5, sameAge)
	withFaceStore(t, st)

	got := suggestMerges(10)
	if len(got) == 0 {
		t.Fatal("no suggestions — the two halves of one person were never offered")
	}
	top := got[0]
	if !((top.A == "c:1" && top.B == "c:2") || (top.A == "c:2" && top.B == "c:1")) {
		t.Errorf("top suggestion is %s+%s, want the two halves of one identity", top.A, top.B)
	}
	for _, s := range got {
		if s.A == "c:3" || s.B == "c:3" {
			t.Errorf("suggested merging a stranger: %s + %s at %.3f", s.A, s.B, s.Score)
		}
	}
}

// A dismissal has to survive the renumbering that every clustering pass does
// to unnamed groups, or it comes back the next time a scan runs — which is
// exactly when the user is least inclined to forgive it.
func TestDismissalSurvivesRegrouping(t *testing.T) {
	st := newStore()
	addGroup(st, "c:1", false, 11, 5, sameAge)
	addGroup(st, "c:2", false, 11, 5, sameAge)
	withFaceStore(t, st)

	if len(suggestMerges(10)) == 0 {
		t.Fatal("nothing suggested to dismiss")
	}
	if err := dismissSuggestion("c:1", "c:2"); err != nil {
		t.Fatal(err)
	}
	if got := suggestMerges(10); len(got) != 0 {
		t.Fatalf("dismissed pair still suggested: %+v", got)
	}

	// Now rename the groups the way a clustering pass would: same faces,
	// different group ids.
	faceMu.Lock()
	for _, f := range faces.Faces {
		switch f.Person {
		case "c:1":
			f.Person = "c:7"
		case "c:2":
			f.Person = "c:4"
		}
	}
	delete(faces.Persons, "c:1")
	delete(faces.Persons, "c:2")
	faces.Persons["c:7"] = &Person{ID: "c:7"}
	faces.Persons["c:4"] = &Person{ID: "c:4"}
	faceMu.Unlock()

	if got := suggestMerges(10); len(got) != 0 {
		t.Errorf("dismissal was lost when the groups were renumbered: %+v", got)
	}
}

// Two groups the user has named are two decisions already made. Suggesting
// they are one person contradicts both.
func TestNoSuggestionBetweenTwoNamedGroups(t *testing.T) {
	st := newStore()
	addGroup(st, "p:1", true, 11, 5, sameAge)
	addGroup(st, "p:2", true, 11, 5, sameAge)
	withFaceStore(t, st)

	if got := suggestMerges(10); len(got) != 0 {
		t.Errorf("suggested merging two named people: %+v", got)
	}

	// But a named person and a loose cluster is the case worth surfacing.
	st2 := newStore()
	addGroup(st2, "p:1", true, 11, 5, sameAge)
	addGroup(st2, "c:1", false, 11, 5, sameAge)
	withFaceStore(t, st2)
	if got := suggestMerges(10); len(got) == 0 {
		t.Error("did not offer to attach a loose cluster to a named person")
	}
}

// A group that resembles several others must not fill the whole list, or one
// ambiguous face makes the feature look like busywork.
func TestEachGroupAppearsOnce(t *testing.T) {
	st := newStore()
	for i, id := range []string{"c:1", "c:2", "c:3", "c:4"} {
		addGroup(st, id, false, 11, 5, sameAge)
		_ = i
	}
	withFaceStore(t, st)

	got := suggestMerges(10)
	seen := map[string]int{}
	for _, s := range got {
		seen[s.A]++
		seen[s.B]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("%s appears in %d suggestions, want at most 1", id, n)
		}
	}
}

// Suggestions carry what the user needs to judge: how big each side is, a face
// to look at, and when the photos were taken. The date span is often what
// actually settles it.
func TestSuggestionsCarryEnoughToDecide(t *testing.T) {
	st := newStore()
	addGroup(st, "c:1", false, 11, 5, sameAge)
	addGroup(st, "c:2", false, 11, 4, sameAge)
	withFaceStore(t, st)

	got := suggestMerges(10)
	if len(got) == 0 {
		t.Fatal("no suggestions")
	}
	s := got[0]
	if s.ACount == 0 || s.BCount == 0 {
		t.Errorf("counts missing: %+v", s)
	}
	if s.ACover == "" || s.BCover == "" {
		t.Errorf("no face to look at: %+v", s)
	}
	if s.AYears == "" || s.BYears == "" {
		t.Errorf("no date span: %+v", s)
	}
	if faces.Faces[s.ACover] == nil || faces.Faces[s.BCover] == nil {
		t.Error("cover ids do not resolve to real faces")
	}
}

// Degenerate stores must not panic: this runs behind an HTTP handler that any
// logged-in user can hit.
func TestSuggestMergesHandlesEmptyAndTiny(t *testing.T) {
	withFaceStore(t, newStore())
	if got := suggestMerges(10); len(got) != 0 {
		t.Errorf("suggestions from an empty store: %+v", got)
	}

	st := newStore()
	addGroup(st, "c:1", false, 11, 1, 0.0)
	// A face with no embedding at all, as a truncated or failed detect leaves.
	st.Faces["broken#0"] = &Face{ID: "broken#0", Person: "c:1", Emb: nil}
	withFaceStore(t, st)
	_ = suggestMerges(10) // must not panic
}
