package taxonomy

import (
	"context"
	"sync/atomic"
	"time"
)

type requestStatsKey struct{}

// RequestStats records the source work performed for one preflight. Its
// counters stay separate when multiple queue jobs use the same resolver.
type RequestStats struct {
	job         int64
	attempts    atomic.Int64
	rateLimited atomic.Int64
	httpTime    atomic.Int64
	slotWait    atomic.Int64
	retries     atomic.Int64
	retryWait   atomic.Int64
}

// WithRequestStats scopes taxonomy diagnostics to a single queue job.
func WithRequestStats(ctx context.Context, job int64) (context.Context, *RequestStats) {
	stats := &RequestStats{job: job}
	return context.WithValue(ctx, requestStatsKey{}, stats), stats
}

type RequestSummary struct {
	Attempts, RateLimited, Retries int64
	HTTPTime, SlotWait, RetryWait  time.Duration
}

func (s *RequestStats) Summary() RequestSummary {
	return RequestSummary{
		Attempts: s.attempts.Load(), RateLimited: s.rateLimited.Load(), Retries: s.retries.Load(),
		HTTPTime: time.Duration(s.httpTime.Load()), SlotWait: time.Duration(s.slotWait.Load()),
		RetryWait: time.Duration(s.retryWait.Load()),
	}
}
