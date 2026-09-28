package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// faceTestEnv isolates the face store for one test.
func faceTestEnv(t *testing.T) {
	t.Helper()
	prevData := dataDir
	dataDir = t.TempDir()
	faceMu.Lock()
	prevFaces := faces
	faces = nil
	faceMu.Unlock()
	t.Cleanup(func() {
		dataDir = prevData
		faceMu.Lock()
		faces = prevFaces
		faceMu.Unlock()
	})
}

// unit returns an L2-normalised vector, as the sidecar produces.
func unit(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(sum))
	if n == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

// personVec builds a face embedding for a synthetic identity: a fixed base
// direction plus a little noise, so faces of one identity sit close together
// and different identities sit far apart.
//
// Noise is per-component, so it grows with sqrt(dim) relative to the unit base
// — 0.08 in 64 dimensions lands intra-identity similarity around 0.7, right on
// top of the 0.60 threshold, which tests nothing but the coin flip. Keep it
// small enough that "same identity" is unambiguous; TestSyntheticIdentities
// asserts the separation these tests rely on.
func personVec(rng *rand.Rand, identity int, noise float64) []float32 {
	const dim = 64
	v := make([]float32, dim)
	base := rand.New(rand.NewSource(int64(identity) * 7919))
	for i := range v {
		v[i] = float32(base.NormFloat64())
	}
	v = unit(v)
	for i := range v {
		v[i] += float32(rng.NormFloat64() * noise)
	}
	return unit(v)
}

// addFace puts a face straight into the store, bypassing detection.
func addFace(id string, emb []float32, taken int64) {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	faces.Faces[id] = &Face{ID: id, Path: id, Emb: emb, Score: 0.9, Taken: taken}
}

func personOf(id string) string {
	faceMu.Lock()
	defer faceMu.Unlock()
	loadFacesLocked()
	if f := faces.Faces[id]; f != nil {
		return f.Person
	}
	return ""
}

// Two clearly different identities must not be put in one group. This is the
// failure that made the 2.10.2 implementation useless.
func TestClusterKeepsDifferentPeopleApart(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(1))
	var aIDs, bIDs []string
	for i := 0; i < 5; i++ {
		a, b := "a"+string(rune('0'+i)), "b"+string(rune('0'+i))
		addFace(a, personVec(rng, 1, 0.02), 0)
		addFace(b, personVec(rng, 2, 0.02), 0)
		aIDs, bIDs = append(aIDs, a), append(bIDs, b)
	}
	clusterFaces()

	groupA, groupB := personOf(aIDs[0]), personOf(bIDs[0])
	if groupA == "" || groupB == "" {
		t.Fatalf("faces were left ungrouped: A=%q B=%q", groupA, groupB)
	}
	if groupA == groupB {
		t.Fatal("two different identities were merged into one group")
	}
	for _, id := range aIDs {
		if personOf(id) != groupA {
			t.Errorf("%s landed in %q, want %q — one identity split", id, personOf(id), groupA)
		}
	}
	for _, id := range bIDs {
		if personOf(id) != groupB {
			t.Errorf("%s landed in %q, want %q", id, personOf(id), groupB)
		}
	}
}

// A group the user has named must survive re-clustering untouched — their
// decision outranks the algorithm.
func TestNamedPeopleSurviveReclustering(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 4; i++ {
		addFace("x"+string(rune('0'+i)), personVec(rng, 3, 0.02), 0)
	}
	clusterFaces()
	group := personOf("x0")
	if group == "" {
		t.Fatal("no group formed")
	}
	if err := namePerson(group, "Alex", false); err != nil {
		t.Fatal(err)
	}
	// Naming moves the group out of the automatic namespace on purpose:
	// clustering renumbers c:N every pass and would otherwise reuse the id.
	named := personOf("x0")
	if named == group {
		t.Fatalf("naming left the group in the automatic namespace (%q)", named)
	}

	// New photos arrive and everything is regrouped.
	for i := 0; i < 3; i++ {
		addFace("y"+string(rune('0'+i)), personVec(rng, 4, 0.02), 0)
	}
	clusterFaces()

	for _, id := range []string{"x0", "x1", "x2", "x3"} {
		if got := personOf(id); got != named {
			t.Errorf("%s left the named group: now %q, want %q", id, got, named)
		}
	}
	faceMu.Lock()
	p := faces.Persons[named]
	faceMu.Unlock()
	if p == nil || p.Name != "Alex" || !p.Named {
		t.Errorf("name was lost on re-clustering: %+v", p)
	}
}

