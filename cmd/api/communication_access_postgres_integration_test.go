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

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	insertAccount := func(username string, protected, discoverable bool) string {
		t.Helper()
		var id string
		err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.accounts(user_id,username,protected_account,discoverable_by_username)
VALUES(gen_random_uuid(),$1,$2,$3)
RETURNING user_id::text`, username+"_"+suffix, protected, discoverable).Scan(&id)
		if err != nil {
			t.Fatalf("insert communication fixture account %s: %v", username, err)
		}
		return id
	}

	sender := insertAccount("ci_comm_sender", false, true)
	publicRecipient := insertAccount("ci_comm_public", false, true)
	hiddenRecipient := insertAccount("ci_comm_hidden", false, false)
	protectedRecipient := insertAccount("ci_comm_protected", true, true)

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

	if err := lockCommunicationAccounts(ctx, tx, sender, publicRecipient); err != nil {
		t.Fatalf("lock communication account pair: %v", err)
	}
}
