#!/usr/bin/env bash
set -euo pipefail

container="testagram-notification-engine-ci"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --rm --name "$container" \
  -e POSTGRES_PASSWORD=ci-only-password \
  -p 5432:5432 postgres:17-alpine >/dev/null

for attempt in $(seq 1 30); do
  if docker exec "$container" pg_isready -U postgres >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" -eq 30 ]]; then
    docker logs "$container"
    exit 1
  fi
  sleep 2
done

psql_ci() {
  docker exec -i "$container" psql -U postgres -v ON_ERROR_STOP=1 "$@"
}

psql_ci <<'SQL'
CREATE ROLE testagram_app NOLOGIN;
CREATE ROLE service_role NOLOGIN;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
-- The social moderation migration references the canonical staff principal.
-- Keep this focused schema fixture minimal; identity migrations have their own CI suite.
CREATE SCHEMA testagram_auth;
CREATE TABLE testagram_auth.staff_principals(user_id UUID PRIMARY KEY);
INSERT INTO testagram_auth.staff_principals(user_id)
VALUES ('123e4567-e89b-42d3-a456-426614174204');
SQL

for migration in \
  migrations/20261019_dual_backend_replication.sql \
  migrations/20261019_testagram_local_accounts_posts.sql \
  migrations/20261106_social_core.sql \
  migrations/20261120_social_platform_product_foundation.sql \
  migrations/20261126_unified_notification_engine.sql; do
  psql_ci < "$migration"
done

# Re-apply the notification migration to verify idempotent DDL and grants.
psql_ci < migrations/20261126_unified_notification_engine.sql

psql_ci <<'SQL'
INSERT INTO testagram_local.accounts(user_id,username,display_name) VALUES
  ('123e4567-e89b-42d3-a456-426614174201','notify_sender','Notification sender'),
  ('123e4567-e89b-42d3-a456-426614174202','notify_recipient','Notification recipient');

INSERT INTO testagram_local.posts(id,author_id,text)
VALUES ('123e4567-e89b-42d3-a456-426614174203',
        '123e4567-e89b-42d3-a456-426614174201','Notification migration test post');

INSERT INTO testagram_local.accounts(user_id,username)
SELECT gen_random_uuid(),'notify_actor_' || g FROM generate_series(1,7) AS g;

INSERT INTO testagram_local.notification_events
  (recipient_id,actor_id,event_type,event_key,entity_type,entity_id,payload)
VALUES
  ('123e4567-e89b-42d3-a456-426614174202',
   '123e4567-e89b-42d3-a456-426614174201',
   'social.follow','ci:follow:one','account',
   '123e4567-e89b-42d3-a456-426614174201',
   '{"source":"migration-test"}'::jsonb)
ON CONFLICT(recipient_id,event_key) DO NOTHING;

-- Event replay must be idempotent even if a duplicate carries a different payload.
INSERT INTO testagram_local.notification_events
  (recipient_id,actor_id,event_type,event_key,entity_type,entity_id,payload)
VALUES
  ('123e4567-e89b-42d3-a456-426614174202',
   '123e4567-e89b-42d3-a456-426614174201',
   'social.follow','ci:follow:one','account',
   '123e4567-e89b-42d3-a456-426614174201',
   '{"source":"replayed"}'::jsonb)
ON CONFLICT(recipient_id,event_key) DO NOTHING;

INSERT INTO testagram_local.notification_deliveries(event_id,recipient_id,channel,status)
SELECT id,recipient_id,'in_app','sent'
FROM testagram_local.notification_events
WHERE event_key='ci:follow:one'
ON CONFLICT(event_id,channel) DO NOTHING;

INSERT INTO testagram_local.notification_preferences
  (user_id,push_social_enabled,push_wallet_enabled,push_messages_enabled)
VALUES ('123e4567-e89b-42d3-a456-426614174202',FALSE,TRUE,FALSE);

INSERT INTO testagram_local.push_devices
  (user_id,platform,provider,token_hash,token_ciphertext,token_nonce)
VALUES
  ('123e4567-e89b-42d3-a456-426614174202','android','fcm',
   digest('notification-test-device-token','sha256'),
   decode('01','hex'),decode('02','hex'));

