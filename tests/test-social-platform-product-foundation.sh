#!/usr/bin/env bash
set -euo pipefail
: "${DATABASE_URL:?DATABASE_URL is required}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE testagram_app NOLOGIN;
CREATE ROLE service_role NOLOGIN;
CREATE SCHEMA testagram_local;
CREATE SCHEMA testagram_auth;
CREATE TABLE testagram_local.accounts (user_id uuid PRIMARY KEY);
CREATE TABLE testagram_local.posts (id uuid PRIMARY KEY);
CREATE TABLE testagram_auth.staff_principals (user_id uuid PRIMARY KEY);
SQL
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$root/migrations/20261120_social_platform_product_foundation.sql"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
DO $verify$
DECLARE
  required_table text;
BEGIN
  FOREACH required_table IN ARRAY ARRAY[
    'social_lists','social_list_members','social_list_posts','post_drafts',
    'scheduled_posts','stories','story_views','story_reactions',
    'social_reports','social_moderation_actions','social_moderation_appeals'
  ] LOOP
    IF to_regclass('testagram_local.' || required_table) IS NULL THEN
      RAISE EXCEPTION 'missing product foundation table: %', required_table;
    END IF;
  END LOOP;
  IF NOT has_table_privilege('testagram_app','testagram_local.stories','SELECT')
     OR NOT has_table_privilege('service_role','testagram_local.scheduled_posts','INSERT') THEN
    RAISE EXCEPTION 'Backend runtime grants are incomplete';
  END IF;
  IF has_table_privilege('public','testagram_local.stories','SELECT') THEN
    RAISE EXCEPTION 'PUBLIC unexpectedly has access to story records';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname='testagram_local' AND indexname='testagram_local_scheduled_posts_claim_idx'
  ) THEN
    RAISE EXCEPTION 'scheduled-post claim index is missing';
  END IF;
END
$verify$;
SQL
