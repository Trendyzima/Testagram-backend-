package queueworker

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/platformclient"
)

type fakeQueue struct {
	queued map[string][]platformclient.QueueMessage
	acked []string
	next int
}

func newFakeQueue() *fakeQueue { return &fakeQueue{queued: map[string][]platformclient.QueueMessage{}} }

func (f *fakeQueue) Enqueue(_ context.Context, queue string, values map[string]string) (string, error) {
	f.next++
	id := "retry-" + time.Duration(f.next).String()
	copyValues := map[string]string{}
	for k, v := range values { copyValues[k] = v }
	f.queued[queue] = append(f.queued[queue], platformclient.QueueMessage{ID: id, Values: copyValues})
	return id, nil
}
func (f *fakeQueue) Claim(_ context.Context, queue, _, _ string, count int64) ([]platformclient.QueueMessage, error) {
	messages := f.queued[queue]
	if len(messages) == 0 { return nil, nil }
	if int64(len(messages)) > count { messages = messages[:count] }
	f.queued[queue] = f.queued[queue][len(messages):]
	return messages, nil
}
func (f *fakeQueue) Reclaim(_ context.Context, _, _, _ string, _ time.Duration, _ int64, start string) ([]platformclient.QueueMessage, string, error) {
	return nil, "0-0", nil
}
func (f *fakeQueue) Ack(_ context.Context, _, _, id string) error {
	f.acked = append(f.acked, id)
	return nil
}

func testWorker(t *testing.T, client Client, config Config, handler Handler) *Worker {
	t.Helper()
	config.RetryBackoff = time.Millisecond
	worker, err := New(client, config, handler, log.New(io.Discard, "", 0))
	if err != nil { t.Fatal(err) }
	return worker
}

func TestWorkerAcknowledgesOnlyAfterSuccessfulHandling(t *testing.T) {
	client := newFakeQueue()
	client.queued["mail"] = []platformclient.QueueMessage{{ID: "job-1", Values: map[string]string{"job_id": "mail-1"}}}
	worker := testWorker(t, client, Config{Queue: "mail", Group: "sender", Consumer: "worker-1"}, func(_ context.Context, msg platformclient.QueueMessage) error {
		if msg.Values["job_id"] != "mail-1" || msg.Values[stableJobIDField] != "job-1" { t.Fatalf("unexpected job: %+v", msg) }
		return nil
	})
	count, err := worker.RunOnce(context.Background())
	if err != nil { t.Fatal(err) }
	if count != 1 || len(client.acked) != 1 || client.acked[0] != "job-1" {
		t.Fatalf("count=%d acked=%v", count, client.acked)
	}
	stats := worker.Stats()
	if stats.Claimed != 1 || stats.Succeeded != 1 || stats.Retried != 0 { t.Fatalf("unexpected stats: %+v", stats) }
}

func TestWorkerBoundsRetriesAndDeadLettersPoisonJobs(t *testing.T) {
	client := newFakeQueue()
	client.queued["federation"] = []platformclient.QueueMessage{{ID: "job-1", Values: map[string]string{"job_id": "activity-1"}}}
	worker := testWorker(t, client, Config{Queue: "federation", Group: "delivery", Consumer: "worker-1", MaxAttempts: 2}, func(context.Context, platformclient.QueueMessage) error {
		return errors.New("remote unavailable")
	})
	if _, err := worker.RunOnce(context.Background()); err != nil { t.Fatal(err) }
	retry := client.queued["federation"]
	if len(retry) != 1 || retry[0].Values[attemptField] != "1" || retry[0].Values[stableJobIDField] != "job-1" { t.Fatalf("retry not persisted with stable identity: %+v", retry) }
	if _, err := worker.RunOnce(context.Background()); err != nil { t.Fatal(err) }
	dead := client.queued["federation.dead"]
	if len(dead) != 1 || dead[0].Values[attemptField] != "2" || dead[0].Values["job_id"] != "activity-1" || dead[0].Values[stableJobIDField] != "job-1" {
		t.Fatalf("job not moved to dead-letter queue: %+v", dead)
	}
	if len(client.acked) != 2 { t.Fatalf("expected original messages acked only after retry/dead-letter enqueue, got %v", client.acked) }
	stats := worker.Stats()
	if stats.Retried != 1 || stats.DeadLettered != 1 { t.Fatalf("unexpected stats: %+v", stats) }
}

func TestNewValidatesConfiguration(t *testing.T) {
	if _, err := New(nil, Config{Queue: "q", Group: "g", Consumer: "c"}, func(context.Context, platformclient.QueueMessage) error { return nil }, nil); err == nil {
		t.Fatal("expected nil client to be rejected")
	}
	if _, err := New(newFakeQueue(), Config{Queue: "q", Group: "g", Consumer: "c", MaxAttempts: 21}, func(context.Context, platformclient.QueueMessage) error { return nil }, nil); err == nil {
		t.Fatal("expected max attempts bound to be enforced")
	}
}
