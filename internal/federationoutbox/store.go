package federationoutbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxPayloadBytes = 1 << 20
const maxAttempts = 12

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Item struct {
	ActivityID string
	ActorUserID string
	Payload json.RawMessage
	Attempts int
	LeaseToken string
}

type Store struct { db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, errors.New("Federation outbox requires PostgreSQL pool")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var exists bool
	if err := db.QueryRow(ctx, "SELECT to_regclass('testagram_local.federation_outbox') IS NOT NULL").Scan(&exists); err != nil {
		return nil, fmt.Errorf("verify Federation outbox migration: %w", err)
	}
	if !exists {
		return nil, errors.New("Federation outbox migration is not applied")
	}
	return &Store{db: db}, nil
}

// EnqueueTx must be called with the same transaction that commits the local
// post/reaction mutation. This guarantees the product write and protocol intent
// either both commit or both roll back.
func (s *Store) EnqueueTx(ctx context.Context, tx pgx.Tx, actorUserID, activityID string, payload []byte) error {
	actorUserID = strings.TrimSpace(actorUserID)
	activityID = strings.TrimSpace(activityID)
	if tx == nil || !uuidPattern.MatchString(actorUserID) || activityID == "" ||
		len(activityID) > 2048 || len(payload) == 0 || len(payload) > maxPayloadBytes || !json.Valid(payload) {
		return errors.New("invalid transactional Federation outbox event")
	}
	u, err := url.Parse(activityID)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("Federation activity ID must be an absolute HTTPS URL")
	}
	var activity struct {
		ID string `json:"id"`
		Type string `json:"type"`
		Actor string `json:"actor"`
	}
	if err := json.Unmarshal(payload, &activity); err != nil || activity.ID != activityID || activity.Actor == "" {
		return errors.New("Federation payload ID and actor are required and must match the outbox event")
	}
	switch activity.Type {
	case "Create", "Update", "Delete", "Like", "Announce":
	default:
		return errors.New("unsupported ActivityPub event type")
	}
	result, err := tx.Exec(ctx, `INSERT INTO testagram_local.federation_outbox
		(activity_id, actor_user_id, payload) VALUES ($1,$2,$3::jsonb)
		ON CONFLICT (activity_id) DO UPDATE SET activity_id=EXCLUDED.activity_id
		WHERE testagram_local.federation_outbox.actor_user_id=EXCLUDED.actor_user_id
		  AND testagram_local.federation_outbox.payload=EXCLUDED.payload`,
		activityID, actorUserID, string(payload))
	if err != nil { return fmt.Errorf("insert Federation outbox event: %w", err) }
	if result.RowsAffected() != 1 {
		return errors.New("Federation activity ID already exists with a different actor or payload")
	}
	return nil
}

func (s *Store) Claim(ctx context.Context, limit int, lease time.Duration) ([]Item, error) {
	if limit < 1 || limit > 100 || lease < 15*time.Second || lease > 5*time.Minute {
		return nil, errors.New("invalid Federation outbox claim bounds")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil { return nil, err }
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `SELECT activity_id, actor_user_id::text, payload, attempts
		FROM testagram_local.federation_outbox
		WHERE attempts < $1 AND (
			(state IN ('pending','retry') AND available_at<=NOW()) OR
			(state='dispatching' AND lease_until<NOW())
		)
		ORDER BY available_at, created_at
		FOR UPDATE SKIP LOCKED LIMIT $2`, maxAttempts, limit)
	if err != nil { return nil, err }
	items := make([]Item, 0, limit)
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ActivityID, &item.ActorUserID, &item.Payload, &item.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil { rows.Close(); return nil, err }
	rows.Close()
	for i := range items {
		var raw [24]byte
		if _, err := rand.Read(raw[:]); err != nil { return nil, err }
		items[i].LeaseToken = hex.EncodeToString(raw[:])
		if _, err := tx.Exec(ctx, `UPDATE testagram_local.federation_outbox
			SET state='dispatching', lease_token=$2, lease_until=NOW()+($3 * INTERVAL '1 second')
			WHERE activity_id=$1`, items[i].ActivityID, items[i].LeaseToken, int(lease.Seconds())); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil { return nil, err }
	return items, nil
}

func (s *Store) Ack(ctx context.Context, activityID, leaseToken string) error {
	tag, err := s.db.Exec(ctx, `UPDATE testagram_local.federation_outbox
		SET state='published', published_at=NOW(), lease_token=NULL, lease_until=NULL, last_error=''
		WHERE activity_id=$1 AND state='dispatching' AND lease_token=$2`, activityID, leaseToken)
	if err != nil { return err }
	if tag.RowsAffected() != 1 { return errors.New("Federation outbox lease is no longer owned") }
	return nil
}

func (s *Store) Fail(ctx context.Context, activityID, leaseToken, reason string) error {
	reason = strings.TrimSpace(reason)
	if len(reason) > 1000 { reason = reason[:1000] }
	tag, err := s.db.Exec(ctx, `UPDATE testagram_local.federation_outbox SET
		attempts=attempts+1,
		state=CASE WHEN attempts+1 >= $3 THEN 'dead_letter' ELSE 'retry' END,
		available_at=CASE WHEN attempts+1 >= $3 THEN available_at
			ELSE NOW() + (LEAST(1 << LEAST(attempts,10),3600) * INTERVAL '1 second') END,
		lease_token=NULL, lease_until=NULL, last_error=$4
		WHERE activity_id=$1 AND state='dispatching' AND lease_token=$2`,
		activityID, leaseToken, maxAttempts, reason)
	if err != nil { return err }
	if tag.RowsAffected() != 1 { return errors.New("Federation outbox lease is no longer owned") }
	return nil
}