// Merging is what makes ages work: the person keeps every face from every
// group, and the result is named so the next pass can't undo it.
func TestMergeKeepsEveryFaceAndSticks(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(3))
	// Two identities standing in for "same child, years apart" — far enough
	// apart that no threshold would join them automatically.
	for i := 0; i < 4; i++ {
		addFace("young"+string(rune('0'+i)), personVec(rng, 5, 0.02), 1000)
		addFace("older"+string(rune('0'+i)), personVec(rng, 6, 0.02), 9000)
	}
	clusterFaces()
	g1, g2 := personOf("young0"), personOf("older0")
	if g1 == "" || g2 == "" || g1 == g2 {
		t.Fatalf("expected two separate groups, got %q and %q", g1, g2)
	}

	if err := mergePeople([]string{g1, g2}); err != nil {
		t.Fatal(err)
	}
	merged := personOf("young0")
	if merged == "" {
		t.Fatal("merged faces have no group")
	}
	for _, id := range []string{"young0", "young3", "older0", "older3"} {
		if got := personOf(id); got != merged {
			t.Errorf("%s is in %q after merge, want %q — every face must come along", id, got, merged)
		}
	}
	// A merge is a decision, so it must outlast the next clustering pass.
	clusterFaces()
	for _, id := range []string{"young0", "older0"} {
		if got := personOf(id); got != merged {
			t.Errorf("re-clustering undid the merge: %s is now in %q, want %q", id, got, merged)
		}
	}
}

// "That isn't them" has to actually remove the face from the group.
func TestDetachRemovesFaceFromGroup(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 4; i++ {
		addFace("p"+string(rune('0'+i)), personVec(rng, 7, 0.02), 0)
	}
	clusterFaces()
	namePerson(personOf("p0"), "Sam", false)
	group := personOf("p0") // naming promotes the group to a stable id

	if err := detachFaces([]string{"p1"}); err != nil {
		t.Fatal(err)
	}
	if got := personOf("p1"); got != "" {
		t.Errorf("detached face still belongs to %q", got)
	}
	for _, id := range []string{"p0", "p2", "p3"} {
		if got := personOf(id); got != group {
			t.Errorf("detaching one face disturbed %s: now in %q, want %q", id, got, group)
		}
	}
}

// A one-off face shouldn't become a "person" — the People view would fill with
// strangers caught in the background.
func TestTinyClustersAreNotPeople(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(5))
	addFace("lone", personVec(rng, 8, 0.05), 0)
	for i := 0; i < faceMinClusterSize+1; i++ {
		addFace("real"+string(rune('0'+i)), personVec(rng, 9, 0.05), 0)
	}
	clusterFaces()

	if got := personOf("lone"); got != "" {
		t.Errorf("a single face became group %q", got)
	}
	if got := personOf("real0"); got == "" {
		t.Error("a genuine group was dropped")
	}
	for _, p := range listPeople() {
		if p.Count < faceMinClusterSize {
			t.Errorf("group %s has only %d faces, below the minimum", p.ID, p.Count)
		}
	}
}

// The store has to survive a restart, or naming people would be pointless.
func TestFaceStorePersists(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(6))
	for i := 0; i < 4; i++ {
		addFace("z"+string(rune('0'+i)), personVec(rng, 10, 0.02), 0)
	}
	clusterFaces()
	group := personOf("z0")
	namePerson(group, "Robin", false)

	if _, err := os.Stat(facesPath()); err != nil {
		t.Fatalf("nothing was written to disk: %v", err)
	}
	// Drop the in-memory copy, as a restart would.
	faceMu.Lock()
	faces = nil
	faceMu.Unlock()

	people := listPeople()
	if len(people) != 1 || people[0].Name != "Robin" {
		t.Fatalf("after reload got %+v, want one person named Robin", people)
	}
	if people[0].Count != 4 {
		t.Errorf("person has %d faces after reload, want 4", people[0].Count)
	}
}

