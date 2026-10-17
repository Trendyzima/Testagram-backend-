// Package queueworker implements an at-least-once worker loop over the private
// Testagram platform queue API. Handlers must be idempotent: a crash after a side
// effect but before acknowledgement can redeliver the same job.
package queueworker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/platformclient"
)

const attemptField = "__testagram_attempt"
const stableJobIDField = "__testagram_job_id"

// Client is the queue subset required by Worker. It allows deterministic tests
// without a live Valkey instance.
type Client interface {
	Enqueue(context.Context, string, map[string]string) (string, error)
	Claim(context.Context, string, string, string, int64) ([]platformclient.QueueMessage, error)
	Reclaim(context.Context, string, string, string, time.Duration, int64, string) ([]platformclient.QueueMessage, string, error)
	Ack(context.Context, string, string, string) error
}

type Handler func(context.Context, platformclient.QueueMessage) error

type Config struct {
	Queue       string
	Group       string
	Consumer    string
	BatchSize   int64
	MaxAttempts int
	ReclaimIdle time.Duration
	RetryBackoff time.Duration
	DeadLetterSuffix string
}

type Stats struct {
	Claimed      uint64
	Succeeded    uint64
	Retried      uint64
	DeadLettered uint64
	Failed       uint64
}

type Worker struct {
	client Client
	handle Handler
	config Config
	logger *log.Logger
	cursor string
	claimed atomic.Uint64
	succeeded atomic.Uint64
	retried atomic.Uint64
	deadLettered atomic.Uint64
	failed atomic.Uint64
}

func New(client Client, config Config, handler Handler, logger *log.Logger) (*Worker, error) {
	if client == nil || handler == nil {
		return nil, errors.New("queue client and job handler are required")
	}
	if strings.TrimSpace(config.Queue) == "" || strings.TrimSpace(config.Group) == "" || strings.TrimSpace(config.Consumer) == "" {
		return nil, errors.New("queue, group and consumer are required")
	}
	if config.BatchSize == 0 { config.BatchSize = 10 }
	if config.BatchSize < 1 || config.BatchSize > 100 {
		return nil, errors.New("batch size must be between 1 and 100")
	}
	if config.MaxAttempts == 0 { config.MaxAttempts = 5 }
	if config.MaxAttempts < 1 || config.MaxAttempts > 20 {
		return nil, errors.New("max attempts must be between 1 and 20")
	}
	if config.ReclaimIdle == 0 { config.ReclaimIdle = 30 * time.Second }
	if config.ReclaimIdle < time.Millisecond || config.ReclaimIdle > 24*time.Hour {
		return nil, errors.New("reclaim idle must be between 1ms and 24h")
	}
	if config.RetryBackoff == 0 { config.RetryBackoff = time.Second }
	if config.RetryBackoff < 0 || config.RetryBackoff > time.Minute {
		return nil, errors.New("retry backoff must be between 0 and 1m")
	}
	if config.DeadLetterSuffix == "" { config.DeadLetterSuffix = ".dead" }
	if len(config.Queue)+len(config.DeadLetterSuffix) > 160 {
		return nil, errors.New("queue name plus dead-letter suffix exceeds 160 characters")
	}
	if strings.ContainsAny(config.DeadLetterSuffix, " :/") {
		return nil, errors.New("dead-letter suffix contains invalid characters")
	}
	if logger == nil { logger = log.Default() }
	return &Worker{client: client, handle: handler, config: config, logger: logger, cursor: "0-0"}, nil
}

// Run polls until ctx is cancelled. Queue API failures use a bounded retry delay;
// job failures are moved to a retry entry or the dead-letter queue.
func (w *Worker) Run(ctx context.Context) error {
	for {
		_, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.failed.Add(1)
			w.logger.Printf("queue worker transient error queue=%s: %v", w.config.Queue, err)
			if !sleepContext(ctx, w.config.RetryBackoff) { return ctx.Err() }
		}
		if ctx.Err() != nil { return ctx.Err() }
	}
}

