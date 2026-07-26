package cmd

import (
	"context"
	"sync"
)

type workerResult[T any] struct {
	Value T
	Err   error
}

// runConcurrent processes items concurrently with up to concurrency workers.
// Returns results in the same order as items.
func runConcurrent[T any](ctx context.Context, items []T, concurrency int, fn func(ctx context.Context, item T) error) []error {
	results := make([]error, len(items))
	if len(items) == 0 {
		return results
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, item := range items {
		select {
		case <-ctx.Done():
			break
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(idx int, val T) {
			defer func() {
				<-sem
				wg.Done()
			}()
			results[idx] = fn(ctx, val)
		}(i, item)
	}

	wg.Wait()
	return results
}
