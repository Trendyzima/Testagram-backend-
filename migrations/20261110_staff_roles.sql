BEGIN;

CREATE TABLE IF NOT EXISTS testagram_auth.staff_roles (
  appwrite_user_id text PRIMARY KEY CHECK (length(appwrite_user_id) BETWEEN 1 AND 36),
  role text NOT NULL CHECK (role IN ('owner','administrator','engineer','operations','support','moderator')),
  created_by_appwrite_user_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS staff_roles_role_idx
  ON testagram_auth.staff_roles(role);

CREATE TABLE IF NOT EXISTS testagram_auth.staff_role_audit (
  id bigserial PRIMARY KEY,
  actor_appwrite_user_id text NOT NULL,
  target_appwrite_user_id text NOT NULL,
  action text NOT NULL CHECK (action IN ('invited','role_changed','revoked')),
  role text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS staff_role_audit_target_created_idx
  ON testagram_auth.staff_role_audit(target_appwrite_user_id, created_at DESC);

COMMIT;
