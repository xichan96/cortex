package mem

import (
	"context"
	"log/slog"
	"time"
)

func RunIngestLoop(ctx context.Context, opts IngestLoopOptions) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.WaitReady != nil {
		if err := opts.WaitReady(ctx); err != nil {
			return
		}
	}
	if opts.StartParams == nil || opts.TickParams == nil || opts.NewLLM == nil {
		return
	}
	dir, sqliteFile, interval, err := opts.StartParams()
	if err != nil {
		log.Warn("memory_ingest: start params", "error", err)
		return
	}
	if interval < 5*time.Second {
		interval = 2 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			enabled, batch, minNew, rt := opts.TickParams()
			if !enabled {
				continue
			}
			if batch <= 0 {
				batch = 50
			}
			if minNew <= 0 {
				minNew = 2
			}
			runIngestOnce(ctx, log, opts.NewLLM, dir, sqliteFile, batch, minNew, rt)
		}
	}
}