func TestCosine(t *testing.T) {
	a := unit([]float32{1, 0, 0})
	if got := cosine(a, a); math.Abs(float64(got)-1) > 1e-6 {
		t.Errorf("cosine with itself = %v, want 1", got)
	}
	if got := cosine(a, unit([]float32{0, 1, 0})); math.Abs(float64(got)) > 1e-6 {
		t.Errorf("cosine of orthogonal vectors = %v, want 0", got)
	}
	if got := cosine(a, []float32{1, 0}); got != -1 {
		t.Errorf("mismatched lengths = %v, want -1", got)
	}
}

// The clustering tests are only meaningful if the synthetic identities are
// actually separable. Assert that directly, so a change to the generator shows
// up here rather than as a confusing failure somewhere else.
func TestSyntheticIdentitiesAreSeparable(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	a1, a2 := personVec(rng, 1, 0.02), personVec(rng, 1, 0.02)
	b1 := personVec(rng, 2, 0.02)

	same, diff := cosine(a1, a2), cosine(a1, b1)
	if same < faceSameThreshold {
		t.Errorf("same identity similarity %.3f is below the %.2f threshold — the tests would be testing noise", same, faceSameThreshold)
	}
	if diff >= faceSameThreshold {
		t.Errorf("different identities similarity %.3f reaches the %.2f threshold", diff, faceSameThreshold)
	}
	t.Logf("same=%.3f different=%.3f threshold=%.2f", same, diff, faceSameThreshold)
}

// Faces are keyed by path in three places — the id, Face.Path and the Seen
// record — so a move has to carry all three. Missing any one leaves broken
// crops, or makes the photo look unscanned so it is detected again and the
// person gains a duplicate.
func TestFacePathFollowsAMove(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(11))
	addFace("old/a.jpg#0", personVec(rng, 20, 0.02), 0)
	faceMu.Lock()
	faces.Faces["old/a.jpg#0"].Path = "old/a.jpg"
	faces.Seen["old/a.jpg"] = faceSeen{Size: 1, ModNs: 2, Faces: 1}
	faceMu.Unlock()

	renameFacePath("old/a.jpg", "new/a.jpg")

	faceMu.Lock()
	defer faceMu.Unlock()
	if _, stale := faces.Faces["old/a.jpg#0"]; stale {
		t.Error("the face is still under its old id")
	}
	fc := faces.Faces["new/a.jpg#0"]
	if fc == nil {
		t.Fatalf("no face at the new id; have %v", keysOf(faces.Faces))
	}
	if fc.Path != "new/a.jpg" {
		t.Errorf("Path = %q, want new/a.jpg — the crop would 404", fc.Path)
	}
	if _, ok := faces.Seen["new/a.jpg"]; !ok {
		t.Error("Seen was not re-keyed — the photo would be scanned again and duplicated")
	}
	if _, ok := faces.Seen["old/a.jpg"]; ok {
		t.Error("the old Seen entry is still there")
	}
}

