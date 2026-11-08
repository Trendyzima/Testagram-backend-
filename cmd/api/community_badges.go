package main

import (
	"net/http"
	"time"
)

func (s *service) communityBadgesList(w http.ResponseWriter,r *http.Request) {
	id:=requestID(r)
	if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
	if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_DATABASE_UNAVAILABLE","Community badge service is unavailable",id));return}
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	userID:=""
	if r.Header.Get("Authorization")!="" { var ok bool;userID,_,ok=s.socialAuth(w,r);if !ok{return} }
	var canView bool
	err:=s.db.QueryRow(r.Context(),`SELECT (c.visibility='public' OR EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active')) FROM testagram_local.communities c WHERE c.id=$1::uuid`,communityID,userID).Scan(&canView)
	if err!=nil||!canView{writeJSON(w,http.StatusNotFound,errorBody("COMMUNITY_NOT_FOUND","Community was not found",id));return}
	rows,err:=s.db.Query(r.Context(),`SELECT b.id::text,b.owner_id::text,b.badge_type,b.minted_at,a.username,a.display_name,a.avatar_key FROM testagram_local.community_badges b JOIN testagram_local.accounts a ON a.user_id=b.owner_id WHERE b.community_id=$1::uuid ORDER BY b.minted_at DESC,b.id DESC LIMIT 100`,communityID)
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGES_FAILED","Community badges could not be loaded",id));return}
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next(){var badgeID,ownerID,badgeType,username,displayName,avatar string;var minted time.Time
		if err=rows.Scan(&badgeID,&ownerID,&badgeType,&minted,&username,&displayName,&avatar);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGES_FAILED","Community badge records could not be read",id));return}
		profile:=map[string]any{"id":ownerID,"username":username,"display_name":displayName,"avatar_url":avatar}
		items=append(items,map[string]any{"id":badgeID,"community_id":communityID,"owner_id":ownerID,"badge_type":badgeType,"minted_at":minted,"user_profiles":profile})
	}
	if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_BADGES_FAILED","Community badge records could not be read",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items}})
}

func (s *service) communityBadgeMint(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_badge_mint",5){return}
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	var badgeID,badgeType string
	err:=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.community_badges(community_id,owner_id,badge_type)
		SELECT c.id,$2::uuid,CASE WHEN c.owner_id=$2::uuid THEN 'founder' ELSE 'member' END
		FROM testagram_local.communities c WHERE c.id=$1::uuid AND EXISTS(
			SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=$2::uuid AND m.status='active')
		ON CONFLICT(community_id,owner_id) DO UPDATE SET owner_id=EXCLUDED.owner_id
		RETURNING id::text,badge_type`,communityID,userID).Scan(&badgeID,&badgeType)
	if err!=nil{writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Join this community before minting its badge",id));return}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":badgeID,"community_id":communityID,"owner_id":userID,"badge_type":badgeType,"minted":true}})
}
