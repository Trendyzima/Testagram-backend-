package federationoutbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/federationclient"
)

type fakeRepository struct {
	items []Item
	acked []string
	failed []string
	claimErr error
}
func (f *fakeRepository) Claim(context.Context, int, time.Duration) ([]Item, error) {
	return f.items, f.claimErr
}
func (f *fakeRepository) Ack(_ context.Context, id, _ string) error {
	f.acked = append(f.acked, id)
	return nil
}
func (f *fakeRepository) Fail(_ context.Context, id, _, _ string) error {
	f.failed = append(f.failed, id)
	return nil
}

type fakePublisher struct { err error; calls int }
func (f *fakePublisher) Publish(_ context.Context, input federationclient.PublishRequest) (federationclient.PublishResult, error) {
	f.calls++
	if f.err != nil { return federationclient.PublishResult{}, f.err }
	return federationclient.PublishResult{ActivityID: input.ActivityID, Accepted: true}, nil
}

func TestWorkerAcknowledgesOnlyAcceptedActivities(t *testing.T) {
	repo := &fakeRepository{items: []Item{{ActivityID: "https://testagram.site/activities/1", ActorUserID: "account", Payload: []byte(`{"id":"1"}`), LeaseToken: "lease"}}}
	publisher := &fakePublisher{}
	worker, err := NewWorker(repo, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil { t.Fatal(err) }
	count, err := worker.ProcessBatch(context.Background())
	if err != nil || count != 1 { t.Fatalf("ProcessBatch() = %d, %v", count, err) }
	if publisher.calls != 1 || len(repo.acked) != 1 || repo.acked[0] != repo.items[0].ActivityID || len(repo.failed) != 0 {
		t.Fatalf("unexpected delivery state: calls=%d acked=%v failed=%v", publisher.calls, repo.acked, repo.failed)
	}
}

func TestWorkerRecordsPublishFailureForRetry(t *testing.T) {
	repo := &fakeRepository{items: []Item{{ActivityID: "https://testagram.site/activities/2", ActorUserID: "account", Payload: []byte(`{"id":"2"}`), LeaseToken: "lease"}}}
	publisher := &fakePublisher{err: errors.New("temporary federation outage")}
	worker, err := NewWorker(repo, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil { t.Fatal(err) }
	count, err := worker.ProcessBatch(context.Background())
	if count != 1 || err == nil { t.Fatalf("expected recorded publish failure, got %d, %v", count, err) }
	if len(repo.failed) != 1 || len(repo.acked) != 0 {
		t.Fatalf("failed event must be retried, not acknowledged: acked=%v failed=%v", repo.acked, repo.failed)
	}
}