// A folder move carries every photo underneath it, and nothing else.
func TestFacePrefixFollowsAFolderMove(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(12))
	set := func(id, path string) {
		addFace(id, personVec(rng, 21, 0.02), 0)
		faceMu.Lock()
		faces.Faces[id].Path = path
		faces.Seen[path] = faceSeen{Size: 1, ModNs: 2, Faces: 1}
		faceMu.Unlock()
	}
	set("Trip/a.jpg#0", "Trip/a.jpg")
	set("Trip/Inner/b.jpg#0", "Trip/Inner/b.jpg")
	set("Trips-other/c.jpg#0", "Trips-other/c.jpg") // shared name prefix: must not move

	renameFacePrefix("Trip", "Dest/Trip")

	faceMu.Lock()
	defer faceMu.Unlock()
	for _, want := range []string{"Dest/Trip/a.jpg#0", "Dest/Trip/Inner/b.jpg#0"} {
		if faces.Faces[want] == nil {
			t.Errorf("missing %s after the folder move; have %v", want, keysOf(faces.Faces))
		}
	}
	if faces.Faces["Trips-other/c.jpg#0"] == nil {
		t.Error("a sibling whose name merely starts with Trip was moved too")
	}
	if _, ok := faces.Seen["Dest/Trip/Inner/b.jpg"]; !ok {
		t.Error("a nested Seen entry was not re-keyed")
	}
}

// A deleted photo must not leave its faces sitting in a person's group.
func TestForgetFacesOnDelete(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(13))
	addFace("gone.jpg#0", personVec(rng, 22, 0.02), 0)
	addFace("kept.jpg#0", personVec(rng, 22, 0.02), 0)
	faceMu.Lock()
	faces.Faces["gone.jpg#0"].Path = "gone.jpg"
	faces.Faces["kept.jpg#0"].Path = "kept.jpg"
	faces.Seen["gone.jpg"] = faceSeen{}
	faceMu.Unlock()

	forgetFaces("gone.jpg")

	faceMu.Lock()
	defer faceMu.Unlock()
	if faces.Faces["gone.jpg#0"] != nil {
		t.Error("the deleted photo's face is still in the index")
	}
	if _, ok := faces.Seen["gone.jpg"]; ok {
		t.Error("the deleted photo is still marked as scanned")
	}
	if faces.Faces["kept.jpg#0"] == nil {
		t.Error("an unrelated photo's face was removed")
	}
}

// Files removed outside the app (over SMB, say) still have to be cleaned up.
func TestPruneMissingFaces(t *testing.T) {
	faceTestEnv(t)
	lib := t.TempDir()
	prevBase := baseDir
	baseDir = lib
	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()
	t.Cleanup(func() {
		baseDir = prevBase
		rootMu.Lock()
		rootCacheBase, rootCacheReal = "", ""
		rootMu.Unlock()
	})
	if err := os.WriteFile(filepath.Join(lib, "here.jpg"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(14))
	addFace("here.jpg#0", personVec(rng, 23, 0.02), 0)
	addFace("vanished.jpg#0", personVec(rng, 23, 0.02), 0)
	faceMu.Lock()
	faces.Faces["here.jpg#0"].Path = "here.jpg"
	faces.Faces["vanished.jpg#0"].Path = "vanished.jpg"
	faceMu.Unlock()

	if n := pruneMissingFaces(); n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	if faces.Faces["vanished.jpg#0"] != nil {
		t.Error("a face whose photo is gone survived the prune")
	}
	if faces.Faces["here.jpg#0"] == nil {
		t.Error("the prune removed a face whose photo is still there")
	}
}

func keysOf(m map[string]*Face) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Through the real handler, not the helper: this is what catches a move site
// that forgot to tell the face index. The unit tests above pass whether or not
// adminBatchMoveHandler actually calls them.
func TestBatchMoveHandlerCarriesFaces(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t) // temp library + isolated favorites
	faceTestEnv(t)
	mkPhoto(t, lib, "Album/a.jpg")
	if err := os.MkdirAll(filepath.Join(lib, "Dest"), 0755); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(21))
	addFace("Album/a.jpg#0", personVec(rng, 30, 0.02), 0)
	faceMu.Lock()
	faces.Faces["Album/a.jpg#0"].Path = "Album/a.jpg"
	faces.Seen["Album/a.jpg"] = faceSeen{Size: 5, ModNs: 7, Faces: 1}
	faceMu.Unlock()

	if _, out := moveBatch(t, `{"paths":["Album/a.jpg"],"destFolder":"Dest"}`); errorsOf(out) != "" {
		t.Fatalf("move failed: %s", errorsOf(out))
	}

	faceMu.Lock()
	defer faceMu.Unlock()
	if faces.Faces["Dest/a.jpg#0"] == nil {
		t.Errorf("the face did not follow the move; have %v", keysOf(faces.Faces))
	}
	if faces.Faces["Album/a.jpg#0"] != nil {
		t.Error("the face is still recorded at the old path — its crop would 404")
	}
	if _, ok := faces.Seen["Dest/a.jpg"]; !ok {
		t.Error("Seen was not re-keyed: the photo would be re-detected and the person would gain a duplicate")
	}
}

