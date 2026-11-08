package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

// nativeSocialCapability routes capability-client operations to the same
// authenticated Neon handlers exposed as first-class /api/social endpoints.
// It deliberately reuses those handlers so authorization, privacy checks,
// rate limits, idempotency and transaction semantics have one implementation.
func (s *service) nativeSocialCapability(w http.ResponseWriter, r *http.Request, requestID string, body capabilityRequest) bool {
	capability := strings.TrimSpace(body.Capability)
	postID, _ := body.Input["post_id"].(string)
	userID, _ := body.Input["target_user_id"].(string)
	notificationID, _ := body.Input["notification_id"].(string)

	type dispatch struct {
		method string
		path string
		handler http.HandlerFunc
		pathValues map[string]string
		query url.Values
		payload map[string]any
	}
	var d dispatch
	switch capability {
	case "testagram.posts.like", "testagram.posts.like.add":
		d = dispatch{method:http.MethodPost,path:"/api/social/posts/"+postID+"/reaction",handler:s.socialReact,pathValues:map[string]string{"post_id":postID},payload:map[string]any{"emoji":body.Input["emoji"]}}
	case "testagram.posts.like.remove":
		d = dispatch{method:http.MethodDelete,path:"/api/social/posts/"+postID+"/reaction",handler:s.socialUnreact,pathValues:map[string]string{"post_id":postID}}
	case "testagram.posts.like.state":
		d = dispatch{method:http.MethodGet,path:"/api/social/posts/"+postID+"/reaction",handler:s.socialReactionState,pathValues:map[string]string{"post_id":postID}}
	case "testagram.posts.repost":
		d = dispatch{method:http.MethodPost,path:"/api/social/posts/"+postID+"/reposts",handler:s.socialRepost,pathValues:map[string]string{"post_id":postID},payload:map[string]any{"quote":body.Input["quote"]}}
	case "testagram.posts.repost.remove":
		d = dispatch{method:http.MethodDelete,path:"/api/social/posts/"+postID+"/reposts",handler:s.socialUnrepost,pathValues:map[string]string{"post_id":postID}}
	case "testagram.posts.repost.state":
		d = dispatch{method:http.MethodGet,path:"/api/social/posts/"+postID+"/reposts",handler:s.socialRepostState,pathValues:map[string]string{"post_id":postID}}
	case "testagram.replies.create":
		d = dispatch{method:http.MethodPost,path:"/api/social/posts/"+postID+"/replies",handler:s.socialReply,pathValues:map[string]string{"post_id":postID},payload:map[string]any{"content":body.Input["content"],"parent_reply_id":body.Input["parent_reply_id"]}}
	case "testagram.bookmarks.add":
		if strings.Contains(postID, "://") { return false } // remote objects belong to Federation
		d = dispatch{method:http.MethodPost,path:"/api/social/posts/"+postID+"/bookmarks",handler:s.socialBookmark,pathValues:map[string]string{"post_id":postID}}
	case "testagram.bookmarks.remove":
		if strings.Contains(postID, "://") { return false }
		d = dispatch{method:http.MethodDelete,path:"/api/social/posts/"+postID+"/bookmarks",handler:s.socialUnbookmark,pathValues:map[string]string{"post_id":postID}}
	case "testagram.notifications.list", "testagram.notifications.rank":
		query := url.Values{}
		if v, ok := body.Input["limit"]; ok { query.Set("limit", strings.TrimSpace(strings.TrimSpace(toString(v)))) }
		if v, ok := body.Input["cursor"]; ok { query.Set("cursor", toString(v)) }
		d = dispatch{method:http.MethodGet,path:"/api/social/notifications",handler:s.socialNotifications,query:query}
	case "testagram.notifications.mark_read":
		d = dispatch{method:http.MethodPost,path:"/api/social/notifications/"+notificationID+"/read",handler:s.socialMarkNotificationRead,pathValues:map[string]string{"notification_id":notificationID}}
	case "testagram.follow_requests.list":
		d = dispatch{method:http.MethodGet,path:"/api/social/follow-requests",handler:s.socialFollowRequests}
	case "testagram.follow_requests.accept":
		d = dispatch{method:http.MethodPost,path:"/api/social/follow-requests/"+userID+"/accept",handler:s.socialAcceptFollowRequest,pathValues:map[string]string{"follower_id":userID}}
	case "testagram.follow_requests.reject":
		d = dispatch{method:http.MethodPost,path:"/api/social/follow-requests/"+userID+"/reject",handler:s.socialRejectFollowRequest,pathValues:map[string]string{"follower_id":userID}}
	default:
		return false
	}
	if d.handler == nil { return false }
	if d.payload == nil { d.payload = map[string]any{} }
	raw, err := json.Marshal(d.payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_INPUT", "Capability input could not be encoded", requestID))
		return true
	}
	targetURL := &url.URL{Path:d.path}
	if d.query != nil { targetURL.RawQuery = d.query.Encode() }
	req := r.Clone(r.Context())
	req.Method = d.method
	req.URL = targetURL
	req.Body = http.NoBody
	if d.method == http.MethodPost || d.method == http.MethodPut {
		req.Body = ioNopCloser(bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range d.pathValues { req.SetPathValue(key, value) }
	recorder := httptest.NewRecorder()
	d.handler(recorder, req)
	for key, values := range recorder.Header() {
		for _, value := range values { w.Header().Add(key, value) }
	}
	status := recorder.Code
	if status == 0 { status = http.StatusOK }
	w.WriteHeader(status)
	_, _ = w.Write(recorder.Body.Bytes())
	return true
}

type byteReadCloser struct { *bytes.Reader }
func (b byteReadCloser) Close() error { return nil }
func ioNopCloser(reader *bytes.Reader) byteReadCloser { return byteReadCloser{Reader:reader} }

func toString(value any) string {
	if value == nil { return "" }
	if text, ok := value.(string); ok { return text }
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(strings.TrimSpace(jsonNumber(value))), " ", ""))
}

func jsonNumber(value any) string {
	raw, err := json.Marshal(value)
	if err != nil { return "" }
	return strings.Trim(string(raw), "\"")
}
