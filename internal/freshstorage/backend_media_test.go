package freshstorage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackendMediaClientIssuesBackendSignedCDNAuthorization(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/media/fresh/cdn-download-url" || r.Method != http.MethodPost {
			t.Fatalf("unexpected Backend route %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Testagram-Fresh-Media-Token") != token {
			t.Fatal("Fresh media service token was not forwarded")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
		if body["viewer_id"] != "viewer_2" || body["owner_id"] != "user_123.test" || body["key"] != "users/user_123.test/0123456789abcdef0123456789abcdef" {
			t.Fatalf("unexpected authorization identity: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://media.testagram.site/v1/users/user_123.test/0123456789abcdef0123456789abcdef","authorization":"Bearer v1.payload.signature","expires_in_seconds":120,"visibility":"private"}`))
	}))
	defer server.Close()
	client, err := NewBackendMediaClient(server.URL, token, server.Client())
	if err != nil { t.Fatal(err) }
	delivery, err := client.Issue(context.Background(), "viewer_2", "user_123.test", "users/user_123.test/0123456789abcdef0123456789abcdef")
	if err != nil { t.Fatal(err) }
	if delivery.Authorization != "Bearer v1.payload.signature" || delivery.ExpiresInSeconds != 120 || delivery.Visibility != "private" {
		t.Fatalf("unexpected media delivery: %+v", delivery)
	}
}

func TestBackendMediaClientRejectsRedirectAndInvalidDelivery(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w,r,"https://attacker.example/collect",http.StatusFound)
	}))
	defer redirect.Close()
	client, err := NewBackendMediaClient(redirect.URL,token,redirect.Client())
	if err != nil { t.Fatal(err) }
	if _,err:=client.Issue(context.Background(),"alice","alice","users/alice/0123456789abcdef0123456789abcdef");err==nil {
		t.Fatal("redirected Backend authorization request was accepted")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		_,_=w.Write([]byte(`{"url":"https://media.example/v1/key","authorization":"Bearer x","expires_in_seconds":9999,"visibility":"public"}`))
	}))
	defer bad.Close()
	client,err=NewBackendMediaClient(bad.URL,token,bad.Client())
	if err!=nil{t.Fatal(err)}
	if _,err=client.Issue(context.Background(),"alice","alice","users/alice/0123456789abcdef0123456789abcdef");err==nil{
		t.Fatal("invalid or overlong media authorization accepted")
	}
}

func TestNewBackendMediaClientRequiresHTTPSAndStrongToken(t *testing.T) {
	if _,err:=NewBackendMediaClient("http://media.example","0123456789abcdef0123456789abcdef",nil);err==nil{
		t.Fatal("remote HTTP Backend URL accepted")
	}
	if _,err:=NewBackendMediaClient("https://api.example","short",nil);err==nil{
		t.Fatal("weak service token accepted")
	}
	if _,err:=NewBackendMediaClient("http://localhost:8080","0123456789abcdef0123456789abcdef",nil);err!=nil{
		t.Fatalf("localhost development URL rejected: %v",err)
	}
}
