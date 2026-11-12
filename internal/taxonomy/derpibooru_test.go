package taxonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestDerpibooruEnrich(t *testing.T) {
	records := map[string]tagRecord{
		"safe": {
			ID:              1,
			Name:            "safe",
			Category:        "rating",
			NameInNamespace: "safe",
		},
		"rainbow dash": {
			ID:              2,
			Name:            "rainbow dash",
			Category:        "character",
			NameInNamespace: "rainbow dash",
		},
		"pony": {
			ID:              3,
			Name:            "pony",
			Category:        "species",
			NameInNamespace: "pony",
		},
		"artist:test": {
			ID:              4,
			Name:            "artist:test",
			Namespace:       "artist",
			NameInNamespace: "test",
		},
		"equestria at war mod": {
			ID:              5,
			Name:            "equestria at war mod",
			Category:        "content-fanmade",
			NameInNamespace: "equestria at war mod",
		},
		"wings": {
			ID:              6,
			Name:            "wings",
			NameInNamespace: "wings",
		},
		"my little pony": {
			ID:              7,
			Name:            "my little pony",
			Category:        "origin",
			NameInNamespace: "my little pony",
		},
	}

	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.URL.Query().Get("q")

			rec, ok := records[name]
			if !ok {
				http.Error(
					w,
					"unknown test tag",
					http.StatusNotFound,
				)
				return
			}

			_ = json.NewEncoder(w).Encode(
				tagSearchResponse{
					Tags: []tagRecord{rec},
				},
			)
		}),
	)
	defer server.Close()

	cachePath := filepath.Join(
		t.TempDir(),
		"derpibooru.json",
	)

	resolver, err := newDerpibooru(
		cachePath,
		server.URL,
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": "derpibooru",
		"tags": []any{
			"safe",
			"rainbow dash",
			"pony",
			"artist:test",
			"equestria at war mod",
			"wings",
			"my little pony",
		},
	}

	if err := resolver.Enrich(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	assertTags(
		t,
		meta["tags"],
		[]string{
			"safe",
			"character:rainbow dash",
			"species:pony",
			"artist:test",
			"meta:equestria at war mod",
			"wings",
			"meta:my little pony",
		},
	)

	/*
		A fresh resolver must be able to reproduce the result using only
		the persistent cache.
	*/
	server.Close()

	cached, err := newDerpibooru(
		cachePath,
		server.URL,
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	meta2 := map[string]any{
		"category": "derpibooru",
		"tags": []any{
			"safe",
			"rainbow dash",
			"pony",
			"artist:test",
			"equestria at war mod",
			"wings",
			"my little pony",
		},
	}

	if err := cached.Enrich(
		context.Background(),
		meta2,
	); err != nil {
		t.Fatalf(
			"cached enrichment tried the network: %v",
			err,
		)
	}

	if !reflect.DeepEqual(meta, meta2) {
		t.Fatalf(
			"cached result differs:\nfirst: %#v\ncached: %#v",
			meta,
			meta2,
		)
	}
}

func TestDerpibooruOtherSiteIsUntouched(t *testing.T) {
	resolver, err := newDerpibooru(
		"",
		"http://127.0.0.1:1",
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": "danbooru",
		"tags":     []any{"safe", "pony"},
	}

	before := map[string]any{
		"category": "danbooru",
		"tags":     []any{"safe", "pony"},
	}

	if err := resolver.Enrich(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(meta, before) {
		t.Fatalf(
			"non-Derpibooru metadata changed: %#v",
			meta,
		)
	}
}

func assertTags(
	t *testing.T,
	got any,
	want []string,
) {
	t.Helper()

	actual, ok := got.([]string)
	if !ok {
		t.Fatalf(
			"tags have type %T; want []string",
			got,
		)
	}

	if !reflect.DeepEqual(actual, want) {
		t.Fatalf(
			"tags = %#v, want %#v",
			actual,
			want,
		)
	}
}

func TestDerpibooruTargetSemantics(t *testing.T) {
	tests := []struct {
		name       string
		rec        tagRecord
		source     string
		wantTarget string
		wantName   string
		wantRating bool
	}{
		{
			name: "artist namespace overrides origin category",
			rec: tagRecord{
				Category:        "origin",
				Namespace:       "artist",
				NameInNamespace: "redruin01",
			},
			source:     "artist:redruin01",
			wantTarget: "artist",
			wantName:   "redruin01",
		},
		{
			name: "named oc namespace is a character",
			rec: tagRecord{
				Category:        "oc",
				Namespace:       "oc",
				NameInNamespace: "anon",
			},
			source:     "oc:anon",
			wantTarget: "character",
			wantName:   "anon",
		},
		{
			name: "generic oc tag is metadata",
			rec: tagRecord{
				Category:        "oc",
				NameInNamespace: "oc",
			},
			source:     "oc",
			wantTarget: "meta",
			wantName:   "oc",
		},
		{
			name: "canonical character category",
			rec: tagRecord{
				Category:        "character",
				NameInNamespace: "rainbow dash",
			},
			source:     "rainbow dash",
			wantTarget: "character",
			wantName:   "rainbow dash",
		},
		{
			name: "species category",
			rec: tagRecord{
				Category:        "species",
				NameInNamespace: "pegasus",
			},
			source:     "pegasus",
			wantTarget: "species",
			wantName:   "pegasus",
		},
		{
			name: "origin category is metadata",
			rec: tagRecord{
				Category:        "origin",
				NameInNamespace: "screencap",
			},
			source:     "screencap",
			wantTarget: "meta",
			wantName:   "screencap",
		},
		{
			name: "fanfic namespace retains source spelling",
			rec: tagRecord{
				Category:        "content-fanmade",
				Namespace:       "fanfic",
				NameInNamespace: "excuses",
			},
			source:     "fanfic:excuses",
			wantTarget: "meta",
			wantName:   "fanfic:excuses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, name, rating := derpibooruTarget(tt.rec, tt.source)

			if target != tt.wantTarget ||
				name != tt.wantName ||
				rating != tt.wantRating {
				t.Fatalf(
					"derpibooruTarget() = (%q, %q, %v), want (%q, %q, %v)",
					target,
					name,
					rating,
					tt.wantTarget,
					tt.wantName,
					tt.wantRating,
				)
			}
		})
	}
}

func TestDerpibooruEnrichSerializesCacheMisses(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32

	server := httptest.NewServer(
		http.HandlerFunc(func(
			w http.ResponseWriter,
			r *http.Request,
		) {
			n := active.Add(1)

			for {
				old := maxActive.Load()

				if n <= old ||
					maxActive.CompareAndSwap(old, n) {
					break
				}
			}

			defer active.Add(-1)

			time.Sleep(20 * time.Millisecond)

			name := r.URL.Query().Get("q")

			_ = json.NewEncoder(w).Encode(
				tagSearchResponse{
					Tags: []tagRecord{
						{
							Name:            name,
							NameInNamespace: name,
						},
					},
				},
			)
		}),
	)
	defer server.Close()

	resolver, err := newDerpibooru(
		"",
		server.URL,
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": "derpibooru",
		"tags": []any{
			"one",
			"two",
			"three",
			"four",
		},
	}

	if err := resolver.Enrich(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if got := maxActive.Load(); got != 1 {
		t.Fatalf(
			"maximum concurrent requests = %d; want 1",
			got,
		)
	}
}

func TestDerpibooruRetries429(t *testing.T) {
	var calls atomic.Int32

	server := httptest.NewServer(
		http.HandlerFunc(func(
			w http.ResponseWriter,
			r *http.Request,
		) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}

			name := r.URL.Query().Get("q")

			_ = json.NewEncoder(w).Encode(
				tagSearchResponse{
					Tags: []tagRecord{
						{
							Name:            name,
							NameInNamespace: name,
						},
					},
				},
			)
		}),
	)
	defer server.Close()

	resolver, err := newDerpibooru(
		"",
		server.URL,
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": "derpibooru",
		"tags": []any{
			"rainbow dash",
		},
	}

	if err := resolver.Enrich(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf(
			"request count = %d; want 2",
			got,
		)
	}
}

