package main

import (
	"context"
	"net/http"
	"strconv"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type communityCreateInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	IconURL     string `json:"icon_url"`
	BannerURL   string `json:"banner_url"`
	Slug        string `json:"slug"`
	Rules       []string `json:"rules"`
}

var communitySlugPattern = regexp.MustCompile(`[^a-z0-9_-]+`)

func communitySlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = communitySlugPattern.ReplaceAllString(slug, "")
	slug = strings.Trim(slug, "-_")
	if len(slug) > 40 { slug = strings.Trim(slug[:40], "-_") }
	return slug
}

func (s *service) communitiesList(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w,r) { writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id)); return }
	if s.db == nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_DATABASE_UNAVAILABLE","Community data service is unavailable",id)); return }
	limit := 20
	if raw:=r.URL.Query().Get("limit"); raw!="" { if n,err:=strconv.Atoi(raw); err==nil { if n<1 {n=1}; if n>100 {n=100}; limit=n } }
	offset:=0
	if raw:=r.URL.Query().Get("page"); raw!="" { if n,err:=strconv.Atoi(raw); err==nil && n>0 { offset=n*limit } }
	search:=strings.TrimSpace(r.URL.Query().Get("q"))
	tab:=r.URL.Query().Get("tab")
	userID := ""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer ") {
		var ok bool
		userID,_,ok=s.socialAuth(w,r)
		if !ok { return }
	}
	sortBy := r.URL.Query().Get("sort")
	order := "c.member_count DESC,c.created_at DESC,c.id DESC"
	if sortBy=="newest" { order="c.created_at DESC,c.id DESC" }
	if sortBy=="trending" { order="c.created_at DESC,c.member_count DESC,c.id DESC" }
	query := `SELECT c.id::text,c.slug,c.name,c.description,c.visibility,c.icon_url,c.banner_url,c.member_count,c.owner_id::text,c.created_at,c.rules,
		EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($1,'')::uuid AND m.status='active')
		FROM testagram_local.communities c WHERE (($3='joined' AND EXISTS(SELECT 1 FROM testagram_local.community_memberships mine WHERE mine.community_id=c.id AND mine.user_id=NULLIF($1,'')::uuid AND mine.status='active')) OR ($3<>'joined' AND c.visibility='public' AND ($4='' OR c.name ILIKE '%'||$4||'%' OR c.slug ILIKE '%'||$4||'%')) AND ($3<>'joined' OR c.visibility='public')) ORDER BY `+order+` LIMIT $2 OFFSET $5`
	rows,err:=s.db.Query(r.Context(),query,userID,limit,tab,search,offset)
	if err!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_LIST_FAILED","Communities could not be loaded",id)); return }
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next() {
		var cid,slug,name,description,visibility,icon,banner,owner,created string
		var members int64
		var createdAt time.Time
		var isMember bool
		if err=rows.Scan(&cid,&slug,&name,&description,&visibility,&icon,&banner,&members,&owner,&createdAt,&rules,&isMember); err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_LIST_FAILED","Community records could not be read",id)); return
		}
		var parsedRules []string
		_ = json.Unmarshal(rules,&parsedRules)
		items=append(items,map[string]any{"id":cid,"name":slug,"slug":slug,"display_name":name,"description":description,"visibility":visibility,"is_private":visibility=="private","icon_url":icon,"banner_url":banner,"member_count":members,"post_count":0,"rules":parsedRules,"created_by":owner,"owner_id":owner,"created_at":createdAt,"is_member":isMember})
	}
	if err=rows.Err(); err!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_LIST_FAILED","Community records could not be read",id)); return }
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) communitiesCreate(w http.ResponseWriter, r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r); if !ok { return }
	if !s.socialWriteAllowed(w,r,userID,id,"community_create",5) { return }
	r.Body=http.MaxBytesReader(w,r.Body,16<<10); defer r.Body.Close()
	var in communityCreateInput
	if err:=decodeSingleJSON(r.Body,&in); err!=nil || len(strings.TrimSpace(in.Name))<3 || len(in.Name)>80 || len(in.Description)>2000 || len(in.IconURL)>2048 || len(in.BannerURL)>2048 {
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY","Community fields are invalid",id)); return
	}
	if in.Visibility=="" { in.Visibility="public" }
	if in.Visibility!="public" && in.Visibility!="private" { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_VISIBILITY","Visibility must be public or private",id)); return }
	slug:=communitySlug(in.Name)
	if strings.TrimSpace(in.Slug)!="" { slug=communitySlug(in.Slug) }
	for _, rule := range in.Rules { if len(rule)>240 { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_RULES","Community rules must be at most 240 characters each",id)); return } }
	rulesJSON,_:=json.Marshal(in.Rules)
	if len(slug)<3 { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_NAME","Community name must produce a valid URL slug",id)); return }
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
	tx,err:=s.db.Begin(ctx); if err!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_CREATE_FAILED","Community could not be created",id)); return }; defer tx.Rollback(ctx)
	var communityID string
	err=tx.QueryRow(ctx,`INSERT INTO testagram_local.communities(owner_id,slug,name,description,visibility,icon_url,banner_url,rules)
		VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8::jsonb) RETURNING id::text`,userID,slug,strings.TrimSpace(in.Name),strings.TrimSpace(in.Description),in.Visibility,strings.TrimSpace(in.IconURL),strings.TrimSpace(in.BannerURL),string(rulesJSON)).Scan(&communityID)
	if err!=nil { writeJSON(w,http.StatusConflict,errorBody("COMMUNITY_CREATE_CONFLICT","Community name is already in use or could not be saved",id)); return }
	_,err=tx.Exec(ctx,`INSERT INTO testagram_local.community_memberships(community_id,user_id,role,status) VALUES($1::uuid,$2::uuid,'owner','active')`,communityID,userID)
	if err!=nil { writeJSON(w,http.StatusConflict,errorBody("COMMUNITY_OWNER_MEMBERSHIP_FAILED","Community owner membership could not be created",id)); return }
	if err=tx.Commit(ctx); err!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_CREATE_FAILED","Community transaction could not be committed",id)); return }
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"community":map[string]any{"id":communityID,"name":slug,"slug":slug,"display_name":strings.TrimSpace(in.Name),"description":strings.TrimSpace(in.Description),"visibility":in.Visibility,"is_private":in.Visibility=="private","icon_url":strings.TrimSpace(in.IconURL),"banner_url":strings.TrimSpace(in.BannerURL),"rules":in.Rules,"post_count":0,"member_count":1,"owner_id":userID,"is_member":true}}})
}

