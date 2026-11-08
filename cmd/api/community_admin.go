package main

import (
 "encoding/json"
 "net/http"
 "strings"
 "strconv"
 "time"

 "github.com/jackc/pgx/v5"
)

func (s *service) communityGet(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_DATABASE_UNAVAILABLE","Community service is unavailable",id));return}
 slug:=strings.TrimSpace(strings.TrimPrefix(r.PathValue("community_slug"),"c/"));if slug==""{slug=strings.TrimSpace(r.PathValue("community_id"))}
 uid:="";if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer "){var ok bool;uid,_,ok=s.socialAuth(w,r);if !ok{return}}
 var cid,owner,slugValue,name,description,visibility,icon,banner string
 var members int64;var created time.Time;var rulesJSON,pinsJSON []byte;var isMember bool;var role *string;var posts int64
 err:=s.db.QueryRow(r.Context(),`SELECT c.id::text,c.owner_id::text,c.slug,c.name,c.description,c.visibility,c.icon_url,c.banner_url,c.member_count,c.created_at,c.rules,c.pinned_post_ids,
 EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active'),
 (SELECT m.role FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active'),
 (SELECT count(*) FROM testagram_local.posts p WHERE p.community_id=c.id AND p.deleted_at IS NULL)
 FROM testagram_local.communities c WHERE c.slug=$1 OR c.id::text=$1 LIMIT 1`,slug,uid).Scan(&cid,&owner,&slugValue,&name,&description,&visibility,&icon,&banner,&members,&created,&rulesJSON,&pinsJSON,&isMember,&role,&posts)
 if err==pgx.ErrNoRows{http.NotFound(w,r);return};if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_LOOKUP_FAILED","Community could not be loaded",id));return}
 if visibility=="private"&&!isMember{http.NotFound(w,r);return}
 var rules,pins []string;_ = json.Unmarshal(rulesJSON,&rules);_ = json.Unmarshal(pinsJSON,&pins)
 myRole:="member";if role!=nil{myRole=*role}
 community:=map[string]any{"id":cid,"name":slugValue,"slug":slugValue,"display_name":name,"description":description,"visibility":visibility,"is_private":visibility=="private","icon_url":icon,"banner_url":banner,"member_count":members,"post_count":posts,"created_by":owner,"owner_id":owner,"created_at":created,"rules":rules,"pinned_post_ids":pins,"is_member":isMember,"my_role":myRole}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"community":community}})
}