// Same, for a folder move.
func TestBatchMoveHandlerCarriesFacesForFolders(t *testing.T) {
	withUsers(t, nil)
	lib := favEnv(t)
	faceTestEnv(t)
	mkPhoto(t, lib, "Trip/Inner/b.jpg")
	if err := os.MkdirAll(filepath.Join(lib, "Dest"), 0755); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(22))
	addFace("Trip/Inner/b.jpg#0", personVec(rng, 31, 0.02), 0)
	faceMu.Lock()
	faces.Faces["Trip/Inner/b.jpg#0"].Path = "Trip/Inner/b.jpg"
	faces.Seen["Trip/Inner/b.jpg"] = faceSeen{Size: 5, ModNs: 7, Faces: 1}
	faceMu.Unlock()

	if _, out := moveBatch(t, `{"paths":["Trip"],"destFolder":"Dest"}`); errorsOf(out) != "" {
		t.Fatalf("move failed: %s", errorsOf(out))
	}

	faceMu.Lock()
	defer faceMu.Unlock()
	if faces.Faces["Dest/Trip/Inner/b.jpg#0"] == nil {
		t.Errorf("a nested face did not follow the folder move; have %v", keysOf(faces.Faces))
	}
	if _, ok := faces.Seen["Dest/Trip/Inner/b.jpg"]; !ok {
		t.Error("the nested Seen entry was not re-keyed")
	}
}

// Naming two groups the same thing is how someone says "these are the same
// person" — the natural move when one person is split across ages. It must not
// silently create two people with one name, and it must not silently merge
// either, since families reuse names.
func TestSameNameReportsAConflict(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(41))
	for i := 0; i < 4; i++ {
		addFace("young"+string(rune('0'+i)), personVec(rng, 50, 0.02), 0)
		addFace("older"+string(rune('0'+i)), personVec(rng, 51, 0.02), 0)
	}
	clusterFaces()
	g1, g2 := personOf("young0"), personOf("older0")
	if g1 == "" || g2 == "" || g1 == g2 {
		t.Fatalf("expected two groups, got %q and %q", g1, g2)
	}
	if err := namePerson(g1, "Alex", false); err != nil {
		t.Fatal(err)
	}

	// Naming the second group "Alex" must report the clash and change nothing.
	err := namePerson(g2, "Alex", false)
	c, ok := err.(*nameConflict)
	if !ok {
		t.Fatalf("second name returned %v, want a nameConflict", err)
	}
	if c.Count != 4 {
		t.Errorf("conflict reports %d photos, want 4", c.Count)
	}
	if len(listPeople()) != 2 {
		t.Error("the conflicting name was applied anyway")
	}
	if got := personOf("older0"); got != g2 {
		t.Errorf("the second group moved despite the conflict: now %q", got)
	}

	// With merge, the two become one person holding every face.
	if err := namePerson(g2, "Alex", true); err != nil {
		t.Fatal(err)
	}
	people := listPeople()
	if len(people) != 1 {
		t.Fatalf("after merging got %d people, want 1: %+v", len(people), people)
	}
	if people[0].Count != 8 {
		t.Errorf("merged person has %d faces, want all 8", people[0].Count)
	}
	// And it has to survive the next pass, like any other merge.
	clusterFaces()
	if got := personOf("young0"); got != personOf("older0") {
		t.Error("re-clustering split the name-merged person again")
	}
}

