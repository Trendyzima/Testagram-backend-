package main
import("context";"encoding/json";"errors";"net/http";"net/http/httptest";"strings";"testing")
func TestMailjetAPIConfigurationRequiresCredentials(t *testing.T){
 t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY",strings.Repeat("61",32));t.Setenv("AUTH_FROM_EMAIL","no-reply@testagram.site");t.Setenv("MAILJET_API_KEY","api-key");t.Setenv("MAILJET_SECRET_KEY","secret")
 if err:=mailProviderConfigurationError();err!=nil{t.Fatalf("valid Mailjet API configuration rejected: %v",err)}
 t.Setenv("MAILJET_SECRET_KEY","");if err:=mailProviderConfigurationError();err==nil{t.Fatal("missing Mailjet secret was accepted")}
}
func TestSendMailjetAPIUsesHTTPSBasicAuthAndPreservesEventCorrelation(t *testing.T){
 t.Setenv("MAILJET_API_KEY","api-key");t.Setenv("MAILJET_SECRET_KEY","secret");t.Setenv("AUTH_FROM_EMAIL","no-reply@testagram.site")
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  user,pass,ok:=r.BasicAuth();if !ok||user!="api-key"||pass!="secret"{t.Errorf("unexpected Mailjet API authentication")}
  if r.Method!=http.MethodPost||r.Header.Get("Content-Type")!="application/json"{t.Errorf("unexpected request method or content type")}
  var body mailjetSendRequest;if err:=json.NewDecoder(r.Body).Decode(&body);err!=nil{t.Fatal(err)}
  if len(body.Messages)!=1{t.Fatalf("message count = %d",len(body.Messages))};msg:=body.Messages[0]
  if msg.From.Email!="no-reply@testagram.site"||msg.To[0].Email!="person@example.test"{t.Fatalf("unexpected sender or recipient: %#v",msg)}
  if msg.CustomID==""||!strings.HasPrefix(msg.CustomID,"testagram-")||len(msg.CustomID)>64{t.Fatalf("invalid custom correlation ID: %q",msg.CustomID)}
  if msg.Headers["List-Unsubscribe-Post"]!="List-Unsubscribe=One-Click"{t.Fatalf("unsubscribe headers not forwarded: %#v",msg.Headers)}
  w.Header().Set("Content-Type","application/json");_,_=w.Write([]byte(`{"Messages":[{"Status":"success","To":[{"MessageID":12345}]}]}`))
 }));defer server.Close()
 customID,err:=sendMailjetAPIWithClient(context.Background(),queuedMail{Recipient:"person@example.test",Subject:"Testagram notice",Text:"Hello",HTML:"<p>Hello</p>",Headers:map[string]string{"List-Unsubscribe":"<https://testagram.site/unsubscribe>","List-Unsubscribe-Post":"List-Unsubscribe=One-Click"}},server.Client(),server.URL)
 if err!=nil{t.Fatal(err)};if !strings.HasPrefix(customID,"testagram-"){t.Fatalf("returned correlation ID = %q",customID)}
}
func TestSendMailjetAPIQuarantinesAmbiguousServerFailure(t *testing.T){
 t.Setenv("MAILJET_API_KEY","api-key");t.Setenv("MAILJET_SECRET_KEY","secret");t.Setenv("AUTH_FROM_EMAIL","no-reply@testagram.site")
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){http.Error(w,"upstream error",http.StatusInternalServerError)}));defer server.Close()
 _,err:=sendMailjetAPIWithClient(context.Background(),queuedMail{Recipient:"person@example.test",Subject:"Test",HTML:"<p>Hi</p>"},server.Client(),server.URL)
 var ambiguous ambiguousMailError;if !errors.As(err,&ambiguous){t.Fatalf("HTTP 500 must be quarantined as ambiguous, got %v",err)}
}
func TestSendMailjetAPIRejectsUnsafeCustomHeaders(t *testing.T){
 t.Setenv("MAILJET_API_KEY","api-key");t.Setenv("MAILJET_SECRET_KEY","secret");t.Setenv("AUTH_FROM_EMAIL","no-reply@testagram.site")
 _,err:=sendMailjetAPIWithClient(context.Background(),queuedMail{Recipient:"person@example.test",Subject:"Test",HTML:"<p>Hi</p>",Headers:map[string]string{"X-Test":"safe"}},http.DefaultClient,"https://api.mailjet.com/v3.1/send")
 if err==nil{t.Fatal("unsupported custom header was accepted")}
}
