package main

import (
 "net/http"
 "regexp"
 "strconv"
 "strings"
 "time"
 "encoding/json"
)

func (s *service) discoveryCreators(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_UNAVAILABLE","Discovery service is unavailable",id));return}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 q:=strings.TrimSpace(r.URL.Query().Get("q"));if len(q)>100{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_DISCOVERY_QUERY","Search query is too long",id));return}
 rows,err:=s.db.Query(r.Context(),`SELECT user_id::text,username,display_name,avatar_key,bio,follower_count,verified,verified_tier,creator_tier
 FROM testagram_local.accounts WHERE account_status='active' AND discoverable_by_username AND NOT protected_account AND is_creator=TRUE
 AND ($1='' OR username ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%')
 ORDER BY follower_count DESC,created_at DESC LIMIT $2`,q,limit)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_CREATORS_FAILED","Creators could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var uid,username,display,avatar,bio,verifiedTier,creatorTier string;var followers int64;var verified bool
  if err=rows.Scan(&uid,&username,&display,&avatar,&bio,&followers,&verified,&verifiedTier,&creatorTier);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_CREATORS_FAILED","Creator records could not be read",id));return}
  items=append(items,map[string]any{"id":uid,"username":username,"display_name":display,"avatar_url":avatar,"bio":bio,"follower_count":followers,"verified":verified,"verified_tier":verifiedTier,"is_creator":true,"creator_tier":creatorTier})
 }
 if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_CREATORS_FAILED","Creator records could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) discoveryPosts(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("DISCOVERY_UNAVAILABLE", "Discovery service is unavailable", id))
		return
	}

	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_DISCOVERY_LIMIT", "Limit must be between 1 and 100", id))
			return
		}
		limit = n
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 100000 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_DISCOVERY_OFFSET", "Offset must be between 0 and 100000", id))
			return
		}
		offset = n
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_DISCOVERY_QUERY", "Search query is too long", id))
		return
	}
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	order := "p.created_at DESC,p.id DESC"
	switch sortBy {
	case "", "recent":
	case "top":
		order = "p.likes_count DESC,p.created_at DESC,p.id DESC"
	case "views":
		order = "p.views_count DESC,p.created_at DESC,p.id DESC"
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_DISCOVERY_SORT", "Sort must be recent, top or views", id))
		return
	}
	since := time.Now().UTC().Add(-48 * time.Hour)
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_DISCOVERY_DATE", "Discovery date is invalid", id))
			return
		}
		since = parsed.UTC()
	}
	media := strings.TrimSpace(r.URL.Query().Get("media"))
	if media != "" && media != "video" && media != "image" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_DISCOVERY_MEDIA", "Media must be video or image", id))
		return
	}
	feed := strings.TrimSpace(r.URL.Query().Get("feed"))
	if feed != "" && feed != "following" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_DISCOVERY_FEED", "Feed must be following", id))
		return
	}
	userID := ""
	if feed == "following" {
		var ok bool
		userID, _, ok = s.socialAuth(w, r)
		if !ok {
			return
		}
	}
	keywords := strings.Split(strings.ToLower(r.URL.Query().Get("keywords")), ",")
	patterns := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword != "" && len(keyword) <= 64 {
			patterns = append(patterns, "%"+keyword+"%")
		}
	}

	query := `
		SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,p.is_video,
		       p.views_count,p.created_at,p.updated_at,p.likes_count,p.replies_count,p.reposts_count,
		       a.username,a.display_name,a.avatar_key
		FROM testagram_local.posts p
		JOIN testagram_local.accounts a ON a.user_id=p.author_id
		WHERE p.deleted_at IS NULL AND p.visibility='public' AND p.community_id IS NULL
		  AND p.created_at >= $1
		  AND (($2='' AND cardinality($5::text[])=0) OR p.text ILIKE '%'||$2||'%'
		       OR a.username ILIKE '%'||$2||'%'
		       OR EXISTS(SELECT 1 FROM unnest($5::text[]) term WHERE p.text ILIKE term))
		  AND ($4='' OR ($4='video' AND p.is_video=TRUE) OR ($4='image' AND p.is_video=FALSE))
		  AND ($6<>'following' OR EXISTS(
		       SELECT 1 FROM testagram_local.follows f
		       WHERE f.follower_id=$7::uuid AND f.following_id=p.author_id AND f.status='accepted'))
		ORDER BY ` + order + `
		LIMIT $3 OFFSET $8
	`
	rows, err := s.db.Query(r.Context(), query, since, q, limit, media, patterns, feed, userID, offset)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("DISCOVERY_POSTS_FAILED", "Posts could not be loaded", id))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var postID, authorID, content, visibility, username, displayName, avatar string
		var mediaJSON []byte
		var video bool
		var views, likes, replies, reposts int64
		var created, updated time.Time
		if err := rows.Scan(&postID, &authorID, &content, &visibility, &mediaJSON, &video,
			&views, &created, &updated, &likes, &replies, &reposts,
			&username, &displayName, &avatar); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("DISCOVERY_POSTS_FAILED", "Post records could not be read", id))
			return
		}
		var mediaIDs []string
		_ = json.Unmarshal(mediaJSON, &mediaIDs)
		profile := map[string]any{"id": authorID, "username": username, "display_name": displayName, "avatar_url": avatar, "verified": false}
		items = append(items, map[string]any{
			"id": postID, "user_id": authorID, "author_id": authorID,
			"content": content, "text": content, "visibility": visibility,
			"media_ids": mediaIDs, "media_urls": []string{}, "is_video": video,
			"views_count": views, "created_at": created, "updated_at": updated,
			"likes_count": likes, "replies_count": replies, "reposts_count": reposts,
			"user_profiles": profile,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("DISCOVERY_POSTS_FAILED", "Post records could not be read", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items, "next_cursor": nil}})
}

