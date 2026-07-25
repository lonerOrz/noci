package gc

import (
	"context"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/oci"
	"sync"
	"time"
)

// Runner orchestrates the complete end-to-end garbage collection workflow:
// fetch index → compute sweep/eviction → apply mutations → push index → optional physical tag pruning.
type Runner struct {
	store         oci.Store
	gracePeriod   time.Duration
	keepVersions  int
	physicalSweep bool
	dryRun        bool
	logger        log.Logger
}

// SetLogger replaces the default stderr logger.
func (r *Runner) SetLogger(l log.Logger) {
	if l == nil {
		r.logger = log.NopLogger{}
	} else {
		r.logger = l
	}
}

func NewRunner(store oci.Store, gracePeriod time.Duration, keepVersions int, physicalSweep, dryRun bool) *Runner {
	return &Runner{
		store:         store,
		gracePeriod:   gracePeriod,
		keepVersions:  keepVersions,
		physicalSweep: physicalSweep,
		dryRun:        dryRun,
		logger:        log.DefaultLogger{},
	}
}

// Run executes the full GC lifecycle. maxSize=0 means no quota limit.
// targets non-empty triggers CascadeEvict; otherwise Sweep runs.
func (r *Runner) Run(ctx context.Context, maxSize int64, targets []string) (*Result, error) {
	index, err := r.store.FetchIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch index: %w", err)
	}

	engine := NewEngine(index, r.gracePeriod)
	engine.SetKeepVersions(r.keepVersions)

	var result *Result
	if len(targets) > 0 {
		r.logger.Action("Targeted eviction resolved to %d input hashes.", len(targets))
		result = engine.CascadeEvict(targets)
	} else {
		result = engine.Sweep(time.Now(), maxSize)
	}

	if result.EvictedCount == 0 {
		return result, nil
	}

	if r.dryRun {
		return result, nil
	}

	engine.Apply(result)

	r.logger.Action("Updating OCI state...")
	if err := r.store.PushIndex(ctx, index); err != nil {
		return nil, fmt.Errorf("failed to push updated index: %w", err)
	}

	if r.physicalSweep && len(result.EvictedKeys) > 0 {
		r.physicalDelete(ctx, result.EvictedKeys)
	}

	return result, nil
}

func (r *Runner) physicalDelete(ctx context.Context, keys []string) {
	r.logger.Action("Physically pruning evicted manifests concurrently (8 workers)...")
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup

	for _, key := range keys {
		sem <- struct{}{}
		wg.Add(1)

		go func(tag string) {
			defer func() {
				<-sem
				wg.Done()
			}()
			r.logger.Action("Pruning physical manifest tag: %s", tag)
			if err := r.store.DeleteManifest(ctx, tag); err != nil {
				r.logger.Warning("Failed to prune physical manifest tag %s: %v", tag, err)
			} else {
				r.logger.Success("Successfully pruned physical manifest tag: %s", tag)
			}
		}(key)
	}
	wg.Wait()
}
