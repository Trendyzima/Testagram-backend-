package platformclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// QueueMessage is a single at-least-once message delivered by the platform queue.
// Values are string fields because the platform stores Redis Stream fields as strings.
type QueueMessage struct {
	ID     string            `json:"id"`
	Values map[string]string `json:"values"`
}

type queueMessagesResponse struct {
	Messages []QueueMessage `json:"messages"`
	NextStart string         `json:"next_start,omitempty"`
}

// Claim reads new messages for a consumer group. Counts outside the server's
// supported range are rejected locally instead of silently changing semantics.
func (c *Client) Claim(ctx context.Context, queue, group, consumer string, count int64) ([]QueueMessage, error) {
	if queue == "" || group == "" || consumer == "" {
		return nil, errors.New("queue, group and consumer are required")
	}
	if count < 1 || count > 100 {
		return nil, errors.New("queue claim count must be between 1 and 100")
	}
	var result queueMessagesResponse
	err := c.do(ctx, http.MethodPost, "/v1/queue/claim", map[string]any{
		"queue": queue, "group": group, "consumer": consumer, "count": count,
	}, &result)
	return result.Messages, err
}

// Reclaim returns pending messages that have been idle long enough to indicate
// that their previous consumer may have crashed. Re-delivery is expected.
func (c *Client) Reclaim(ctx context.Context, queue, group, consumer string, minIdle time.Duration, count int64) ([]QueueMessage, string, error) {
	if queue == "" || group == "" || consumer == "" {
		return nil, "", errors.New("queue, group and consumer are required")
	}
	idleMS := minIdle.Milliseconds()
	if idleMS < 1 || idleMS > 86400000 {
		return nil, "", errors.New("queue reclaim idle time must be between 1ms and 24h")
	}
	if count < 1 || count > 100 {
		return nil, "", errors.New("queue reclaim count must be between 1 and 100")
	}
	q := url.Values{}
	q.Set("queue", queue)
	q.Set("group", group)
	q.Set("consumer", consumer)
	q.Set("min_idle_ms", strconv.FormatInt(idleMS, 10))
	q.Set("count", strconv.FormatInt(count, 10))
	var result queueMessagesResponse
	err := c.do(ctx, http.MethodGet, "/v1/queue/reclaim?"+q.Encode(), nil, &result)
	return result.Messages, result.NextStart, err
}

// Ack removes a successfully processed message from the group's pending list.
// Call only after the business side effect has completed successfully.
func (c *Client) Ack(ctx context.Context, queue, group, id string) error {
	if queue == "" || group == "" || id == "" {
		return errors.New("queue, group and message ID are required")
	}
	q := url.Values{}
	q.Set("queue", queue)
	q.Set("group", group)
	q.Set("id", id)
	return c.do(ctx, http.MethodPost, "/v1/queue/ack?"+q.Encode(), nil, nil)
}