func TestDerpibooruWarmPreloadsWithoutMutation(t *testing.T) {
	var calls atomic.Int32

	records := map[string]tagRecord{
		"rainbow dash": {
			Name:            "rainbow dash",
			Category:        "character",
			NameInNamespace: "rainbow dash",
		},
		"pony": {
			Name:            "pony",
			Category:        "species",
			NameInNamespace: "pony",
		},
	}

	server := httptest.NewServer(
		http.HandlerFunc(func(
			w http.ResponseWriter,
			r *http.Request,
		) {
			calls.Add(1)

			name := r.URL.Query().Get("q")
			rec, ok := records[name]

			if !ok {
				http.Error(w, "unexpected tag", http.StatusBadRequest)
				return
			}

			_ = json.NewEncoder(w).Encode(
				tagSearchResponse{
					Tags: []tagRecord{rec},
				},
			)
		}),
	)
	defer server.Close()

	resolver, err := newDerpibooru(
		"",
		server.URL,
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": "derpibooru",
		"tags": []any{
			"rainbow dash",
			"pony",
		},
	}

	before := fmt.Sprint(meta["tags"])

	if err := resolver.Warm(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if got := fmt.Sprint(meta["tags"]); got != before {
		t.Fatalf(
			"Warm mutated tags: got %s, want %s",
			got,
			before,
		)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf(
			"Warm request count = %d; want 2",
			got,
		)
	}

	if err := resolver.Enrich(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf(
			"Enrich made new requests after Warm: calls = %d, want 2",
			got,
		)
	}

	assertTags(
		t,
		meta["tags"],
		[]string{
			"character:rainbow dash",
			"species:pony",
		},
	)
}

func TestDerpibooruEnrichBatchesTagIDs(t *testing.T) {
	var calls atomic.Int32

	server := httptest.NewServer(
		http.HandlerFunc(func(
			w http.ResponseWriter,
			r *http.Request,
		) {
			calls.Add(1)

			got := r.URL.Query().Get("q")
			want := "id:101 || id:102 || id:103"

			if got != want {
				http.Error(
					w,
					"unexpected query: "+got,
					http.StatusBadRequest,
				)
				return
			}

			_ = json.NewEncoder(w).Encode(
				tagSearchResponse{
					Tags: []tagRecord{
						{
							Name:            "cute",
							NameInNamespace: "cute",
						},
						{
							Name:            "rainbow dash",
							Category:        "character",
							NameInNamespace: "rainbow dash",
						},
						{
							Name:            "pony",
							Category:        "species",
							NameInNamespace: "pony",
						},
					},
				},
			)
		}),
	)
	defer server.Close()

	resolver, err := newDerpibooru(
		"",
		server.URL,
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": "derpibooru",
		"tags": []any{
			"cute",
			"rainbow dash",
			"pony",
		},
		"tag_ids": []any{
			float64(101),
			float64(102),
			float64(103),
		},
	}

	if err := resolver.Enrich(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf(
			"request count = %d; want 1 batch request",
			got,
		)
	}

	assertTags(
		t,
		meta["tags"],
		[]string{
			"cute",
			"character:rainbow dash",
			"species:pony",
		},
	)
}
