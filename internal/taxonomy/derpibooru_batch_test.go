package taxonomy

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/mapping"
)

func TestDerpibooruBatch200PostParity(t *testing.T) {
	const posts = 200
	const ratingID = 1000
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		terms := strings.Split(r.URL.Query().Get("q"), " || ")
		if len(terms) > derpibooruBatchSize {
			t.Errorf("request had %d terms", len(terms))
		}
		var tags []tagRecord
		for _, term := range terms {
			id, err := strconv.Atoi(strings.TrimPrefix(term, "id:"))
			if err != nil {
				t.Errorf("unexpected taxonomy query %q", term)
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			record := tagRecord{ID: int64(id), Name: fmt.Sprintf("fixture %d", id), Category: tagCategory{uncategorized: true}}
			switch id {
			case ratingID:
				record.Name, record.Category = "safe", tagCategory{value: "rating"}
			case 1:
				record.Aliases = []string{"block+alias"}
			}
			tags = append(tags, record)
		}
		_ = json.NewEncoder(w).Encode(tagSearchResponse{Tags: tags})
	}))
	defer server.Close()
	var raw, baseline, batched []map[string]any
	for id := 1; id <= posts; id++ {
		meta := map[string]any{"category": "derpibooru", "tags": []string{"safe", fmt.Sprintf("fixture %d", id)}, "tag_ids": []int{ratingID, id}}
		raw = append(raw, meta)
		baseline = append(baseline, maps.Clone(meta))
		batched = append(batched, maps.Clone(meta))
	}
	newResolver := func() *Derpibooru {
		t.Helper()
		d, err := newDerpibooru("", server.URL, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	cfg := config.Default()
	cfg.Tags.Blacklist = []string{"block_alias"}
	mapper, err := mapping.New(config.NewProvider(cfg), "")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := mapper.StrictBlacklist("derpibooru")
	if err != nil {
		t.Fatal(err)
	}

	single := newResolver()
	started := time.Now()
	for _, meta := range baseline {
		if err := single.Enrich(t.Context(), meta); err != nil {
			t.Fatal(err)
		}
	}
	baselineTime := time.Since(started)
	if got := calls.Swap(0); got != posts {
		t.Fatalf("baseline made %d calls, want %d", got, posts)
	}
	batch := newResolver()
	started = time.Now()
	for _, err := range batch.EnrichBatch(t.Context(), "derpibooru", batched) {
		if err != nil {
			t.Fatal(err)
		}
	}
	batchTime := time.Since(started)
	if got := calls.Swap(0); got != 5 {
		t.Fatalf("batch made %d calls, want 5 for 201 distinct IDs", got)
	}
	for i := range raw {
		want, got := rules.Match(raw[i], baseline[i]), rules.Match(raw[i], batched[i])
		if got != want || (got != "") != (i == 0) ||
			!slices.Equal(mapper.Map(baseline[i]).Tags, mapper.Map(batched[i]).Tags) {
			t.Fatalf("post %d: baseline=%q batch=%q", i+1, want, got)
		}
	}
	warm := make([]map[string]any, len(raw))
	for i := range raw {
		warm[i] = maps.Clone(raw[i])
	}
	for _, err := range batch.EnrichBatch(t.Context(), "derpibooru", warm) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("warm batch made %d calls", got)
	}
	t.Logf("200 posts, 201 distinct IDs: single 200 requests in %s, batch 5 requests in %s, warm batch 0 requests", baselineTime, batchTime)
}

