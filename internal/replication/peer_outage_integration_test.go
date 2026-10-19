package replication

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPeerOutageRecoveryIntegration is enabled only in the disposable two-DB CI
// job. It exercises a real source write, durable retry during peer outage, HMAC
// delivery, destination inbox/version application, and source acknowledgement.
func TestPeerOutageRecoveryIntegration(t *testing.T) {
	if os.Getenv("TESTAGRAM_REPLICATION_INTEGRATION") != "1" { t.Skip("requires isolated source and destination PostgreSQL databases") }
	sourceURL,destinationURL:=os.Getenv("SOURCE_DATABASE_URL"),os.Getenv("DESTINATION_DATABASE_URL")
	if sourceURL==""||destinationURL=="" { t.Fatal("source and destination database URLs are required") }
	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second); defer cancel()
	sourceDB,err:=pgxpool.New(ctx,sourceURL); if err!=nil { t.Fatal(err) }; defer sourceDB.Close()
	destinationDB,err:=pgxpool.New(ctx,destinationURL); if err!=nil { t.Fatal(err) }; defer destinationDB.Close()
	if err:=sourceDB.Ping(ctx); err!=nil { t.Fatal(err) }; if err:=destinationDB.Ping(ctx); err!=nil { t.Fatal(err) }
	source,err:=NewStore(sourceDB); if err!=nil { t.Fatal(err) }; destination,err:=NewStore(destinationDB); if err!=nil { t.Fatal(err) }

	suffix:=time.Now().UnixNano()&0xffffffffffff
	accountID:=fmt.Sprintf("123e4567-e89b-42d3-a456-%012x",suffix)
	postID:=fmt.Sprintf("223e4567-e89b-42d3-a456-%012x",suffix)
	_,err=sourceDB.Exec(ctx,"INSERT INTO testagram_local.accounts(user_id,username,display_name) VALUES($1::uuid,$2,$3)",accountID,fmt.Sprintf("ci_%012x",suffix),"Outage test")
	if err!=nil { t.Fatalf("source account write should work without a peer: %v",err) }
	_,err=sourceDB.Exec(ctx,"INSERT INTO testagram_local.posts(id,author_id,text,visibility) VALUES($1::uuid,$2::uuid,$3,'public')",postID,accountID,"outage recovery post")
	if err!=nil { t.Fatalf("source post write should work without a peer: %v",err) }
	var pending int
	if err:=sourceDB.QueryRow(ctx,"SELECT count(*) FROM platform_replication_outbox WHERE entity_id IN ($1,$2) AND delivered_at IS NULL",accountID,postID).Scan(&pending); err!=nil || pending<2 {
		t.Fatalf("source writes must retain outbox events during peer outage; pending=%d err=%v",pending,err)
	}

	key:=[]byte("ci-only-replication-key-32-bytes-minimum")
	receiver,err:=NewReceiver(destination,map[string][]byte{"testagram-vps":key},(ProjectionApplier{}).Apply)
	if err!=nil { t.Fatal(err) }
	peer:=httptest.NewTLSServer(receiver); defer peer.Close()
	closed:=httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter,*http.Request){}))
	deadEndpoint:=closed.URL
	closed.Close()
	client:=peer.Client()
	if err:=source.RelayOnce(ctx,client,deadEndpoint,"testagram-vps",key,20); err==nil { t.Fatal("relay should report peer outage") }
	if err:=sourceDB.QueryRow(ctx,"SELECT count(*) FROM platform_replication_outbox WHERE entity_id IN ($1,$2) AND delivered_at IS NULL AND failed_at IS NULL",accountID,postID).Scan(&pending); err!=nil || pending<2 {
		t.Fatalf("peer outage must retain retryable events; pending=%d err=%v",pending,err)
	}
	if _,err:=sourceDB.Exec(ctx,"UPDATE platform_replication_outbox SET available_at=now(),lease_until=NULL,lease_token=NULL WHERE entity_id IN ($1,$2) AND delivered_at IS NULL AND failed_at IS NULL",accountID,postID); err!=nil { t.Fatal(err) }

	if err:=source.RelayOnce(ctx,client,peer.URL,"testagram-vps",key,20); err!=nil { t.Fatalf("relay after peer recovery: %v",err) }
	var projected int
	if err:=destinationDB.QueryRow(ctx,"SELECT count(*) FROM platform_replication_projection WHERE origin='testagram-vps' AND entity_id IN ($1,$2) AND tombstoned=FALSE",accountID,postID).Scan(&projected); err!=nil || projected!=2 {
		t.Fatalf("expected account and post projections after recovery; got %d err=%v",projected,err)
	}
	if err:=sourceDB.QueryRow(ctx,"SELECT count(*) FROM platform_replication_outbox WHERE entity_id IN ($1,$2) AND delivered_at IS NOT NULL",accountID,postID).Scan(&pending); err!=nil || pending<2 {
		t.Fatalf("source events should be acknowledged after receiver accepts them; delivered=%d err=%v",pending,err)
	}
}