func (s *service) communityJoin(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r); if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_join",30){return}
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second); defer cancel()
	var visibility string
	err:=s.db.QueryRow(ctx,"SELECT visibility FROM testagram_local.communities WHERE id=$1::uuid",communityID).Scan(&visibility)
	if err==pgx.ErrNoRows { http.NotFound(w,r); return }; if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_LOOKUP_FAILED","Community is unavailable",id));return}
	if visibility!="public" {writeJSON(w,http.StatusForbidden,errorBody("PRIVATE_COMMUNITY","This community requires an invitation",id));return}
	tag,err:=s.db.Exec(ctx,`INSERT INTO testagram_local.community_memberships(community_id,user_id,role,status) VALUES($1::uuid,$2::uuid,'member','active')
		ON CONFLICT(community_id,user_id) DO UPDATE SET status='active' WHERE testagram_local.community_memberships.status<>'banned'`,communityID,userID)
	if err!=nil {writeJSON(w,http.StatusConflict,errorBody("COMMUNITY_JOIN_FAILED","Community membership could not be saved",id));return}
	if tag.RowsAffected()==0 {writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_BANNED","You cannot join this community",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"community_id":communityID,"joined":true}})
}

func (s *service) communityLeave(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r); if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_leave",30){return}
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second); defer cancel()
	tag,err:=s.db.Exec(ctx,`DELETE FROM testagram_local.community_memberships m USING testagram_local.communities c
		WHERE m.community_id=c.id AND m.community_id=$1::uuid AND m.user_id=$2::uuid AND m.role<>'owner'`,communityID,userID)
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_LEAVE_FAILED","Community membership could not be removed",id));return}
	if tag.RowsAffected()==0 {
		var owner bool
		_ = s.db.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND role='owner')",communityID,userID).Scan(&owner)
		if owner {writeJSON(w,http.StatusConflict,errorBody("COMMUNITY_OWNER_REQUIRED","Transfer community ownership before leaving",id));return}
	}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"community_id":communityID,"left":true}})
}
