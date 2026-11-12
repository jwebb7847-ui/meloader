package taxonomy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monbooru/monloader/internal/shared/logx"
)

// Adapter owns taxonomy normalization for one source category.
//
// Keeping this interface inside taxonomy is deliberate for now: this first
// refactor creates an extension point without changing the wider product model.
// If source adapters eventually own more than taxonomy, we can promote the
// abstraction later as a separately discussed architectural change.
type Adapter interface {
	Enricher
	Warmer

	// Source is the normalized gallery-dl category this adapter owns.
	Source() string
}

// Registry dispatches source metadata to the adapter that owns it.
//
// Unknown sources are deliberately a no-op. Meloader already supports many
// sources that do not need taxonomy enrichment, so adding the registry must
// not turn the taxonomy subsystem into a new requirement.
type Registry struct {
	bySource map[string]Adapter
}

func NewRegistry(
	adapters ...Adapter,
) (*Registry, error) {
	r := &Registry{
		bySource: make(map[string]Adapter),
	}

	for _, adapter := range adapters {
		if adapter == nil {
			return nil, fmt.Errorf(
				"taxonomy adapter is nil",
			)
		}

		source := normalizeAdapterSource(
			adapter.Source(),
		)

		if source == "" {
			return nil, fmt.Errorf(
				"taxonomy adapter has empty source",
			)
		}

		if _, exists := r.bySource[source]; exists {
			return nil, fmt.Errorf(
				"duplicate taxonomy adapter for source %q",
				source,
			)
		}

		r.bySource[source] = adapter
	}

	return r, nil
}

func (r *Registry) Enrich(
	ctx context.Context,
	meta map[string]any,
) error {
	adapter := r.adapterFor(meta)

	if adapter == nil {
		return nil
	}

	started := time.Now()

	err := adapter.Enrich(
		ctx,
		meta,
	)

	logx.Infof(
		"perf: taxonomy stage=enrich source=%s, duration=%s, err=%v",
		adapter.Source(),
		time.Since(started).Round(time.Millisecond),
		err,
	)

	return err
}

func (r *Registry) Warm(
	ctx context.Context,
	meta map[string]any,
) error {
	adapter := r.adapterFor(meta)

	if adapter == nil {
		return nil
	}

	started := time.Now()

	err := adapter.Warm(
		ctx,
		meta,
	)

	logx.Infof(
		"perf: taxonomy stage=warm source=%s, duration=%s, err=%v",
		adapter.Source(),
		time.Since(started).Round(time.Millisecond),
		err,
	)

	return err
}

func (r *Registry) adapterFor(
	meta map[string]any,
) Adapter {
	if r == nil {
		return nil
	}

	source := normalizeAdapterSource(
		stringValue(meta["category"]),
	)

	if source == "" {
		return nil
	}

	return r.bySource[source]
}

func normalizeAdapterSource(
	source string,
) string {
	return strings.ToLower(
		strings.TrimSpace(source),
	)
}

var (
	_ Enricher = (*Registry)(nil)
	_ Warmer   = (*Registry)(nil)
)
