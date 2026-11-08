package main

import (
 "net/http"
 "strings"
 "github.com/jackc/pgx/v5"
)

func (s *service) communityShopList(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MARKETPLACE_UNAVAILABLE","Marketplace is unavailable",id));return}
 communityID:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
 uid:=""
 if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer "){var ok bool;uid,_,ok=s.socialAuth(w,r);if !ok{return}}
 var keyword,visibility string
 if err:=s.db.QueryRow(r.Context(),`SELECT split_part(c.name,' ',1),c.visibility FROM testagram_local.communities c WHERE c.id=$1::uuid AND (c.visibility='public' OR EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active'))`,communityID,uid).Scan(&keyword,&visibility);err==pgx.ErrNoRows{http.NotFound(w,r);return}else if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_SHOP_FAILED","Community shop could not be loaded",id));return}
 rows,err:=s.db.Query(r.Context(),`SELECT p.id::text,p.name,p.description,p.price::text,p.image_url,p.avg_rating,p.review_count,p.external_link,p.owner_id::text,a.username,a.avatar_key
 FROM testagram_local.marketplace_products p JOIN testagram_local.accounts a ON a.user_id=p.owner_id
 WHERE p.is_active AND (p.is_featured OR p.name ILIKE '%'||$1||'%')
 ORDER BY p.is_featured DESC,p.sales_count DESC,p.created_at DESC LIMIT 30`,keyword)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_SHOP_FAILED","Marketplace products could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var pid,name,description,price,image,external,owner,username,avatar string;var rating float64;var reviews int64
  if err=rows.Scan(&pid,&name,&description,&price,&image,&rating,&reviews,&external,&owner,&username,&avatar);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_SHOP_FAILED","Marketplace product records could not be read",id));return}
  items=append(items,map[string]any{"id":pid,"name":name,"description":description,"price":price,"image_url":image,"avg_rating":rating,"review_count":reviews,"external_link":external,"user_id":owner,"user_profiles":map[string]any{"username":username,"avatar_url":avatar}})
 }
 if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_SHOP_FAILED","Marketplace product records could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) communityBadgesList(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGES_UNAVAILABLE","Community badges are unavailable",id));return}
 communityID:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
 uid:="";if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer "){var ok bool;uid,_,ok=s.socialAuth(w,r);if !ok{return}}
 var visible bool
 if err:=s.db.QueryRow(r.Context(),`SELECT c.visibility='public' OR EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active') FROM testagram_local.communities c WHERE c.id=$1::uuid`,communityID,uid).Scan(&visible);err==pgx.ErrNoRows||err!=nil||!visible{http.NotFound(w,r);return}
 rows,err:=s.db.Query(r.Context(),`SELECT b.id::text,b.owner_id::text,b.badge_tier,b.contribution_score,b.minted_at,a.username,a.avatar_key
 FROM testagram_local.community_badges b JOIN testagram_local.accounts a ON a.user_id=b.owner_id
 WHERE b.community_id=$1::uuid ORDER BY b.issued_at DESC LIMIT 100`,communityID)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGES_FAILED","Community badges could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var bid,owner,tier,username,avatar string;var score int64;var issued time.Time
  if err=rows.Scan(&bid,&owner,&tier,&score,&issued,&username,&avatar);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGES_FAILED","Badge records could not be read",id));return}
  items=append(items,map[string]any{"id":bid,"owner_id":owner,"badge_tier":tier,"badge_emoji":map[string]string{"rare":"🏅","epic":"🔮","legendary":"💎"}[tier],"contribution_score":score,"minted_at":issued,"user_profiles":map[string]any{"username":username,"avatar_url":avatar}})
 }
 if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGES_FAILED","Badge records could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) communityBadgeClaim(w http.ResponseWriter,r *http.Request) {
 userID,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,userID,id,"community_badge_claim",5){return}
 communityID:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
 var score int64
 err:=s.db.QueryRow(r.Context(),`SELECT count(*)+COALESCE(sum(p.likes_count),0) FROM testagram_local.posts p
 WHERE p.community_id=$1::uuid AND p.author_id=$2::uuid AND p.deleted_at IS NULL
 AND EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=$1::uuid AND m.user_id=$2::uuid AND m.status='active')`,communityID,userID).Scan(&score)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGE_SCORE_FAILED","Community contribution could not be calculated",id));return}
 if score<1{writeJSON(w,http.StatusConflict,errorBody("BADGE_REQUIREMENTS_NOT_MET","Publish a community post to qualify for a badge",id));return}
 tier:="rare";if score>=10{tier="legendary"}else if score>=5{tier="epic"}
 var badgeID string;var issued time.Time
 err=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.community_badges(community_id,owner_id,badge_tier,contribution_score)
 VALUES($1::uuid,$2::uuid,$3,$4) ON CONFLICT(community_id,owner_id) DO UPDATE SET badge_tier=EXCLUDED.badge_tier,contribution_score=EXCLUDED.contribution_score
 RETURNING id::text,minted_at`,communityID,userID,tier,score).Scan(&badgeID,&issued)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGE_CLAIM_FAILED","Community badge could not be issued",id));return}
 writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":badgeID,"community_id":communityID,"owner_id":userID,"badge_tier":tier,"badge_emoji":map[string]string{"rare":"🏅","epic":"🔮","legendary":"💎"}[tier],"contribution_score":score,"minted_at":issued,"issued":true}})
}

