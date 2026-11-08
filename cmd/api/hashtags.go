package main

import (
 "net/http"
 "regexp"
 "strconv"
 "strings"
 "time"
 "encoding/json"
 "github.com/jackc/pgx/v5"
)
var hashtagTagPattern=regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
func (s *service) hashtagResolve(w http.ResponseWriter,r *http.Request){
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return};if s.db==nil{writeJSON(w,503,errorBody("HASHTAG_SERVICE_UNAVAILABLE","Hashtag service is unavailable",id));return}
 tag:=strings.ToLower(strings.TrimSpace(strings.TrimPrefix(r.PathValue("tag"),"#")));if !hashtagTagPattern.MatchString(tag){writeJSON(w,400,errorBody("INVALID_HASHTAG","Hashtag is invalid",id));return}
 var item map[string]any;err:=s.db.QueryRow(r.Context(),`SELECT jsonb_build_object('id',h.id,'tag',h.tag,'usage_count',h.usage_count,'post_count',h.usage_count,'federated_post_count',h.federated_post_count,'follower_count',h.follower_count) FROM testagram_local.hashtags h WHERE h.tag=$1`,tag).Scan(&item)
 if err==pgx.ErrNoRows{http.NotFound(w,r);return};if err!=nil{writeJSON(w,503,errorBody("HASHTAG_LOOKUP_FAILED","Hashtag could not be loaded",id));return};writeJSON(w,200,map[string]any{"ok":true,"data":item})
}
func (s *service) hashtagPostsList(w http.ResponseWriter,r *http.Request){
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return};if s.db==nil{writeJSON(w,503,errorBody("HASHTAG_SERVICE_UNAVAILABLE","Hashtag service is unavailable",id));return}
 hid:=r.PathValue("hashtag_id");if !socialUUIDPattern.MatchString(hid){writeJSON(w,400,errorBody("INVALID_HASHTAG_ID","Hashtag ID is invalid",id));return}
 uid:="";if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer "){var ok bool;uid,_,ok=s.socialAuth(w,r);if !ok{return}}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}};offset:=0;if raw:=r.URL.Query().Get("offset");raw!=""{if n,e:=strconv.Atoi(raw);e==nil&&n>0{offset=n};if offset>10000{offset=10000}}
 order:="p.created_at DESC,p.id DESC";if r.URL.Query().Get("sort")=="top"{order="p.likes_count DESC,p.created_at DESC,p.id DESC"}
 query:=`SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,p.created_at,p.likes_count,p.replies_count,a.username,a.display_name,a.avatar_key,EXISTS(SELECT 1 FROM testagram_local.post_reactions mine WHERE mine.post_id=p.id AND mine.user_id=NULLIF($1,'')::uuid)
 FROM testagram_local.post_hashtags ph JOIN testagram_local.posts p ON p.id=ph.post_id JOIN testagram_local.accounts a ON a.user_id=p.author_id
 WHERE ph.hashtag_id=$2::uuid AND p.deleted_at IS NULL AND (p.visibility='public' OR p.author_id=NULLIF($1,'')::uuid)
 ORDER BY `+order+` LIMIT $3 OFFSET $4`
 rows,err:=s.db.Query(r.Context(),query,uid,hid,limit,offset);if err!=nil{writeJSON(w,503,errorBody("HASHTAG_POSTS_FAILED","Hashtag posts could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0);for rows.Next(){var pid,author,content,visibility,username,display,avatar string;var media []byte;var created time.Time;var likes,replies int64;var liked bool
 if err=rows.Scan(&pid,&author,&content,&visibility,&media,&created,&likes,&replies,&username,&display,&avatar,&liked);err!=nil{writeJSON(w,503,errorBody("HASHTAG_POSTS_FAILED","Hashtag post records could not be read",id));return};var mediaIDs []string;_ = json.Unmarshal(media,&mediaIDs);profile:=map[string]any{"id":author,"username":username,"display_name":display,"avatar_url":avatar};items=append(items,map[string]any{"id":pid,"author_id":author,"user_id":author,"text":content,"content":content,"visibility":visibility,"media_ids":mediaIDs,"created_at":created,"likes_count":likes,"replies_count":replies,"liked_by_me":liked,"user_profiles":profile,"profiles":profile})}
 writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}
func (s *service) hashtagsTrending(w http.ResponseWriter,r *http.Request){
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return};if s.db==nil{writeJSON(w,503,errorBody("HASHTAG_SERVICE_UNAVAILABLE","Hashtag service is unavailable",id));return}
 limit:=50;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 rows,err:=s.db.Query(r.Context(),`SELECT id::text,tag,usage_count,follower_count,federated_post_count,last_used_at FROM testagram_local.hashtags ORDER BY usage_count DESC,follower_count DESC,last_used_at DESC NULLS LAST LIMIT $1`,limit);if err!=nil{writeJSON(w,503,errorBody("HASHTAG_TRENDING_FAILED","Trending hashtags could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0);for rows.Next(){var hid,tag string;var usage,followers,fed int64;var last *time.Time;if err=rows.Scan(&hid,&tag,&usage,&followers,&fed,&last);err!=nil{writeJSON(w,503,errorBody("HASHTAG_TRENDING_FAILED","Trending hashtag records could not be read",id));return};items=append(items,map[string]any{"id":hid,"tag":tag,"usage_count":usage,"post_count":usage,"follower_count":followers,"federated_post_count":fed,"last_used_at":last})}
 writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"items":items}})
}
func (s *service) hashtagFollowedList(w http.ResponseWriter,r *http.Request){
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};rows,err:=s.db.Query(r.Context(),`SELECT h.id::text,h.tag,h.usage_count,h.follower_count,h.federated_post_count,h.last_used_at FROM testagram_local.hashtag_follows f JOIN testagram_local.hashtags h ON h.id=f.hashtag_id WHERE f.user_id=$1::uuid ORDER BY f.created_at DESC LIMIT 200`,uid);if err!=nil{writeJSON(w,503,errorBody("HASHTAG_FOLLOWS_FAILED","Followed hashtags could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0);for rows.Next(){var hid,tag string;var usage,followers,fed int64;var last *time.Time;if err=rows.Scan(&hid,&tag,&usage,&followers,&fed,&last);err!=nil{writeJSON(w,503,errorBody("HASHTAG_FOLLOWS_FAILED","Followed hashtag records could not be read",id));return};items=append(items,map[string]any{"id":hid,"tag":tag,"usage_count":usage,"follower_count":followers,"federated_post_count":fed,"last_used_at":last})}
 writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"items":items}})
}
func (s *service) hashtagFollowState(w http.ResponseWriter,r *http.Request){
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};hid:=r.PathValue("hashtag_id");if !socialUUIDPattern.MatchString(hid){writeJSON(w,400,errorBody("INVALID_HASHTAG_ID","Hashtag ID is invalid",id));return};var following bool
 if err:=s.db.QueryRow(r.Context(),`SELECT EXISTS(SELECT 1 FROM testagram_local.hashtag_follows WHERE user_id=$1::uuid AND hashtag_id=$2::uuid)`,uid,hid).Scan(&following);err!=nil{writeJSON(w,503,errorBody("HASHTAG_FOLLOW_STATE_FAILED","Follow state could not be loaded",id));return};writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"following":following}})
}
func (s *service) hashtagFollowCreate(w http.ResponseWriter,r *http.Request){
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"hashtag_follow",60){return};hid:=r.PathValue("hashtag_id");if !socialUUIDPattern.MatchString(hid){writeJSON(w,400,errorBody("INVALID_HASHTAG_ID","Hashtag ID is invalid",id));return}
 tag,err:=s.db.Exec(r.Context(),`INSERT INTO testagram_local.hashtag_follows(user_id,hashtag_id) VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`,uid,hid);if err!=nil{writeJSON(w,503,errorBody("HASHTAG_FOLLOW_FAILED","Hashtag could not be followed",id));return};writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"following":true,"created":tag.RowsAffected()>0}})
}
func (s *service) hashtagFollowDelete(w http.ResponseWriter,r *http.Request){
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};hid:=r.PathValue("hashtag_id");if !socialUUIDPattern.MatchString(hid){writeJSON(w,400,errorBody("INVALID_HASHTAG_ID","Hashtag ID is invalid",id));return};if _,err:=s.db.Exec(r.Context(),`DELETE FROM testagram_local.hashtag_follows WHERE user_id=$1::uuid AND hashtag_id=$2::uuid`,uid,hid);err!=nil{writeJSON(w,503,errorBody("HASHTAG_UNFOLLOW_FAILED","Hashtag could not be unfollowed",id));return};writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"following":false}})
}
