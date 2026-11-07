package federationoutbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/federationclient"
)

type Repository interface {
	Claim(context.Context, int, time.Duration) ([]Item, error)
	Ack(context.Context, string, string) error
	Fail(context.Context, string, string, string) error
}

type Publisher interface {
	Publish(context.Context, federationclient.PublishRequest) (federationclient.PublishResult, error)
}

type Worker struct {
	repository Repository
	publisher Publisher
	logger *slog.Logger
	interval time.Duration
	batch int
}

func NewWorker(repository Repository, publisher Publisher, logger *slog.Logger) (*Worker, error) {
	if repository == nil || publisher == nil {
		return nil, errors.New("Federation outbox worker requires repository and publisher")
	}
	if logger == nil { logger = slog.Default() }
	return &Worker{repository: repository, publisher: publisher, logger: logger, interval: 3*time.Second, batch: 25}, nil
}

func (w *Worker) ProcessBatch(ctx context.Context) (int, error) {
	items, err := w.repository.Claim(ctx, w.batch, 45*time.Second)
	if err != nil { return 0, err }
	var firstErr error
	for _, item := range items {
		_, publishErr := w.publisher.Publish(ctx, federationclient.PublishRequest{
			ActivityID: item.ActivityID,
			ActorUserID: item.ActorUserID,
			Activity: item.Payload,
		})
		if publishErr == nil {
			if err := w.repository.Ack(ctx, item.ActivityID, item.LeaseToken); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := w.repository.Fail(ctx, item.ActivityID, item.LeaseToken, publishErr.Error()); err != nil {
			if firstErr == nil { firstErr = err }
			w.logger.Error("Federation outbox failure could not be recorded", "activity_id", item.ActivityID, "error", err)
			continue
		}
		if firstErr == nil { firstErr = publishErr }
	}
	return len(items), firstErr
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		processed, err := w.ProcessBatch(batchCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			w.logger.Warn("Federation outbox batch failed", "error", err)
		} else if processed > 0 {
			w.logger.Info("Federation outbox batch processed", "count", processed)
		}
		select {
		case <-ctx.Done(): return
		case <-ticker.C:
		}
	}
}