func (s *service) discoveryThreads(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return};if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_UNAVAILABLE","Discovery service is unavailable",id));return}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 rows,err:=s.db.Query(r.Context(),`SELECT t.id::text,t.owner_id::text,t.title,t.content,t.likes_count,t.replies_count,t.created_at,a.username,a.display_name,a.avatar_key,a.verified_tier
 FROM testagram_local.threads t JOIN testagram_local.accounts a ON a.user_id=t.owner_id
 WHERE t.is_published AND t.deleted_at IS NULL AND a.account_status='active' AND a.discoverable_by_username AND NOT a.protected_account
 ORDER BY t.likes_count DESC,t.created_at DESC LIMIT $1`,limit)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_THREADS_FAILED","Threads could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0);for rows.Next(){var tid,owner,title,content,username,display,avatar,tier string;var likes,replies int64;var created time.Time
 if err=rows.Scan(&tid,&owner,&title,&content,&likes,&replies,&created,&username,&display,&avatar,&tier);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_THREADS_FAILED","Thread records could not be read",id));return}
 profile:=map[string]any{"id":owner,"username":username,"display_name":display,"avatar_url":avatar,"verified_tier":tier}
 items=append(items,map[string]any{"id":tid,"owner_id":owner,"title":title,"content":content,"likes_count":likes,"replies_count":replies,"created_at":created,"user_profiles":profile})
 }
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) discoveryPostCount(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_UNAVAILABLE","Discovery service is unavailable",id));return}
 raw:=strings.Split(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("terms"))),",")
 terms:=make([]string,0,len(raw));for _,term:=range raw{term=strings.TrimSpace(term);if term==""{continue};if len(term)>32||!regexp.MustCompile(`^[a-z0-9 -]+$`).MatchString(term){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_DISCOVERY_TERMS","Discovery terms are invalid",id));return};terms=append(terms,term)}
 if len(terms)==0||len(terms)>20{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_DISCOVERY_TERMS","Between 1 and 20 search terms are required",id));return}
 var count int64
 err:=s.db.QueryRow(r.Context(),`SELECT count(*) FROM testagram_local.posts p
 WHERE p.deleted_at IS NULL AND p.visibility='public' AND p.community_id IS NULL AND p.created_at>=now()-interval '7 days'
 AND EXISTS(SELECT 1 FROM unnest($1::text[]) term WHERE p.text ILIKE '%'||term||'%')`,terms).Scan(&count)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_COUNT_FAILED","Discovery counts could not be loaded",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"count":count}})
}
