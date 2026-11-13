package taxonomy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDerpibooruRejectsUnverifiedTaxonomy(t *testing.T) {
	const valid = `{"id":10,"name":"sky","category":null}`
	const wrongID = `{"id":99,"name":"sky","category":"character"}`
	for _, tc := range []struct {
		name     string
		tags     any
		ids      any
		batch    string
		exact    string
		want     string
		requests int32
	}{
		{name: "missing tags", want: "tags must be an array"},
		{name: "string tags", tags: "sky", want: "tags must be an array"},
		{name: "mixed tag types", tags: []any{"sky", 99}, want: "entry 2 is not a string"},
		{name: "blank tag", tags: []string{"sky", " "}, want: "entry 2 is blank"},
		{name: "empty with tag IDs", tags: []string{}, ids: []int{10}, want: "empty tags array"},
		{name: "conflicting tag IDs", tags: []string{"sky", "sky"}, ids: []int{10, 99}, want: "conflicting ids"},
		{name: "missing category", tags: []string{"sky"}, exact: `{"id":10,"name":"sky"}`, want: "missing or empty category", requests: 1},
		{name: "unknown category", tags: []string{"sky"}, exact: `{"id":10,"name":"sky","category":"new"}`, want: "unknown category", requests: 1},
		{name: "invalid category type", tags: []string{"sky"}, exact: `{"id":10,"name":"sky","category":true}`, want: "decoding Derpibooru response", requests: 1},
		{name: "wrong ID for same name", tags: []string{"sky"}, ids: []int{10}, batch: wrongID, exact: valid, want: "expected id 10, got 99", requests: 1},
		{name: "wrong name for same ID", tags: []string{"sky"}, ids: []int{10}, batch: `{"id":10,"name":"other sky","category":null}`, exact: valid, want: `expected name "sky"`, requests: 1},
		{name: "wrong ID after incomplete batch", tags: []string{"sky"}, ids: []int{10}, exact: wrongID, want: "expected id 10, got 99", requests: 2},
		{name: "temporarily unavailable", tags: []string{"sky"}, exact: "503", want: "HTTP 503", requests: derpibooruMaxAttempts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				record := tc.exact
				if strings.HasPrefix(r.URL.Query().Get("q"), "id:") {
					record = tc.batch
				}
				if record == "503" {
					w.Header().Set("Retry-After", "0")
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				fmt.Fprintf(w, `{"tags":[%s]}`, record)
			}))
			defer server.Close()
			cache := filepath.Join(t.TempDir(), "derpibooru.json")
			d, err := newDerpibooru(cache, server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			meta := map[string]any{"category": "derpibooru", "rating": "safe", "tags": tc.tags, "tag_ids": tc.ids}
			if err := d.Enrich(context.Background(), meta); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Enrich error = %v, want %q", err, tc.want)
			}
			if got := calls.Load(); got != tc.requests {
				t.Errorf("taxonomy requests = %d, want %d", got, tc.requests)
			}
			if !reflect.DeepEqual(meta["tags"], tc.tags) || len(d.cache) != 0 {
				t.Errorf("rejected response changed tags or cache: %v, %v", meta["tags"], d.cache)
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Errorf("rejected response wrote cache: %v", err)
			}
		})
	}
}

func TestDerpibooruRevalidatesLegacyCacheAndPersistsNullCategory(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch q := r.URL.Query().Get("q"); q {
		case "id:10":
			fmt.Fprint(w, `{"tags":[{"id":10,"name":"sky","category":null}]}`)
		case "id:11":
			fmt.Fprint(w, `{"tags":[{"id":10,"name":"sky","category":"character"}]}`)
		default:
			t.Errorf("unexpected taxonomy query: %q", q)
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "derpibooru.json")
	const legacy = `{"sky":{"id":10,"name":"sky"},"pony":{"id":20,"name":"pony","category":"species"}}`
	if err := os.WriteFile(cache, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	open := func() *Derpibooru {
		t.Helper()
		d, err := newDerpibooru(cache, server.URL, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	meta := func(skyID int) map[string]any {
		return map[string]any{"category": "derpibooru", "tags": []string{"sky", "pony"}, "tag_ids": []int{skyID, 20}}
	}
	for i := range 2 {
		d := open()
		post := meta(10)
		if err := d.Enrich(context.Background(), post); err != nil {
			t.Fatal(err)
		}
		assertTags(t, post["tags"], []string{"sky", "species:pony"})
		if got := calls.Load(); got != 1 {
			t.Fatalf("pass %d: taxonomy requests = %d, want one total", i+1, got)
		}
	}
	d := open()
	if err := d.Enrich(context.Background(), meta(11)); err == nil || !strings.Contains(err.Error(), "expected id 11, got 10") {
		t.Fatalf("accepted a cached or returned record for the wrong ID: %v", err)
	}
	post := meta(10)
	if err := d.Enrich(context.Background(), post); err != nil {
		t.Fatal(err)
	}
	assertTags(t, post["tags"], []string{"sky", "species:pony"})
	if got := calls.Load(); got != 2 {
		t.Fatalf("taxonomy requests = %d, want two total", got)
	}
}