func TestDerpibooruBatchIsolatesUnverifiedTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("q") {
		case "id:1 || id:2 || id:3 || id:4":
			fmt.Fprint(w, `{"tags":[
				{"id":1,"name":"good","category":null,"aliased_tag":null,"aliases":[]},
				{"id":2,"name":"bad","category":null},
				{"id":4,"name":"wrong","category":null,"aliased_tag":null,"aliases":[]}
			]}`)
		case "missing":
			fmt.Fprint(w, `{"tags":[{"id":3,"name":"missing","category":null,"aliased_tag":null,"aliases":[]}]}`)
		default:
			t.Errorf("unexpected query %q", r.URL.Query().Get("q"))
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	d, err := newDerpibooru("", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var posts []map[string]any
	for i, name := range []string{"good", "bad", "missing", "expected", "bad"} {
		id := i + 1
		if i == 4 {
			id = 2
		}
		posts = append(posts, map[string]any{"category": "derpibooru", "tags": []string{name}, "tag_ids": []int{id}})
	}
	errs := d.EnrichBatch(t.Context(), "derpibooru", posts)
	for i, want := range []string{"", "no verified alias fields", "", "expected name", "no verified alias fields"} {
		if (errs[i] == nil) != (want == "") || want != "" && !strings.Contains(errs[i].Error(), want) {
			t.Errorf("post %d error = %v, want %q", i, errs[i], want)
		}
	}
}

func TestDerpibooruBatchRetryExhaustionLeavesOtherChunksUsable(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		query := r.URL.Query().Get("q")
		switch {
		case strings.HasPrefix(query, "id:1 || "):
			w.Header().Set("Retry-After", "0")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		case query == "id:51":
			fmt.Fprint(w, `{"tags":[{"id":51,"name":"fixture 51","category":null,"aliased_tag":null,"aliases":[]}]}`)
		default:
			t.Errorf("unexpected fallback query %q", query)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	d, err := newDerpibooru("", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var posts []map[string]any
	for id := 1; id <= 51; id++ {
		posts = append(posts, map[string]any{"category": "derpibooru", "tags": []string{fmt.Sprintf("fixture %d", id)}, "tag_ids": []int{id}})
	}
	ctx, stats := WithRequestStats(t.Context(), 41)
	errs := d.EnrichBatch(ctx, "derpibooru", posts)
	for i, err := range errs[:50] {
		if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
			t.Errorf("post %d error = %v, want HTTP 429", i+1, err)
		}
	}
	if errs[50] != nil {
		t.Fatalf("post in successful chunk failed: %v", errs[50])
	}
	if got := attempts.Load(); got != derpibooruMaxAttempts+1 {
		t.Fatalf("made %d attempts, want %d", got, derpibooruMaxAttempts+1)
	}
	summary := stats.Summary()
	if summary.Attempts != derpibooruMaxAttempts+1 || summary.RateLimited != derpibooruMaxAttempts ||
		summary.Retries != derpibooruMaxAttempts-1 || summary.HTTPTime <= 0 {
		t.Fatalf("unexpected request summary: %+v", summary)
	}
}

func TestDerpibooruBatchWithholdsConflictingNamesAcrossPosts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query := r.URL.Query().Get("q"); query != "id:3" {
			t.Errorf("unexpected query %q", query)
		}
		fmt.Fprint(w, `{"tags":[{"id":3,"name":"verified","category":null,"aliased_tag":null,"aliases":[]}]}`)
	}))
	defer server.Close()
	d, err := newDerpibooru("", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	posts := []map[string]any{
		{"category": "derpibooru", "tags": []string{"shared"}, "tag_ids": []int{1}},
		{"category": "derpibooru", "tags": []string{"shared"}, "tag_ids": []int{2}},
		{"category": "derpibooru", "tags": []string{"shared"}},
		{"category": "derpibooru", "tags": []string{"verified"}, "tag_ids": []int{3}},
	}
	errs := d.EnrichBatch(t.Context(), "derpibooru", posts)
	for i, err := range errs[:3] {
		if err == nil || !strings.Contains(err.Error(), "conflicting Derpibooru tag IDs") {
			t.Errorf("post %d error = %v", i+1, err)
		}
	}
	if errs[3] != nil {
		t.Fatalf("unrelated post failed: %v", errs[3])
	}
}
