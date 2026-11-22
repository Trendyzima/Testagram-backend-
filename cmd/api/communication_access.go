package main

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

type communicationQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// lockCommunicationAccounts serializes DM creation against block/follow/privacy
// changes. Social graph mutations take FOR UPDATE locks on the same account rows.
func lockCommunicationAccounts(ctx context.Context, tx pgx.Tx, accountIDs ...string) error {
	ids := append([]string(nil), accountIDs...)
	sort.Strings(ids)
	last := ""
	for _, id := range ids {
		if id == last {
			continue
		}
		last = id
		var lockedID string
		if err := tx.QueryRow(ctx,
			"SELECT user_id::text FROM testagram_local.accounts WHERE user_id=$1 FOR SHARE",
			id,
		).Scan(&lockedID); err != nil {
			return err
		}
	}
	return nil
}

// communicationRecipientAllowed mirrors profile discoverability and protected
// account rules and rejects either direction of a block. A raw UUID must never
// bypass the policy enforced by profile-handle resolution.
func communicationRecipientAllowed(ctx context.Context, q communicationQueryRower, senderID, recipientID string) (bool, error) {
	var allowed bool
	err := q.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM testagram_local.accounts recipient
  WHERE recipient.user_id=$2
    AND recipient.account_status='active'
    AND (recipient.discoverable_by_username OR EXISTS(
      SELECT 1 FROM testagram_local.follows f
      WHERE f.follower_id=$1 AND f.following_id=recipient.user_id AND f.status='accepted'
    ))
    AND (NOT recipient.protected_account OR EXISTS(
      SELECT 1 FROM testagram_local.follows f
      WHERE f.follower_id=$1 AND f.following_id=recipient.user_id AND f.status='accepted'
    ))
    AND NOT EXISTS(
      SELECT 1 FROM testagram_local.blocks b
      WHERE (b.blocker_id=$1 AND b.blocked_id=recipient.user_id)
         OR (b.blocker_id=recipient.user_id AND b.blocked_id=$1)
    )
)`, senderID, recipientID).Scan(&allowed)
	return allowed, err
}

// communicationConversationBlocked prevents an existing conversation from
// bypassing a block added after the conversation was created.
func communicationConversationBlocked(ctx context.Context, q communicationQueryRower, userID, conversationID string) (bool, error) {
	var blocked bool
	err := q.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM testagram_local.conversation_members cm
  JOIN testagram_local.blocks b
    ON (b.blocker_id=$1 AND b.blocked_id=cm.user_id)
    OR (b.blocker_id=cm.user_id AND b.blocked_id=$1)
  WHERE cm.conversation_id=$2
    AND cm.user_id<>$1
    AND cm.left_at IS NULL
)`, userID, conversationID).Scan(&blocked)
	return blocked, err
}

// communicationCallEndAuthorized requires current conversation membership and
// an active participant record (or the conversation creator). Historical
// participant rows must not grant call-control access after leaving.
func communicationCallEndAuthorized(ctx context.Context, q communicationQueryRower, callID, userID string) (bool, error) {
	var authorized bool
	err := q.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM testagram_local.call_sessions c
		JOIN testagram_local.conversation_members cm
		  ON cm.conversation_id=c.conversation_id AND cm.user_id=$2 AND cm.left_at IS NULL
		WHERE c.id=$1 AND (
		  c.created_by=$2 OR EXISTS(
		    SELECT 1 FROM testagram_local.call_participants cp
		    WHERE cp.call_id=c.id AND cp.user_id=$2 AND cp.left_at IS NULL
		  )
		)
	)`, callID, userID).Scan(&authorized)
	return authorized, err
}

func lockCommunicationConversationAccounts(ctx context.Context, tx pgx.Tx, userID, conversationID string) error {
	rows, err := tx.Query(ctx, `
SELECT user_id::text
FROM testagram_local.conversation_members
WHERE conversation_id=$1 AND left_at IS NULL`, conversationID)
	if err != nil {
		return err
	}
	ids := []string{userID}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	return lockCommunicationAccounts(ctx, tx, ids...)
}
