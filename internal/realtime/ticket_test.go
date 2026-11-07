package realtime

import (
	"testing"
	"time"
)

func TestIssueAndVerifyConversationTicket(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	token, err := Issue("0123456789abcdef0123456789abcdef", "appwrite-user-1", "conversation-1", now, time.Minute)
	if err != nil { t.Fatal(err) }
	claims, err := Verify("0123456789abcdef0123456789abcdef", token, "conversation-1", now.Add(10*time.Second))
	if err != nil { t.Fatal(err) }
	if claims.Subject != "appwrite-user-1" || claims.ConversationID != "conversation-1" { t.Fatalf("unexpected claims: %+v", claims) }
}

func TestVerifyRejectsWrongConversationExpiredAndTamperedTicket(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	token, err := Issue("0123456789abcdef0123456789abcdef", "user-1", "conversation-1", now, time.Minute)
	if err != nil { t.Fatal(err) }
	for name, candidate := range map[string]struct{token,conversation string; at time.Time}{
		"wrong conversation": {token,"conversation-2",now},
		"expired": {token,"conversation-1",now.Add(2*time.Minute)},
		"tampered": {token+"x","conversation-1",now},
	} {
		t.Run(name,func(t *testing.T){
			if _,err:=Verify("0123456789abcdef0123456789abcdef",candidate.token,candidate.conversation,candidate.at); err==nil { t.Fatal("invalid ticket accepted") }
		})
	}
}

func TestIssueRejectsWeakSecretAndLongTTL(t *testing.T) {
	now:=time.Now()
	if _,err:=Issue("short","user","conversation",now,time.Minute); err==nil { t.Fatal("weak secret accepted") }
	if _,err:=Issue("0123456789abcdef0123456789abcdef","user","conversation",now,10*time.Minute); err==nil { t.Fatal("long lifetime accepted") }
}

func TestSignPublishRequiresSecretAndBoundsBody(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	signature, err := SignPublish(secret, []byte("{\"event_id\":\"e1\"}"))
	if err != nil || signature == "" { t.Fatalf("expected publish signature, got %q, %v", signature, err) }
	if _, err := SignPublish("short", []byte("{}")); err == nil { t.Fatal("weak publish secret accepted") }
	if _, err := SignPublish(secret, make([]byte, 64*1024+1)); err == nil { t.Fatal("oversized event accepted") }
}
