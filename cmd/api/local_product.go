package main
import (
 "context"
 "crypto/hmac"
 "crypto/sha256"
 "encoding/base64"
 "encoding/json"
 "errors"
 "net/http"
 "regexp"
 "strings"
 "time"
 "github.com/jackc/pgx/v5"
)
var localUUIDPattern = regexp.MustCompile("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$")
type localProfileInput struct { Username string `json:"username"`; DisplayName string `json:"display_name"`; Bio string `json:"bio"`; Website string `json:"website"`; AvatarKey string `json:"avatar_key"`; Discoverable *bool `json:"discoverable_by_username"`; Protected *bool `json:"protected_account"` }
type localPostInput struct { ID string `json:"id"`; Text string `json:"text"`; Visibility string `json:"visibility"`; MediaIDs []string `json:"media_ids"`; Language string `json:"language"` }
func localProductEnabled() bool { return strings.EqualFold(env("TESTAGRAM_LOCAL_PRODUCT_WRITES_ENABLED", "false"), "true") }
func localCapabilityName(name string) bool { switch name { case "testagram.profile.update", "testagram.posts.create", "testagram.posts.update", "testagram.posts.delete": return true }; return false }
func localJWTSubject(authorization string) (string, error) {
 parts := strings.Fields(authorization); if len(parts)!=2 || !strings.EqualFold(parts[0],"Bearer") { return "", errors.New("bearer token required") }
 secret:=env("SUPABASE_JWT_SECRET",""); if secret=="" { return "",errors.New("local JWT verification is not configured") }
 segments:=strings.Split(parts[1],"."); if len(segments)!=3 { return "",errors.New("invalid token") }
 hb,err:=base64.RawURLEncoding.DecodeString(segments[0]); if err!=nil { return "",errors.New("invalid token header") }; var header map[string]any; if json.Unmarshal(hb,&header)!=nil || header["alg"]!="HS256" { return "",errors.New("only HS256 local verification is configured") }
 sig,err:=base64.RawURLEncoding.DecodeString(segments[2]); if err!=nil { return "",errors.New("invalid token signature") }; mac:=hmac.New(sha256.New,[]byte(secret)); _,_=mac.Write([]byte(segments[0]+"."+segments[1])); if !hmac.Equal(sig,mac.Sum(nil)) { return "",errors.New("invalid token signature") }
 pb,err:=base64.RawURLEncoding.DecodeString(segments[1]); if err!=nil { return "",errors.New("invalid token claims") }; var claims map[string]any; if json.Unmarshal(pb,&claims)!=nil { return "",errors.New("invalid token claims") }
 sub,_:=claims["sub"].(string); exp,ok:=claims["exp"].(float64); if !ok || exp<=float64(time.Now().Unix()) || !localUUIDPattern.MatchString(sub) { return "",errors.New("invalid or expired token claims") }; return strings.ToLower(sub),nil
}
func (s *service) localProductCapability(w http.ResponseWriter,r *http.Request,requestID string,body capabilityRequest) bool {
 if !localProductEnabled() || !localCapabilityName(body.Capability) { return false }; w.Header().Set("Cache-Control","private, no-store")
 userID,err:=localJWTSubject(r.Header.Get("Authorization")); if err!=nil { writeJSON(w,http.StatusUnauthorized,errorBody("AUTH_REQUIRED","A valid locally verifiable bearer token is required",requestID)); return true }
 if s.db==nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("LOCAL_STORE_UNAVAILABLE","Testagram local product store is unavailable",requestID)); return true }
 ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second); defer cancel()
 switch body.Capability {
 case "testagram.profile.update":
  raw,_:=json.Marshal(body.Input); var in localProfileInput; if json.Unmarshal(raw,&in)!=nil || !regexp.MustCompile("^[A-Za-z0-9_]{3,30}$").MatchString(in.Username) || len(in.DisplayName)>80 || len(in.Bio)>500 || len(in.Website)>2048 || len(in.AvatarKey)>512 { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_PROFILE","Profile fields are invalid",requestID)); return true }
  discoverable,protected:=true,false; if in.Discoverable!=nil { discoverable=*in.Discoverable }; if in.Protected!=nil { protected=*in.Protected }; var saved string
  err=s.db.QueryRow(ctx,"INSERT INTO testagram_local.accounts(user_id,username,display_name,bio,website,avatar_key,discoverable_by_username,protected_account) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(user_id) DO UPDATE SET username=EXCLUDED.username,display_name=EXCLUDED.display_name,bio=EXCLUDED.bio,website=EXCLUDED.website,avatar_key=EXCLUDED.avatar_key,discoverable_by_username=EXCLUDED.discoverable_by_username,protected_account=EXCLUDED.protected_account,updated_at=now() RETURNING user_id::text",userID,in.Username,in.DisplayName,in.Bio,in.Website,in.AvatarKey,discoverable,protected).Scan(&saved)
  if err!=nil { writeJSON(w,http.StatusConflict,errorBody("PROFILE_WRITE_FAILED","Profile could not be saved locally",requestID)); return true }; writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"user_id":saved,"username":in.Username}}); return true
 case "testagram.posts.create":
  raw,_:=json.Marshal(body.Input); var in localPostInput; if json.Unmarshal(raw,&in)!=nil || len(in.Text)>10000 || (in.Visibility!="" && in.Visibility!="public" && in.Visibility!="followers" && in.Visibility!="private") || len(in.MediaIDs)>20 || len(in.Language)>32 || (strings.TrimSpace(in.Text)=="" && len(in.MediaIDs)==0) { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POST","Post fields are invalid",requestID)); return true }; if in.Visibility=="" { in.Visibility="public" }; media,_:=json.Marshal(in.MediaIDs); var id string
  err=s.db.QueryRow(ctx,"INSERT INTO testagram_local.posts(author_id,text,visibility,media_ids,language) VALUES($1::uuid,$2,$3,$4::jsonb,$5) RETURNING id::text",userID,in.Text,in.Visibility,string(media),in.Language).Scan(&id)
  if err!=nil { writeJSON(w,http.StatusConflict,errorBody("POST_WRITE_FAILED","Post could not be saved locally; create a local profile first",requestID)); return true }; writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":id,"author_id":userID,"text":in.Text,"visibility":in.Visibility}}); return true
 case "testagram.posts.update":
  raw,_:=json.Marshal(body.Input); var in localPostInput; if json.Unmarshal(raw,&in)!=nil || !localUUIDPattern.MatchString(in.ID) || len(in.Text)>10000 || (in.Visibility!="public" && in.Visibility!="followers" && in.Visibility!="private") || len(in.MediaIDs)>20 || len(in.Language)>32 { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POST","Post fields are invalid",requestID)); return true }; if in.MediaIDs == nil { in.MediaIDs = []string{} }; media,_:=json.Marshal(in.MediaIDs); var id string
  err=s.db.QueryRow(ctx,"UPDATE testagram_local.posts SET text=$3,visibility=$4,media_ids=$5::jsonb,language=$6,updated_at=now() WHERE id=$1::uuid AND author_id=$2::uuid AND deleted_at IS NULL RETURNING id::text",in.ID,userID,in.Text,in.Visibility,string(media),in.Language).Scan(&id); if errors.Is(err,pgx.ErrNoRows) { http.NotFound(w,r); return true }; if err!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("POST_WRITE_FAILED","Post could not be updated locally",requestID)); return true }; writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"id":id}}); return true
 case "testagram.posts.delete":
  postID,_:=body.Input["id"].(string); if !localUUIDPattern.MatchString(postID) { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POST_ID","Post ID is invalid",requestID)); return true }; tag,execErr:=s.db.Exec(ctx,"UPDATE testagram_local.posts SET deleted_at=now(),updated_at=now() WHERE id=$1::uuid AND author_id=$2::uuid AND deleted_at IS NULL",postID,userID); if execErr!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("POST_DELETE_FAILED","Post could not be deleted locally",requestID)); return true }; if tag.RowsAffected()==0 { http.NotFound(w,r); return true }; writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"id":postID,"deleted":true}}); return true
 }; return false
}