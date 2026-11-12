package taxonomy

import (
	"context"
	"errors"
	"testing"
)

type fakeAdapter struct {
	source string

	enrichCalls int
	warmCalls   int

	enrichErr error
	warmErr   error
}

func (a *fakeAdapter) Source() string {
	return a.source
}

func (a *fakeAdapter) Enrich(
	context.Context,
	map[string]any,
) error {
	a.enrichCalls++
	return a.enrichErr
}

func (a *fakeAdapter) Warm(
	context.Context,
	map[string]any,
) error {
	a.warmCalls++
	return a.warmErr
}

func TestRegistryRoutesBySource(t *testing.T) {
	derpi := &fakeAdapter{
		source: "derpibooru",
	}

	other := &fakeAdapter{
		source: "examplebooru",
	}

	registry, err := NewRegistry(
		derpi,
		other,
	)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": " DerpiBooru ",
	}

	if err := registry.Warm(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if err := registry.Enrich(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if derpi.warmCalls != 1 {
		t.Fatalf(
			"Derpibooru warm calls = %d; want 1",
			derpi.warmCalls,
		)
	}

	if derpi.enrichCalls != 1 {
		t.Fatalf(
			"Derpibooru enrich calls = %d; want 1",
			derpi.enrichCalls,
		)
	}

	if other.warmCalls != 0 ||
		other.enrichCalls != 0 {
		t.Fatalf(
			"unmatched adapter was called: warm=%d enrich=%d",
			other.warmCalls,
			other.enrichCalls,
		)
	}
}

func TestRegistryUnknownSourceIsNoOp(t *testing.T) {
	adapter := &fakeAdapter{
		source: "derpibooru",
	}

	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": "some-other-site",
	}

	if err := registry.Warm(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if err := registry.Enrich(
		context.Background(),
		meta,
	); err != nil {
		t.Fatal(err)
	}

	if adapter.warmCalls != 0 ||
		adapter.enrichCalls != 0 {
		t.Fatalf(
			"adapter called for unknown source: warm=%d enrich=%d",
			adapter.warmCalls,
			adapter.enrichCalls,
		)
	}
}

func TestRegistryPropagatesAdapterErrors(t *testing.T) {
	wantWarm := errors.New("warm failed")
	wantEnrich := errors.New("enrich failed")

	adapter := &fakeAdapter{
		source:    "derpibooru",
		warmErr:   wantWarm,
		enrichErr: wantEnrich,
	}

	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"category": "derpibooru",
	}

	if err := registry.Warm(
		context.Background(),
		meta,
	); !errors.Is(err, wantWarm) {
		t.Fatalf(
			"Warm error = %v; want %v",
			err,
			wantWarm,
		)
	}

	if err := registry.Enrich(
		context.Background(),
		meta,
	); !errors.Is(err, wantEnrich) {
		t.Fatalf(
			"Enrich error = %v; want %v",
			err,
			wantEnrich,
		)
	}
}

func TestRegistryRejectsDuplicateSource(t *testing.T) {
	_, err := NewRegistry(
		&fakeAdapter{
			source: "derpibooru",
		},
		&fakeAdapter{
			source: " DerpiBooru ",
		},
	)

	if err == nil {
		t.Fatal(
			"duplicate source registration succeeded",
		)
	}
}

func TestRegistryRejectsEmptySource(t *testing.T) {
	_, err := NewRegistry(
		&fakeAdapter{
			source: " ",
		},
	)

	if err == nil {
		t.Fatal(
			"empty source registration succeeded",
		)
	}
}