// Case and stray spaces are the same intent, not a different person.
func TestNameConflictIgnoresCaseAndSpace(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 4; i++ {
		addFace("a"+string(rune('0'+i)), personVec(rng, 52, 0.02), 0)
		addFace("b"+string(rune('0'+i)), personVec(rng, 53, 0.02), 0)
	}
	clusterFaces()
	namePerson(personOf("a0"), "Alex", false)

	if _, ok := namePerson(personOf("b0"), "  alex ", false).(*nameConflict); !ok {
		t.Error(`"  alex " was treated as a different person from "Alex"`)
	}
}

// Renaming a person to the name they already have must not report a conflict
// with themselves.
func TestRenamingToOwnNameIsFine(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(43))
	for i := 0; i < 4; i++ {
		addFace("s"+string(rune('0'+i)), personVec(rng, 54, 0.02), 0)
	}
	clusterFaces()
	id := personOf("s0")
	namePerson(id, "Sam", false)
	if err := namePerson(personOf("s0"), "Sam", false); err != nil {
		t.Errorf("renaming a person to their own name failed: %v", err)
	}
}

// Assigning a face by hand is the positive correction — "this one IS Alex".
// Without it a face in the wrong group can only be pushed out, and the next
// pass is free to put it straight back.
func TestAssignFaceToExistingPerson(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(61))
	for i := 0; i < 4; i++ {
		addFace("alex"+string(rune('0'+i)), personVec(rng, 60, 0.02), 0)
		addFace("sam"+string(rune('0'+i)), personVec(rng, 61, 0.02), 0)
	}
	clusterFaces()
	alex, sam := personOf("alex0"), personOf("sam0")
	if alex == "" || sam == "" || alex == sam {
		t.Fatalf("expected two groups, got %q and %q", alex, sam)
	}
	namePerson(alex, "Alex", false)
	alex = personOf("alex0")

	// One of Sam's faces is really Alex.
	if _, err := assignFaces([]string{"sam0"}, alex, ""); err != nil {
		t.Fatal(err)
	}
	if got := personOf("sam0"); got != alex {
		t.Errorf("assigned face is in %q, want %q", got, alex)
	}

	// The correction must survive the next pass — that is the whole point.
	clusterFaces()
	if got := personOf("sam0"); got != alex {
		t.Errorf("re-clustering undid the hand assignment: now %q, want %q", got, alex)
	}
}

// Assigning to a name that doesn't exist creates that person; assigning to one
// that does targets them rather than making a second person with one name.
func TestAssignByName(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(62))
	for i := 0; i < 4; i++ {
		addFace("f"+string(rune('0'+i)), personVec(rng, 62, 0.02), 0)
	}
	clusterFaces()

	id, err := assignFaces([]string{"f0"}, "", "Robin")
	if err != nil {
		t.Fatal(err)
	}
	if personOf("f0") != id {
		t.Error("the face was not moved to the new person")
	}

	// A second assignment by the same name must reuse the person.
	id2, err := assignFaces([]string{"f1"}, "", "  robin ")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Errorf("assigning to %q made a second person (%s vs %s)", "robin", id2, id)
	}
	named := 0
	for _, p := range listPeople() {
		if p.Name == "Robin" {
			named++
		}
	}
	if named != 1 {
		t.Errorf("got %d people called Robin, want 1", named)
	}
}

// A bad request must not half-apply.
func TestAssignRejectsUnknownTargets(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(63))
	addFace("g0", personVec(rng, 63, 0.02), 0)

	if _, err := assignFaces([]string{"g0"}, "p:999", ""); err == nil {
		t.Error("assigning to a person that doesn't exist was accepted")
	}
	if _, err := assignFaces([]string{"nope"}, "", "Someone"); err == nil {
		t.Error("assigning a face that doesn't exist was accepted")
	}
}