-- Exercise the same 15-minute aggregation upsert used by social reactions.
DO $aggregate$
DECLARE actor RECORD; aggregate_count INTEGER; preview_count INTEGER; latest_actor UUID;
BEGIN
  FOR actor IN
    SELECT user_id FROM testagram_local.accounts
    WHERE username LIKE 'notify_actor_%' ORDER BY username
  LOOP
    INSERT INTO testagram_local.notification_reaction_aggregates(
      post_id,recipient_id,window_started_at,last_reaction_at,reaction_count,latest_actor_id,actor_ids
    ) VALUES (
      '123e4567-e89b-42d3-a456-426614174203',
      '123e4567-e89b-42d3-a456-426614174202',
      date_bin('15 minutes',now(),TIMESTAMPTZ '2000-01-01'),now(),1,actor.user_id,ARRAY[actor.user_id]
    )
    ON CONFLICT(post_id,recipient_id,window_started_at) DO UPDATE SET
      last_reaction_at=now(),
      reaction_count=testagram_local.notification_reaction_aggregates.reaction_count+1,
      latest_actor_id=EXCLUDED.latest_actor_id,
      actor_ids=CASE
        WHEN EXCLUDED.latest_actor_id=ANY(testagram_local.notification_reaction_aggregates.actor_ids)
          OR cardinality(testagram_local.notification_reaction_aggregates.actor_ids)>=5
        THEN testagram_local.notification_reaction_aggregates.actor_ids
        ELSE array_append(testagram_local.notification_reaction_aggregates.actor_ids,EXCLUDED.latest_actor_id)
      END;
  END LOOP;
  SELECT reaction_count,cardinality(actor_ids),latest_actor_id
    INTO aggregate_count,preview_count,latest_actor
  FROM testagram_local.notification_reaction_aggregates
  WHERE post_id='123e4567-e89b-42d3-a456-426614174203'
    AND recipient_id='123e4567-e89b-42d3-a456-426614174202';
  IF aggregate_count<>7 OR preview_count<>5 OR latest_actor IS NULL THEN
    RAISE EXCEPTION 'reaction aggregation preview invariant failed: count %, preview %, latest %',
      aggregate_count,preview_count,latest_actor;
  END IF;
  INSERT INTO testagram_local.notifications(
    recipient_id,actor_id,post_id,kind,payload,event_key,created_at
  ) VALUES (
    '123e4567-e89b-42d3-a456-426614174202',latest_actor,
    '123e4567-e89b-42d3-a456-426614174203','social.reactions.aggregated',
    '{"reaction_count":1}'::jsonb,'ci:reaction:aggregate',now()-interval '1 minute'
  );
  UPDATE testagram_local.notifications
  SET payload=jsonb_build_object('reaction_count',aggregate_count),
      actor_id=latest_actor,created_at=now()
  WHERE recipient_id='123e4567-e89b-42d3-a456-426614174202'
    AND event_key='ci:reaction:aggregate';
  IF NOT EXISTS (
    SELECT 1 FROM testagram_local.notifications
    WHERE event_key='ci:reaction:aggregate'
      AND created_at>now()-interval '30 seconds'
      AND payload->>'reaction_count'='7'
  ) THEN
    RAISE EXCEPTION 'aggregate refresh did not move the inbox row for SSE replay';
  END IF;
END
$aggregate$;

DO $verify$
DECLARE event_count INTEGER; delivery_count INTEGER; payload JSONB;
BEGIN
  SELECT count(*) INTO event_count
  FROM testagram_local.notification_events
  WHERE recipient_id='123e4567-e89b-42d3-a456-426614174202'
    AND event_key='ci:follow:one';
  SELECT e.payload INTO payload
  FROM testagram_local.notification_events e
  WHERE e.recipient_id='123e4567-e89b-42d3-a456-426614174202'
    AND e.event_key='ci:follow:one';
  IF event_count <> 1 THEN
    RAISE EXCEPTION 'notification event replay was not idempotent: % rows',event_count;
  END IF;
  IF payload->>'source' <> 'migration-test' THEN
    RAISE EXCEPTION 'duplicate event overwrote the original immutable payload';
  END IF;
  SELECT count(*) INTO delivery_count
  FROM testagram_local.notification_deliveries d
  JOIN testagram_local.notification_events e ON e.id=d.event_id
  WHERE e.event_key='ci:follow:one' AND d.channel='in_app';
  IF delivery_count <> 1 THEN
    RAISE EXCEPTION 'in-app delivery uniqueness failed: % rows',delivery_count;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM testagram_local.notification_preferences
    WHERE user_id='123e4567-e89b-42d3-a456-426614174202'
      AND NOT push_social_enabled AND push_wallet_enabled AND NOT push_messages_enabled
  ) THEN
    RAISE EXCEPTION 'notification category preferences were not persisted';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM testagram_local.push_devices
    WHERE user_id='123e4567-e89b-42d3-a456-426614174202'
      AND platform='android' AND provider='fcm' AND enabled
  ) THEN
    RAISE EXCEPTION 'encrypted push-device registration row was not persisted';
  END IF;
  IF NOT has_table_privilege('testagram_app','testagram_local.notification_events','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_local.notification_deliveries','INSERT') THEN
    RAISE EXCEPTION 'application role is missing notification table grants';
  END IF;
END
$verify$;

-- Reject non-object payloads and unsupported delivery channels.
DO $constraints$
BEGIN
  BEGIN
    INSERT INTO testagram_local.notification_events(recipient_id,event_type,event_key,payload)
    VALUES ('123e4567-e89b-42d3-a456-426614174202','social.test','ci:bad-payload','[]'::jsonb);
    RAISE EXCEPTION 'notification payload constraint unexpectedly accepted an array';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;
  BEGIN
    INSERT INTO testagram_local.notification_deliveries(event_id,recipient_id,channel,status)
    SELECT id,recipient_id,'sms','pending'
    FROM testagram_local.notification_events WHERE event_key='ci:follow:one';
    RAISE EXCEPTION 'notification channel constraint unexpectedly accepted sms';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;
END
$constraints$;
SQL

echo "Unified notification migration, replay safety, grants, preferences and delivery constraints passed."
