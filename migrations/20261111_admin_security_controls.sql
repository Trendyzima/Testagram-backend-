BEGIN;

ALTER TABLE testagram_auth.staff_roles
  ADD COLUMN IF NOT EXISTS access_revoked_at timestamptz;

CREATE INDEX IF NOT EXISTS staff_roles_active_role_idx
  ON testagram_auth.staff_roles(role) WHERE access_revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_auth.admin_rate_limits (
  rate_key char(64) PRIMARY KEY CHECK (rate_key ~ '^[0-9a-f]{64}$'),
  window_started_at timestamptz NOT NULL DEFAULT now(),
  request_count integer NOT NULL CHECK (request_count >= 0)
);

CREATE TABLE IF NOT EXISTS testagram_auth.admin_permissions (
  permission_key text PRIMARY KEY,
  description text NOT NULL,
  sensitive boolean NOT NULL DEFAULT false
);

INSERT INTO testagram_auth.admin_permissions(permission_key, description, sensitive) VALUES
  ('staff.manage', 'Invite, assign, change and revoke staff access', true),
  ('identity.review', 'Review and decide identity verification applications', true),
  ('moderation.manage', 'Manage content reports, moderation and appeals', false),
  ('advertising.manage', 'Manage advertising inventory and campaigns', false),
  ('marketplace.manage', 'Manage marketplace listings and seller operations', false),
  ('tv.manage', 'Manage TV catalogue, channels and publishing', false),
  ('engineering.read', 'Read engineering and service diagnostics', false),
  ('deployment.request', 'Request production deployments', true),
  ('deployment.approve', 'Approve production deployments', true),
  ('security.manage', 'Manage security policy and incident controls', true),
  ('finance.read', 'Read financial reports and ledgers', true),
  ('finance.initiate', 'Initiate financial operations', true),
  ('finance.approve', 'Approve financial operations', true),
  ('operations.manage', 'Manage operational workflows', false),
  ('audit.read', 'Read privileged action and access audit history', true)
ON CONFLICT (permission_key) DO UPDATE
SET description = EXCLUDED.description, sensitive = EXCLUDED.sensitive;

CREATE TABLE IF NOT EXISTS testagram_auth.staff_role_permissions (
  role text NOT NULL CHECK (role IN ('owner','administrator','engineer','operations','support','moderator')),
  permission_key text NOT NULL REFERENCES testagram_auth.admin_permissions(permission_key) ON DELETE CASCADE,
  PRIMARY KEY (role, permission_key)
);

INSERT INTO testagram_auth.staff_role_permissions(role, permission_key) VALUES
  ('administrator','identity.review'),
  ('administrator','moderation.manage'),
  ('administrator','advertising.manage'),
  ('administrator','marketplace.manage'),
  ('administrator','tv.manage'),
  ('administrator','engineering.read'),
  ('administrator','operations.manage'),
  ('administrator','audit.read'),
  ('engineer','engineering.read'),
  ('engineer','deployment.request'),
  ('operations','engineering.read'),
  ('operations','operations.manage'),
  ('operations','deployment.request'),
  ('support','moderation.manage'),
  ('moderator','moderation.manage')
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS testagram_auth.staff_permission_overrides (
  appwrite_user_id text NOT NULL REFERENCES testagram_auth.staff_roles(appwrite_user_id) ON DELETE CASCADE,
  permission_key text NOT NULL REFERENCES testagram_auth.admin_permissions(permission_key) ON DELETE CASCADE,
  granted boolean NOT NULL,
  reason text NOT NULL CHECK (length(trim(reason)) BETWEEN 8 AND 500),
  changed_by_appwrite_user_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (appwrite_user_id, permission_key)
);

CREATE TABLE IF NOT EXISTS testagram_auth.sensitive_action_requests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  action_type text NOT NULL CHECK (action_type IN ('identity.approve','finance.execute','security.change','deployment.production')),
  requested_by_appwrite_user_id text NOT NULL REFERENCES testagram_auth.staff_roles(appwrite_user_id),
  target_reference text NOT NULL CHECK (length(trim(target_reference)) BETWEEN 1 AND 200),
  reason text NOT NULL CHECK (length(trim(reason)) BETWEEN 12 AND 1000),
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  payload_sha256 char(64) NOT NULL CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
  required_approvals smallint NOT NULL DEFAULT 2 CHECK (required_approvals BETWEEN 2 AND 5),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired','cancelled')),
  expires_at timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz
);

CREATE INDEX IF NOT EXISTS sensitive_action_pending_idx
  ON testagram_auth.sensitive_action_requests(status, expires_at, created_at DESC);

CREATE TABLE IF NOT EXISTS testagram_auth.sensitive_action_approvals (
  id bigserial PRIMARY KEY,
  request_id uuid NOT NULL REFERENCES testagram_auth.sensitive_action_requests(id) ON DELETE RESTRICT,
  approver_appwrite_user_id text NOT NULL REFERENCES testagram_auth.staff_roles(appwrite_user_id),
  decision text NOT NULL CHECK (decision IN ('approved','rejected')),
  reason text NOT NULL CHECK (length(trim(reason)) BETWEEN 8 AND 1000),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(request_id, approver_appwrite_user_id)
);

CREATE TABLE IF NOT EXISTS testagram_auth.admin_security_audit (
  id bigserial PRIMARY KEY,
  actor_appwrite_user_id text NOT NULL,
  action text NOT NULL,
  target_reference text NOT NULL DEFAULT '',
  request_id uuid REFERENCES testagram_auth.sensitive_action_requests(id) ON DELETE RESTRICT,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS admin_security_audit_created_idx
  ON testagram_auth.admin_security_audit(created_at DESC);
CREATE INDEX IF NOT EXISTS admin_security_audit_actor_idx
  ON testagram_auth.admin_security_audit(actor_appwrite_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS sensitive_action_approvals_request_idx
  ON testagram_auth.sensitive_action_approvals(request_id, created_at);

COMMIT;
