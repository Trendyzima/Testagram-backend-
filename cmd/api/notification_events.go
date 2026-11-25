package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// notificationEvent is the durable, channel-neutral description of one user-visible
// business event. Call publishNotificationEvent with the business transaction itself.
type notificationEvent struct {
	RecipientID string
	ActorID     string
	Type        string
	Key         string
	EntityType  string
	EntityID    string
	Payload     map[string]any
}

// publishNotificationEvent writes the immutable event and its in-app/push delivery
// records atomically with the originating business operation. A duplicate key is a
// successful no-op, making callback and request replay safe.
func publishNotificationEvent(ctx context.Context, tx pgx.Tx, event notificationEvent) error {
	if event.RecipientID == "" || event.Type == "" || event.Key == "" {
		return fmt.Errorf("notification recipient, type and event key are required")
	}
	payload := event.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var eventID string
	err = tx.QueryRow(ctx, `
		INSERT INTO testagram_local.notification_events(
			recipient_id,actor_id,event_type,event_key,entity_type,entity_id,payload
		) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7::jsonb)
		ON CONFLICT(recipient_id,event_key) DO NOTHING
		RETURNING id::text
	`, event.RecipientID, nullableNotificationActor(event.ActorID), event.Type,
		event.Key, event.EntityType, event.EntityID, string(raw)).Scan(&eventID)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	// The existing inbox remains the in-app read model. The event ledger is now
	// the deduplication and dispatch source of truth.
	if _, err = tx.Exec(ctx, `
		INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,payload,event_key)
		VALUES($1,$2,$3,$4::jsonb,$5)
		ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING
	`, event.RecipientID, nullableNotificationActor(event.ActorID), event.Type, string(raw), event.Key); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO testagram_local.notification_deliveries(event_id,recipient_id,channel,status)
		VALUES($1,$2,'in_app','sent')
		ON CONFLICT(event_id,channel) DO NOTHING
	`, eventID, event.RecipientID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_local.notification_deliveries(event_id,recipient_id,channel,status)
		SELECT $1,$2,'push','pending'
		WHERE EXISTS (
			SELECT 1 FROM testagram_local.push_devices d
			WHERE d.user_id=$2 AND d.enabled=TRUE
		)
		AND COALESCE((
			SELECT CASE WHEN $3 LIKE 'wallet.%' THEN p.push_wallet_enabled
			            WHEN $3 LIKE 'message.%' THEN p.push_messages_enabled
			            ELSE p.push_social_enabled END
			FROM testagram_local.notification_preferences p WHERE p.user_id=$2
		),TRUE)
		ON CONFLICT(event_id,channel) DO NOTHING
	`, eventID, event.RecipientID, event.Type)
	return err
}

func nullableNotificationActor(actorID string) any {
	if actorID == "" {
		return nil
	}
	return actorID
}