// Assigning into a group that hasn't been named yet is still a decision, so it
// has to be protected from the next clustering pass like any other. Without
// this the correction is silently undone on the next scan — the worst kind of
// bug, because nothing reports it.
func TestAssignToUnnamedGroupSticks(t *testing.T) {
	faceTestEnv(t)
	rng := rand.New(rand.NewSource(64))
	for i := 0; i < 4; i++ {
		addFace("one"+string(rune('0'+i)), personVec(rng, 70, 0.02), 0)
		addFace("two"+string(rune('0'+i)), personVec(rng, 71, 0.02), 0)
	}
	clusterFaces()
	g1, g2 := personOf("one0"), personOf("two0")
	if g1 == "" || g2 == "" || g1 == g2 {
		t.Fatalf("expected two groups, got %q and %q", g1, g2)
	}

	// Neither group has been named. Move a face across anyway.
	if _, err := assignFaces([]string{"two0"}, g1, ""); err != nil {
		t.Fatal(err)
	}
	if got := personOf("two0"); got != g1 {
		t.Fatalf("face is in %q right after the assignment, want %q", got, g1)
	}

	clusterFaces()
	if got := personOf("two0"); got != g1 {
		t.Errorf("re-clustering undid an assignment into an unnamed group: now %q, want %q", got, g1)
	}
}

// The upgrade path for someone who moved photos while running a build that
// didn't track faces: the index still points at the old locations, and the
// files are somewhere else. The scan finds them at their new paths and the
// prune drops the stale records — the ordering matters, because if grouping
// ran first the same photo would appear twice in one person.
func TestStaleRecordsFromOldMovesArePrunedNotDuplicated(t *testing.T) {
	faceTestEnv(t)
	lib := t.TempDir()
	prevBase := baseDir
	baseDir = lib
	rootMu.Lock()
	rootCacheBase, rootCacheReal = "", ""
	rootMu.Unlock()
	t.Cleanup(func() {
		baseDir = prevBase
		rootMu.Lock()
		rootCacheBase, rootCacheReal = "", ""
		rootMu.Unlock()
	})

	// The photo now lives at the new path; nothing is at the old one.
	if err := os.MkdirAll(filepath.Join(lib, "New"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "New", "a.jpg"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(81))
	shared := personVec(rng, 90, 0.02)
	// Three faces of one person that never moved...
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("Stay/p%d.jpg#0", i)
		addFace(id, personVec(rng, 90, 0.02), 0)
		faceMu.Lock()
		faces.Faces[id].Path = fmt.Sprintf("Stay/p%d.jpg", i)
		faceMu.Unlock()
		if err := os.MkdirAll(filepath.Join(lib, "Stay"), 0755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(lib, "Stay", fmt.Sprintf("p%d.jpg", i)), []byte("x"), 0644)
	}
	// ...a stale record pointing at where the moved photo used to be...
	addFace("Old/a.jpg#0", shared, 0)
	faceMu.Lock()
	faces.Faces["Old/a.jpg#0"].Path = "Old/a.jpg"
	faces.Seen["Old/a.jpg"] = faceSeen{Size: 1, ModNs: 1, Faces: 1}
	// ...and the record a fresh scan would create at the new path.
	faceMu.Unlock()
	addFace("New/a.jpg#0", shared, 0)
	faceMu.Lock()
	faces.Faces["New/a.jpg#0"].Path = "New/a.jpg"
	faceMu.Unlock()

	if n := pruneMissingFaces(); n != 1 {
		t.Errorf("pruned %d, want 1 (just the stale record)", n)
	}
	clusterFaces()

	faceMu.Lock()
	defer faceMu.Unlock()
	if faces.Faces["Old/a.jpg#0"] != nil {
		t.Error("the stale record survived — its crop would 404 inside a group")
	}
	if _, ok := faces.Seen["Old/a.jpg"]; ok {
		t.Error("the stale Seen entry survived")
	}
	fresh := faces.Faces["New/a.jpg#0"]
	if fresh == nil {
		t.Fatal("the re-detected face was lost")
	}
	// One photo, one face in the group — not two.
	seen := map[string]int{}
	for _, f := range faces.Faces {
		if f.Person == fresh.Person {
			seen[f.Path]++
		}
	}
	for p, n := range seen {
		if n > 1 {
			t.Errorf("%s appears %d times in the group — the move produced a duplicate", p, n)
		}
	}
}