func (s *service) communityMembersList(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_DATABASE_UNAVAILABLE","Community service is unavailable",id));return}
 cid:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(cid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 uid:="";if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer "){var ok bool;uid,_,ok=s.socialAuth(w,r);if !ok{return}}
 var visible bool
 if err:=s.db.QueryRow(r.Context(),`SELECT visibility='public' OR EXISTS(SELECT 1 FROM testagram_local.community_memberships WHERE community_id=c.id AND user_id=NULLIF($2,'')::uuid AND status='active') FROM testagram_local.communities c WHERE c.id=$1::uuid`,cid,uid).Scan(&visible);err==pgx.ErrNoRows{http.NotFound(w,r);return}else if err!=nil||!visible{http.NotFound(w,r);return}
 rows,err:=s.db.Query(r.Context(),`SELECT m.user_id::text,m.role,m.joined_at,a.username,a.display_name,a.avatar_key
 FROM testagram_local.community_memberships m JOIN testagram_local.accounts a ON a.user_id=m.user_id
 WHERE m.community_id=$1::uuid AND m.status='active'
 ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'moderator' THEN 2 ELSE 3 END,m.joined_at ASC LIMIT $2`,cid,limit)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_MEMBERS_FAILED","Community members could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var uid,role,username,display,avatar string;var joined time.Time
  if err=rows.Scan(&uid,&role,&joined,&username,&display,&avatar);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_MEMBERS_FAILED","Community member records could not be read",id));return}
  profile:=map[string]any{"id":uid,"username":username,"display_name":display,"avatar_url":avatar,"verified":false}
  items=append(items,map[string]any{"id":uid,"user_id":uid,"role":role,"joined_at":joined,"username":username,"avatar_url":avatar,"user_profiles":profile})
 }
 if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_MEMBERS_FAILED","Community member records could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) communityUpdate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"community_update",10){return}
 cid:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(cid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
 var role string;if err:=s.db.QueryRow(r.Context(),"SELECT role FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'",cid,uid).Scan(&role);err!=nil||!(role=="owner"||role=="admin"){writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_ADMIN_REQUIRED","Only community owners or admins can change settings",id));return}
 r.Body=http.MaxBytesReader(w,r.Body,16<<10);defer r.Body.Close()
 var in struct{DisplayName *string `json:"display_name"`;Description *string `json:"description"`;IconURL *string `json:"icon_url"`;BannerURL *string `json:"banner_url"`;Rules *[]string `json:"rules"`;Pinned *[]string `json:"pinned_post_ids"`}
 if err:=decodeSingleJSON(r.Body,&in);err!=nil{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_UPDATE","Community settings are invalid",id));return}
 if in.DisplayName!=nil&&(len(strings.TrimSpace(*in.DisplayName))<3||len(*in.DisplayName)>80)||in.Description!=nil&&len(*in.Description)>2000||in.IconURL!=nil&&len(*in.IconURL)>2048||in.BannerURL!=nil&&len(*in.BannerURL)>2048||in.Rules!=nil&&len(*in.Rules)>20||in.Pinned!=nil&&len(*in.Pinned)>3{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_UPDATE","Community settings exceed allowed limits",id));return}
 var rulesJSON,pinsJSON any
 if in.Rules!=nil{for _,v:=range *in.Rules{if len(v)>240{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_RULES","Each rule must be at most 240 characters",id));return}};raw,_:=json.Marshal(*in.Rules);rulesJSON=string(raw)}
 if in.Pinned!=nil{
 for _,v:=range *in.Pinned{if !socialUUIDPattern.MatchString(v){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_PINNED_POST","Pinned post IDs must be valid",id));return}}
 raw,_:=json.Marshal(*in.Pinned)
 var validPins int
 if err:=s.db.QueryRow(r.Context(),`SELECT count(*) FROM jsonb_array_elements_text($2::jsonb) pin(id) JOIN testagram_local.posts p ON p.id=pin.id::uuid AND p.community_id=$1::uuid AND p.deleted_at IS NULL`,cid,string(raw)).Scan(&validPins);err!=nil||validPins!=len(*in.Pinned){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_PINNED_POST","Every pinned post must belong to this community",id));return}
 pinsJSON=string(raw)
}
 name:="";description:="";icon:="";banner:=""
 if in.DisplayName!=nil{name=strings.TrimSpace(*in.DisplayName)};if in.Description!=nil{description=strings.TrimSpace(*in.Description)};if in.IconURL!=nil{icon=*in.IconURL};if in.BannerURL!=nil{banner=*in.BannerURL}
 tag,err:=s.db.Exec(r.Context(),`UPDATE testagram_local.communities SET name=COALESCE(NULLIF($2,''),name),description=CASE WHEN $3::boolean THEN $4 ELSE description END,icon_url=CASE WHEN $5::boolean THEN $6 ELSE icon_url END,banner_url=CASE WHEN $7::boolean THEN $8 ELSE banner_url END,rules=COALESCE($9::jsonb,rules),pinned_post_ids=COALESCE($10::jsonb,pinned_post_ids),updated_at=now() WHERE id=$1::uuid`,cid,name,in.Description!=nil,description,in.IconURL!=nil,icon,in.BannerURL!=nil,banner,string(rulesJSON),string(pinsJSON))
 if err!=nil||tag.RowsAffected()==0{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_UPDATE_FAILED","Community settings could not be saved",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"updated":true,"community_id":cid}})
}

func (s *service) communityMemberRoleUpdate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"community_member_role",20){return}
 cid:=r.PathValue("community_id");target:=r.PathValue("user_id")
 if !socialUUIDPattern.MatchString(cid)||!socialUUIDPattern.MatchString(target){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_MEMBER","Community or member ID is invalid",id));return}
 var actorRole string;if err:=s.db.QueryRow(r.Context(),"SELECT role FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'",cid,uid).Scan(&actorRole);err!=nil||actorRole!="owner"{writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_OWNER_REQUIRED","Only the owner can change member roles",id));return}
 r.Body=http.MaxBytesReader(w,r.Body,4<<10);defer r.Body.Close();var in struct{Role string `json:"role"`}
 if err:=decodeSingleJSON(r.Body,&in);err!=nil||(in.Role!="member"&&in.Role!="moderator"){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_MEMBER_ROLE","Role must be member or moderator",id));return}
 tag,err:=s.db.Exec(r.Context(),"UPDATE testagram_local.community_memberships SET role=$3 WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active' AND role<>'owner'",cid,target,in.Role)
 if err!=nil||tag.RowsAffected()==0{writeJSON(w,http.StatusConflict,errorBody("MEMBER_ROLE_UPDATE_FAILED","Member role could not be updated",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"user_id":target,"role":in.Role}})
}

func (s *service) communityMemberRemove(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"community_member_remove",20){return}
 cid:=r.PathValue("community_id");target:=r.PathValue("user_id")
 if !socialUUIDPattern.MatchString(cid)||!socialUUIDPattern.MatchString(target){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_MEMBER","Community or member ID is invalid",id));return}
 var actorRole string;if err:=s.db.QueryRow(r.Context(),"SELECT role FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'",cid,uid).Scan(&actorRole);err!=nil||!(actorRole=="owner"||actorRole=="admin"||actorRole=="moderator"){writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MODERATOR_REQUIRED","A community moderator is required",id));return}
 tag,err:=s.db.Exec(r.Context(),"DELETE FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND role<>'owner' AND ($3='owner' OR role='member')",cid,target,actorRole)
 if err!=nil||tag.RowsAffected()==0{writeJSON(w,http.StatusConflict,errorBody("MEMBER_REMOVE_FAILED","Member could not be removed",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"user_id":target,"removed":true}})
}

func (s *service) communityPostModerate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"community_post_moderate",20){return}
 cid:=r.PathValue("community_id");pid:=r.PathValue("post_id")
 if !socialUUIDPattern.MatchString(cid)||!socialUUIDPattern.MatchString(pid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POST","Community or post ID is invalid",id));return}
 var role string;if err:=s.db.QueryRow(r.Context(),"SELECT role FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'",cid,uid).Scan(&role);err!=nil||!(role=="owner"||role=="admin"||role=="moderator"){writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MODERATOR_REQUIRED","A community moderator is required",id));return}
 tag,err:=s.db.Exec(r.Context(),"UPDATE testagram_local.posts SET deleted_at=now(),updated_at=now() WHERE id=$1::uuid AND community_id=$2::uuid AND deleted_at IS NULL",pid,cid)
 if err!=nil||tag.RowsAffected()==0{writeJSON(w,http.StatusNotFound,errorBody("COMMUNITY_POST_NOT_FOUND","Community post was not found",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"post_id":pid,"moderated":true}})
}
