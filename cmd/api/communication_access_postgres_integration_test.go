package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCommunicationRecipientAndBlockPolicyPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("TESTAGRAM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TESTAGRAM_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to PostgreSQL integration database: %v", err)
	}
	defer conn.Close(context.Background())

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	suffix := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	insertAccount := func(username string, protected, discoverable bool) string {
		t.Helper()
		var id string
		err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.accounts(user_id,username,protected_account,discoverable_by_username)
VALUES(gen_random_uuid(),$1,$2,$3)
RETURNING user_id::text`, username+suffix, protected, discoverable).Scan(&id)
		if err != nil {
			t.Fatalf("insert communication fixture account %s: %v", username, err)
		}
		return id
	}

	sender := insertAccount("cmsender", false, true)
	publicRecipient := insertAccount("cmpub", false, true)
	hiddenRecipient := insertAccount("cmhidden", false, false)
	protectedRecipient := insertAccount("cmprotected", true, true)

	assertAllowed := func(name, recipient string, want bool) {
		t.Helper()
		got, err := communicationRecipientAllowed(ctx, tx, sender, recipient)
		if err != nil {
			t.Fatalf("%s: recipient policy query failed: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: allowed=%v, want %v", name, got, want)
		}
	}

	assertAllowed("discoverable public recipient", publicRecipient, true)
	assertAllowed("undiscoverable recipient without accepted follow", hiddenRecipient, false)
	assertAllowed("protected recipient without accepted follow", protectedRecipient, false)

	if _, err := tx.Exec(ctx, `
INSERT INTO testagram_local.follows(follower_id,following_id,status)
VALUES($1,$2,'accepted')`, sender, protectedRecipient); err != nil {
		t.Fatalf("insert accepted follow fixture: %v", err)
	}
	assertAllowed("protected recipient with accepted follow", protectedRecipient, true)

	var conversationID string
	if err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.conversations(kind,created_by)
VALUES('direct',$1) RETURNING id::text`, sender).Scan(&conversationID); err != nil {
		t.Fatalf("insert conversation fixture: %v", err)
	}
	for _, member := range []string{sender, publicRecipient} {
		if _, err := tx.Exec(ctx, `
INSERT INTO testagram_local.conversation_members(conversation_id,user_id)
VALUES($1,$2)`, conversationID, member); err != nil {
			t.Fatalf("insert conversation member fixture: %v", err)
		}
	}

	blocked, err := communicationConversationBlocked(ctx, tx, sender, conversationID)
	if err != nil {
		t.Fatalf("check conversation before block: %v", err)
	}
	if blocked {
		t.Fatal("conversation should be available before a block is created")
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO testagram_local.blocks(blocker_id,blocked_id)
VALUES($1,$2)`, publicRecipient, sender); err != nil {
		t.Fatalf("insert reverse block fixture: %v", err)
	}
	assertAllowed("recipient blocked the sender", publicRecipient, false)
	blocked, err = communicationConversationBlocked(ctx, tx, sender, conversationID)
	if err != nil {
		t.Fatalf("check conversation after block: %v", err)
	}
	if !blocked {
		t.Fatal("existing conversation should be blocked when the other member blocks the sender")
	}

	var mediaEncryptionEnabled bool
	if err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.call_sessions(conversation_id,created_by,room_name,kind,status)
VALUES($1,$2,$3,'voice','ringing')
RETURNING e2ee_enabled`, conversationID, sender, "ci-room-"+suffix).Scan(&mediaEncryptionEnabled); err != nil {
		t.Fatalf("insert call session fixture: %v", err)
	}
	if mediaEncryptionEnabled {
		t.Fatal("call sessions must not claim media end-to-end encryption before a verified media-key lifecycle exists")
	}

	if err := lockCommunicationAccounts(ctx, tx, sender, publicRecipient); err != nil {
		t.Fatalf("lock communication account pair: %v", err)
	}

	var callMediaEncrypted bool
	if err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.call_sessions(conversation_id,created_by,room_name,kind)
