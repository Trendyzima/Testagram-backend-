package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type feedPreferenceInput struct {
	DefaultFeed string `json:"default_feed"`
}

func validDefaultFeed(feed string) bool {
	switch feed {
	case "for-you","following","latest","media":
		return true
	default:
		return false
	}
}

func (s *service) feedPreferences(w http.ResponseWriter,r *http.Request) {
	rid:=requestID(r)
	if !s.cors(w,r) { writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",rid));return }
	if s.db==nil { writeJSON(w,503,errorBody("SOCIAL_DATABASE_UNAVAILABLE","Social data service is unavailable",rid));return }
	userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second);defer cancel()
	switch r.Method {
	case http.MethodGet:
		var feed string;var updated time.Time
		err:=s.db.QueryRow(ctx,`SELECT default_feed,updated_at FROM testagram_local.user_feed_preferences WHERE user_id=$1`,userID).Scan(&feed,&updated)
		if errors.Is(err,pgx.ErrNoRows) {
			feed="for-you"
			updated=time.Time{}
		} else if err!=nil {
			writeJSON(w,503,errorBody("FEED_PREFERENCE_READ_FAILED","Feed preference could not be loaded",rid));return
		}
		writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"default_feed":feed,"available_feeds":[]string{"for-you","following","latest","media"},"updated_at":updated},"error":nil,"request_id":rid})
	case http.MethodPut:
		if !s.socialWriteAllowed(w,r,userID,rid,"feed-preference",20) { return }
		var in feedPreferenceInput
		if err:=decodeMediaJSON(w,r,&in);err!=nil {
			writeJSON(w,400,errorBody("INVALID_FEED_PREFERENCE","Feed preference payload is invalid",rid));return
		}
		in.DefaultFeed=strings.ToLower(strings.TrimSpace(in.DefaultFeed))
		if !validDefaultFeed(in.DefaultFeed) {
			writeJSON(w,400,errorBody("INVALID_FEED_PREFERENCE","Choose for-you, following, latest or media",rid));return
		}
		var updated time.Time
		err:=s.db.QueryRow(ctx,`INSERT INTO testagram_local.user_feed_preferences(user_id,default_feed)
			VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET default_feed=EXCLUDED.default_feed,updated_at=now()
			RETURNING updated_at`,userID,in.DefaultFeed).Scan(&updated)
		if err!=nil { writeJSON(w,503,errorBody("FEED_PREFERENCE_SAVE_FAILED","Feed preference could not be saved",rid));return }
		writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"default_feed":in.DefaultFeed,"updated_at":updated},"error":nil,"request_id":rid})
	default:
		w.Header().Set("Allow","GET, PUT")
		writeJSON(w,405,errorBody("METHOD_NOT_ALLOWED","Use GET or PUT",rid))
	}
}
