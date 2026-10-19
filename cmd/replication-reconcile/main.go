package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type sourceEntity struct {
	Origin string `json:"origin"`
	Type string `json:"entity_type"`
	ID string `json:"entity_id"`
	Version int64 `json:"entity_version"`
	EventType string `json:"event_type"`
	EventID string `json:"event_id"`
	Payload json.RawMessage `json:"payload"`
	Delivered bool `json:"delivered"`
}

type mismatch struct {
	EntityType string `json:"entity_type"`
	EntityID string `json:"entity_id"`
	SourceVersion int64 `json:"source_version"`
	DestinationVersion int64 `json:"destination_version"`
	Reason string `json:"reason"`
}

type report struct {
	Origin string `json:"origin"`
	Checked int `json:"entities_checked"`
	Mismatches []mismatch `json:"mismatches"`
	OutboxPending int64 `json:"outbox_pending"`
	OutboxDeadLetters int64 `json:"outbox_dead_letters"`
	InboxPending int64 `json:"destination_inbox_pending"`
	InboxRejected int64 `json:"destination_inbox_rejected"`
	InboxStale int64 `json:"destination_inbox_stale"`
	Healthy bool `json:"healthy"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	sourceURL := strings.TrimSpace(os.Getenv("SOURCE_DATABASE_URL"))
	destinationURL := strings.TrimSpace(os.Getenv("DESTINATION_DATABASE_URL"))
	origin := strings.TrimSpace(os.Getenv("RECONCILE_ORIGIN"))
	if origin == "" { origin = "testagram-vps" }
	if sourceURL == "" || destinationURL == "" {
		log.Fatal("SOURCE_DATABASE_URL and DESTINATION_DATABASE_URL are required")
	}
	source, err := pgxpool.New(ctx, sourceURL)
	if err != nil { log.Fatalf("connect source database: %v", err) }
	defer source.Close()
	destination, err := pgxpool.New(ctx, destinationURL)
	if err != nil { log.Fatalf("connect destination database: %v", err) }
	defer destination.Close()
	if err := source.Ping(ctx); err != nil { log.Fatalf("source database unavailable: %v", err) }
	if err := destination.Ping(ctx); err != nil { log.Fatalf("destination database unavailable: %v", err) }

	out := report{Origin: origin, Mismatches: []mismatch{}}
	if err := source.QueryRow(ctx, "SELECT count(*) FILTER (WHERE delivered_at IS NULL AND failed_at IS NULL), count(*) FILTER (WHERE failed_at IS NOT NULL) FROM public.platform_replication_outbox").Scan(&out.OutboxPending, &out.OutboxDeadLetters); err != nil {
		log.Fatalf("read source outbox health: %v", err)
	}
	if err := destination.QueryRow(ctx, "SELECT count(*) FILTER (WHERE applied_at IS NULL AND outcome IN ('received','retry')), count(*) FILTER (WHERE outcome='rejected'), count(*) FILTER (WHERE outcome='stale') FROM public.platform_replication_inbox").Scan(&out.InboxPending, &out.InboxRejected, &out.InboxStale); err != nil {
		log.Fatalf("read destination inbox health: %v", err)
	}

	rows, err := source.Query(ctx, `
		SELECT DISTINCT ON (origin, entity_type, entity_id)
		       origin, entity_type, entity_id, entity_version, event_type, event_id::text, payload,
		       delivered_at IS NOT NULL
		FROM public.platform_replication_outbox
		WHERE origin=$1
		ORDER BY origin, entity_type, entity_id, entity_version DESC, created_at DESC
	`, origin)
	if err != nil { log.Fatalf("read latest source entities: %v", err) }
	entities := make([]sourceEntity, 0, 256)
	for rows.Next() {
		var item sourceEntity
		if err := rows.Scan(&item.Origin,&item.Type,&item.ID,&item.Version,&item.EventType,&item.EventID,&item.Payload,&item.Delivered); err != nil {
			rows.Close(); log.Fatalf("scan source entity: %v", err)
		}
		entities = append(entities,item)
	}
	if err := rows.Err(); err != nil { rows.Close(); log.Fatalf("iterate source entities: %v", err) }
	rows.Close()

	for _, item := range entities {
		out.Checked++
		var version int64
		var tombstoned bool
		var payload []byte
		err := destination.QueryRow(ctx, `
			SELECT entity_version,tombstoned,payload
			FROM public.platform_replication_projection
			WHERE origin=$1 AND entity_type=$2 AND entity_id=$3
		`, item.Origin,item.Type,item.ID).Scan(&version,&tombstoned,&payload)
		if err != nil {
			out.Mismatches=append(out.Mismatches,mismatch{item.Type,item.ID,item.Version,0,"destination_projection_missing"})
			continue
		}
		if version < item.Version {
			out.Mismatches=append(out.Mismatches,mismatch{item.Type,item.ID,item.Version,version,"destination_version_behind"})
			continue
		}
		if version > item.Version {
			out.Mismatches=append(out.Mismatches,mismatch{item.Type,item.ID,item.Version,version,"destination_version_ahead"})
			continue
		}
		wantTombstone := strings.HasSuffix(item.EventType,".deleted")
		if tombstoned != wantTombstone {
			out.Mismatches=append(out.Mismatches,mismatch{item.Type,item.ID,item.Version,version,"tombstone_state_mismatch"})
			continue
		}
		// Compare canonical payload digests only for live projections. Tombstone
		// payloads are intentionally normalized to an empty object by projectors.
		if !wantTombstone {
			var sourceJSON, destinationJSON any
			if json.Unmarshal(item.Payload,&sourceJSON)==nil && json.Unmarshal(payload,&destinationJSON)==nil {
				a,_:=json.Marshal(sourceJSON); b,_:=json.Marshal(destinationJSON)
				ha,hb:=sha256.Sum256(a),sha256.Sum256(b)
				if hex.EncodeToString(ha[:])!=hex.EncodeToString(hb[:]) {
					out.Mismatches=append(out.Mismatches,mismatch{item.Type,item.ID,item.Version,version,"payload_digest_mismatch"})
				}
			}
		}
		if !item.Delivered {
			out.Mismatches=append(out.Mismatches,mismatch{item.Type,item.ID,item.Version,version,"latest_source_event_not_acknowledged"})
		}
	}
	out.Healthy=len(out.Mismatches)==0 && out.OutboxPending==0 && out.OutboxDeadLetters==0 && out.InboxPending==0 && out.InboxRejected==0
	enc:=json.NewEncoder(os.Stdout); enc.SetIndent("","  "); _=enc.Encode(out)
	if !out.Healthy { os.Exit(2) }
}