VALUES($1,$2,$3,'voice')
RETURNING e2ee_enabled`, conversationID, sender, "testagram-ci-"+suffix).Scan(&callMediaEncrypted); err != nil {
		t.Fatalf("insert call session fixture: %v", err)
	}
	if callMediaEncrypted {
		t.Fatal("new call session must not claim media end-to-end encryption before a verified media lifecycle exists")
	}
	var messageID string
	var messageE2EEEnabled bool
	if err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.messages(conversation_id,sender_id,body)
VALUES($1,$2,'') RETURNING id::text,e2ee_enabled`, conversationID, sender).Scan(&messageID, &messageE2EEEnabled); err != nil {
		t.Fatalf("insert message resolver fixture: %v", err)
	}
	if messageE2EEEnabled {
		t.Fatal("new messages must not claim end-to-end encryption before a verified device-key lifecycle exists")
	}
	var resolverCallID string
	if err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.call_sessions(conversation_id,created_by,room_name,kind)
VALUES($1,$2,$3,'voice') RETURNING id::text`, conversationID, sender, "resolver-room-"+suffix).Scan(&resolverCallID); err != nil {
		t.Fatalf("insert call resolver fixture: %v", err)
	}
	resolverCases := []struct {
		name    string
		request capabilityRequest
	}{
		{
			name: "message delete resolves conversation for block policy",
			request: capabilityRequest{
				Capability: "testagram.messages.delete",
				Input:      map[string]any{"message_id": messageID},
			},
		},
		{
			name: "call end resolves conversation for block policy",
			request: capabilityRequest{
				Capability: "testagram.calls.end",
				Input:      map[string]any{"call_id": resolverCallID},
			},
		},
	}
	for _, tc := range resolverCases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := communicationResolveConversationID(ctx, tx, tc.request)
			if err != nil {
				t.Fatalf("resolve conversation: %v", err)
			}
			if resolved != conversationID {
				t.Fatalf("resolved conversation %q, want %q", resolved, conversationID)
			}
		})
	}

	var callID string
	if err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.call_sessions(conversation_id,created_by,room_name,kind,e2ee_enabled)
VALUES($1,$2,$3,'voice',FALSE) RETURNING id::text`, conversationID, sender, "active-participant-"+suffix).Scan(&callID); err != nil {
		t.Fatalf("insert call authorization fixture: %v", err)
	}
	for _, participant := range []string{sender, publicRecipient, hiddenRecipient} {
		if _, err := tx.Exec(ctx, "INSERT INTO testagram_local.call_participants(call_id,user_id) VALUES($1,$2)", callID, participant); err != nil {
			t.Fatalf("insert call participant %s: %v", participant, err)
		}
	}
	assertCanEnd := func(name, userID string, want bool) {
		t.Helper()
		got, err := communicationCallEndAuthorized(ctx, tx, callID, userID)
		if err != nil {
			t.Fatalf("%s: call-end authorization query failed: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: authorized=%v, want %v", name, got, want)
		}
	}
	assertCanEnd("active creator", sender, true)
	assertCanEnd("active participant", publicRecipient, true)
	assertCanEnd("participant without current conversation membership", hiddenRecipient, false)
	if _, err := tx.Exec(ctx, "UPDATE testagram_local.call_participants SET left_at=now() WHERE call_id=$1 AND user_id=$2", callID, publicRecipient); err != nil {
		t.Fatalf("mark participant as left: %v", err)
	}
	assertCanEnd("former participant", publicRecipient, false)
	if _, err := tx.Exec(ctx, "UPDATE testagram_local.conversation_members SET left_at=now() WHERE conversation_id=$1 AND user_id=$2", conversationID, sender); err != nil {
		t.Fatalf("remove creator from active conversation: %v", err)
	}
	assertCanEnd("creator no longer in active conversation", sender, false)

}