// RunOnce reclaims one page of stale pending jobs before claiming new work.
// It returns the number of messages examined and any transport/acknowledgement
// error. A failed handler does not get acknowledged until retry/dead-letter
// enqueue succeeds.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	// Claim first so the platform can create the consumer group before reclaim
	// is attempted. The group starts at stream ID 0, preserving older messages.
	fresh, err := w.client.Claim(ctx, w.config.Queue, w.config.Group, w.config.Consumer, w.config.BatchSize)
	if err != nil { return 0, fmt.Errorf("claim new jobs: %w", err) }
	pending, next, err := w.client.Reclaim(ctx, w.config.Queue, w.config.Group, w.config.Consumer, w.config.ReclaimIdle, w.config.BatchSize, w.cursor)
	if err != nil { return 0, fmt.Errorf("reclaim pending jobs: %w", err) }
	w.cursor = next
	if w.cursor == "" || w.cursor == "0-0" { w.cursor = "0-0" }
	processed := 0
	for _, message := range pending {
		if err := w.process(ctx, message); err != nil { return processed, err }
		processed++
	}
	for _, message := range fresh {
		if err := w.process(ctx, message); err != nil { return processed, err }
		processed++
	}
	return processed, nil
}

func (w *Worker) process(ctx context.Context, message platformclient.QueueMessage) error {
	w.claimed.Add(1)
	if message.ID == "" { return errors.New("queue delivered a message without an ID") }
	if message.Values == nil { message.Values = map[string]string{} }
	if message.Values[stableJobIDField] == "" { message.Values[stableJobIDField] = message.ID }
	if err := w.handle(ctx, message); err == nil {
		if ackErr := w.client.Ack(ctx, w.config.Queue, w.config.Group, message.ID); ackErr != nil {
			return fmt.Errorf("ack successful job %s (will be redelivered): %w", message.ID, ackErr)
		}
		w.succeeded.Add(1)
		return nil
	} else {
		return w.retryOrDeadLetter(ctx, message, err)
	}
}

func (w *Worker) retryOrDeadLetter(ctx context.Context, message platformclient.QueueMessage, handlerErr error) error {
	values := make(map[string]string, len(message.Values)+1)
	for key, value := range message.Values { values[key] = value }
	attempt, parseErr := strconv.Atoi(values[attemptField])
	if parseErr != nil || attempt < 0 { attempt = 0 }
	attempt++
	values[attemptField] = strconv.Itoa(attempt)
	destination := w.config.Queue
	dead := attempt >= w.config.MaxAttempts
	if dead { destination += w.config.DeadLetterSuffix }
	if !dead && w.config.RetryBackoff > 0 {
		// Bound the pause so one broken handler cannot create a tight retry loop.
		if !sleepContext(ctx, w.config.RetryBackoff) { return ctx.Err() }
	}
	if _, err := w.client.Enqueue(ctx, destination, values); err != nil {
		return fmt.Errorf("enqueue retry for job %s after handler error %v: %w", message.ID, handlerErr, err)
	}
	if err := w.client.Ack(ctx, w.config.Queue, w.config.Group, message.ID); err != nil {
		return fmt.Errorf("ack requeued job %s (duplicate retry is possible): %w", message.ID, err)
	}
	if dead {
		w.deadLettered.Add(1)
		w.logger.Printf("queue job moved to dead-letter queue=%s id=%s attempts=%d error=%v", destination, message.ID, attempt, handlerErr)
	} else {
		w.retried.Add(1)
		w.logger.Printf("queue job scheduled for retry queue=%s id=%s attempt=%d error=%v", destination, message.ID, attempt, handlerErr)
	}
	return nil
}

func (w *Worker) Stats() Stats {
	return Stats{
		Claimed: w.claimed.Load(), Succeeded: w.succeeded.Load(),
		Retried: w.retried.Load(), DeadLettered: w.deadLettered.Load(),
		Failed: w.failed.Load(),
	}
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done(): return false
	case <-timer.C: return true
	}
}
