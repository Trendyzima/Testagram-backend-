package mediaassertion
import("strings";"testing";"time")
func TestIssueVerifyBindsViewerObjectAndExpiry(t *testing.T){s:=[]byte(strings.Repeat("s",40));i,e:=New(s,2*time.Minute);if e!=nil{t.Fatal(e)};now:=time.Unix(1800000000,0).UTC();key:="users/alice/0123456789abcdef0123456789abcdef";token,exp,e:=i.Issue(key,"alice",now);if e!=nil{t.Fatal(e)};if !exp.Equal(now.Add(2*time.Minute)){t.Fatal("wrong expiry")};if _,e=Verify(s,token,key,"alice",now.Add(time.Second));e!=nil{t.Fatal(e)};if _,e=Verify(s,token,key,"bob",now);e==nil{t.Fatal("wrong viewer accepted")};if _,e=Verify(s,token,"users/alice/ffffffffffffffffffffffffffffffff","alice",now);e==nil{t.Fatal("wrong key accepted")};if _,e=Verify(s,token,key,"alice",exp);e==nil{t.Fatal("expired token accepted")}}
func TestRejectWeakSecretAndOwnerMismatch(t *testing.T){if _,e:=New([]byte("short"),time.Minute);e==nil{t.Fatal("weak secret accepted")};i,_:=New([]byte(strings.Repeat("x",32)),time.Minute);if _,_,e:=i.Issue("users/bob/0123456789abcdef0123456789abcdef","alice",time.Now());e==nil{t.Fatal("cross-owner key accepted")};if _,_,e:=i.Issue("../private/object","alice",time.Now());e==nil{t.Fatal("invalid key accepted")}}
func TestRejectTampering(t *testing.T){s:=[]byte(strings.Repeat("k",32));i,_:=New(s,time.Minute);now:=time.Now().UTC();key:="users/alice/0123456789abcdef0123456789abcdef";token,_,e:=i.Issue(key,"alice",now);if e!=nil{t.Fatal(e)};tampered:=token[:len(token)-1]+"A";if _,e:=Verify(s,tampered,key,"alice",now);e==nil{t.Fatal("tampered assertion accepted")}}

func TestIssuerVerifyForKey(t *testing.T) {
	secret := []byte(strings.Repeat("v", 32))
	i, err := New(secret, time.Minute)
	if err != nil { t.Fatal(err) }
	key := "users/alice/0123456789abcdef0123456789abcdef"
	token, _, err := i.Issue(key, "alice", time.Now())
	if err != nil { t.Fatal(err) }
	claims, err := i.VerifyForKey(token, key, time.Now())
	if err != nil { t.Fatal(err) }
	if claims.Subject != "alice" || claims.Key != key { t.Fatalf("unexpected claims: %+v", claims) }
	if _, err := i.VerifyForKey(token, "users/alice/ffffffffffffffffffffffffffffffff", time.Now()); err == nil { t.Fatal("assertion accepted for a different object") }
}

func TestIssueVerifySupportsAppwriteOwnerIDs(t *testing.T) {
	secret := []byte(strings.Repeat("a", 32))
	issuer, err := New(secret, time.Minute)
	if err != nil { t.Fatal(err) }
	key := "users/user_123.test/0123456789abcdef0123456789abcdef"
	token, _, err := issuer.Issue(key, "user_123.test", time.Now())
	if err != nil { t.Fatal(err) }
	if _, err := Verify(secret, token, key, "user_123.test", time.Now()); err != nil {
		t.Fatalf("Appwrite owner ID assertion rejected: %v", err)
	}
}
