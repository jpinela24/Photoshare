package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// withDateIndex installs a fixed date index for one test.
func withDateIndex(t *testing.T, files []datedFile) {
	t.Helper()
	dateIndexMu.Lock()
	prev, prevBuilt := dateIndex, dateIndexBuilt
	dateIndex, dateIndexBuilt = files, true
	dateIndexMu.Unlock()
	t.Cleanup(func() {
		dateIndexMu.Lock()
		dateIndex, dateIndexBuilt = prev, prevBuilt
		dateIndexMu.Unlock()
	})
}

type timelineResp struct {
	Built bool `json:"built"`
	Total int  `json:"total"`
	Items []struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		IsVideo bool   `json:"isVideo"`
		Taken   int64  `json:"taken"`
	} `json:"items"`
	Buckets []struct {
		Key    string `json:"key"`
		Label  string `json:"label"`
		Count  int    `json:"count"`
		Offset int    `json:"offset"`
	} `json:"buckets"`
}

func getTimeline(t *testing.T, query string) timelineResp {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/timeline"+query, nil)
	rec := httptest.NewRecorder()
	timelineHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got timelineResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// index builds a descending-by-date index the way buildDateIndex leaves it.
func index(t *testing.T) []datedFile {
	t.Helper()
	mk := func(p string, y int, m time.Month, d int) datedFile {
		return datedFile{Path: p, Taken: time.Date(y, m, d, 12, 0, 0, 0, time.UTC)}
	}
	return []datedFile{
		mk("2024/c.jpg", 2024, time.March, 3),
		mk("2024/b.jpg", 2024, time.March, 2),
		mk("2024/a.jpg", 2024, time.March, 1),
		mk("2023/z.jpg", 2023, time.December, 9),
		mk("2019/y.jpg", 2019, time.July, 4),
	}
}

// The month rail jumps by offset, so each bucket's offset must be the index
// where that month actually starts. If these drift, clicking "July 2019" lands
// the user in the wrong year — silently, since the page still renders.
func TestTimelineBucketOffsetsPointAtTheirMonth(t *testing.T) {
	idx := index(t)
	withDateIndex(t, idx)

	got := getTimeline(t, "?limit=500")
	if got.Total != len(idx) {
		t.Errorf("total = %d, want %d", got.Total, len(idx))
	}
	want := []struct {
		key    string
		count  int
		offset int
	}{
		{"2024-03", 3, 0},
		{"2023-12", 1, 3},
		{"2019-07", 1, 4},
	}
	if len(got.Buckets) != len(want) {
		t.Fatalf("got %d buckets, want %d: %+v", len(got.Buckets), len(want), got.Buckets)
	}
	for i, w := range want {
		b := got.Buckets[i]
		if b.Key != w.key || b.Count != w.count || b.Offset != w.offset {
			t.Errorf("bucket %d = %s count=%d offset=%d, want %s count=%d offset=%d",
				i, b.Key, b.Count, b.Offset, w.key, w.count, w.offset)
		}
		// The offset must actually land on the first item of that month.
		page := getTimeline(t, "?limit=1&offset="+strconv.Itoa(b.Offset))
		if len(page.Items) != 1 {
			t.Fatalf("bucket %s: no item at offset %d", b.Key, b.Offset)
		}
		if at := time.Unix(page.Items[0].Taken, 0).UTC().Format("2006-01"); at != b.Key {
			t.Errorf("bucket %s offset %d lands in %s", b.Key, b.Offset, at)
		}
	}
}

// Buckets describe the whole list, so re-sending them with every page would be
// wasted bytes on a library with hundreds of months.
func TestTimelineBucketsOnlyOnFirstPage(t *testing.T) {
	withDateIndex(t, index(t))
	if got := getTimeline(t, "?offset=0&limit=2"); len(got.Buckets) == 0 {
		t.Error("first page carried no buckets")
	}
	if got := getTimeline(t, "?offset=2&limit=2"); got.Buckets != nil {
		t.Errorf("later page carried buckets: %+v", got.Buckets)
	}
}

// Paging must cover the list exactly once, in order, with no gap or repeat at
// the page boundary — a repeat would render duplicate cards in the grid.
func TestTimelinePagingIsContiguous(t *testing.T) {
	idx := index(t)
	withDateIndex(t, idx)

	var seen []string
	for off := 0; off < len(idx); off += 2 {
		for _, it := range getTimeline(t, "?limit=2&offset="+strconv.Itoa(off)).Items {
			seen = append(seen, it.Path)
		}
	}
	if len(seen) != len(idx) {
		t.Fatalf("paged %d items, want %d", len(seen), len(idx))
	}
	for i, p := range seen {
		if p != idx[i].Path {
			t.Errorf("item %d = %s, want %s", i, p, idx[i].Path)
		}
	}
}

// A hand-edited URL must not be able to ask for a negative offset or a page
// large enough to serialize the whole library at once.
func TestTimelineClampsQueryParams(t *testing.T) {
	withDateIndex(t, index(t))

	if got := getTimeline(t, "?offset=-5&limit=2"); len(got.Items) == 0 || got.Items[0].Path != "2024/c.jpg" {
		t.Errorf("negative offset did not clamp to the start: %+v", got.Items)
	}
	if got := getTimeline(t, "?offset=9999"); len(got.Items) != 0 {
		t.Errorf("offset past the end returned %d items, want 0", len(got.Items))
	}
	if got := getTimeline(t, "?limit=100000"); len(got.Items) != 5 {
		t.Errorf("oversized limit returned %d items, want the whole 5-item index", len(got.Items))
	}
	if got := getTimeline(t, "?limit=notanumber"); len(got.Items) != 5 {
		t.Errorf("unparseable limit should fall back to the default, got %d items", len(got.Items))
	}
}
