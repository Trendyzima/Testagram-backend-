package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

const maxVideoProxyRequestBytes int64 = 20 << 30

func (s *service) videoProxyRequest(w http.ResponseWriter, r *http.Request, upstreamPath string, maxBodyBytes int64) {
	requestID := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID))
		return
	}
	_, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		writeNativeAuthFailure(w, status, err, requestID)
		return
	}
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		writeJSON(w, http.StatusUnauthorized, errorBody("AUTH_REQUIRED", "A valid native session is required", requestID))
		return
	}
	if s.videoService == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("VIDEO_SERVICE_NOT_CONFIGURED", "Testagram video service is not configured on the Backend", requestID))
		return
	}
	if maxBodyBytes > 0 {
		if maxBodyBytes == maxVideoProxyRequestBytes {
			if r.ContentLength <= 0 {
				writeJSON(w, http.StatusLengthRequired, errorBody("VIDEO_CONTENT_LENGTH_REQUIRED", "Content-Length is required for video uploads", requestID))
				return
			}
			if r.ContentLength > maxBodyBytes {
				writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("VIDEO_UPLOAD_TOO_LARGE", "Video upload request exceeds 20 GiB", requestID))
				return
			}
			// The public API has short defaults for ordinary JSON requests.
			// Clear the per-connection deadlines only for this bounded streaming
			// route; request cancellation still stops the upstream body stream.
			controller := http.NewResponseController(w)
			if err := controller.SetReadDeadline(time.Time{}); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("VIDEO_UPLOAD_STREAMING_UNAVAILABLE", "The Backend cannot safely stream this upload", requestID))
				return
			}
			if err := controller.SetWriteDeadline(time.Time{}); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("VIDEO_UPLOAD_STREAMING_UNAVAILABLE", "The Backend cannot safely stream this upload", requestID))
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	}
	if err := s.videoService.Proxy(w, r, upstreamPath, parts[1]); err != nil {
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			writeJSON(w, http.StatusGatewayTimeout, errorBody("VIDEO_SERVICE_TIMEOUT", "Video service did not respond in time", requestID))
			return
		}
		writeJSON(w, http.StatusBadGateway, errorBody("VIDEO_SERVICE_UNAVAILABLE", "Video service request could not be completed", requestID))
	}
}

func (s *service) videoChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use GET or POST for creator channels", requestID(r)))
		return
	}
	limit := int64(0)
	if r.Method == http.MethodPost {
		limit = 64 << 10
	}
	s.videoProxyRequest(w, r, "/v1/channels", limit)
}

func (s *service) videoUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use POST to upload a video", requestID(r)))
		return
	}
	s.videoProxyRequest(w, r, "/v1/videos", maxVideoProxyRequestBytes)
}

func (s *service) videoStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use GET to read video processing status", requestID(r)))
		return
	}
	id := strings.TrimSpace(r.PathValue("video_id"))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_VIDEO_ID", "A valid video ID is required", requestID(r)))
		return
	}
	s.videoProxyRequest(w, r, "/v1/videos/"+id+"/status", 0)
}

func (s *service) videoPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use POST to publish a video", requestID(r)))
		return
	}
	id := strings.TrimSpace(r.PathValue("video_id"))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_VIDEO_ID", "A valid video ID is required", requestID(r)))
		return
	}
	s.videoProxyRequest(w, r, "/v1/videos/"+id+"/publish", 0)
}

func (s *service) videoFeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use GET to read the public video feed", requestID(r)))
		return
	}
	s.videoProxyRequest(w, r, "/v1/feed", 0)
}

func (s *service) videoAction(w http.ResponseWriter, r *http.Request, action, method string, maxBodyBytes int64) {
	if r.Method != method {
		w.Header().Set("Allow", method+", OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Unsupported video action method", requestID(r)))
		return
	}
	id := strings.TrimSpace(r.PathValue("video_id"))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_VIDEO_ID", "A valid video ID is required", requestID(r)))
		return
	}
	s.videoProxyRequest(w, r, "/v1/videos/"+id+"/"+action, maxBodyBytes)
}

func (s *service) videoManifest(w http.ResponseWriter, r *http.Request) {
	s.videoAction(w, r, "manifest", http.MethodGet, 0)
}

func (s *service) videoView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use POST to record a video view", requestID(r)))
		return
	}
	session := strings.TrimSpace(r.Header.Get("X-Playback-Session"))
	if len(session) < 8 || len(session) > 128 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PLAYBACK_SESSION", "A valid playback session identifier is required", requestID(r)))
		return
	}
	for _, c := range session {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PLAYBACK_SESSION", "A valid playback session identifier is required", requestID(r)))
			return
		}
	}
	s.videoAction(w, r, "view", http.MethodPost, 0)
}

func (s *service) videoLike(w http.ResponseWriter, r *http.Request) {
	s.videoAction(w, r, "like", http.MethodPost, 0)
}

func (s *service) videoComments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use GET or POST for video comments", requestID(r)))
		return
	}
	limit := int64(0)
	if r.Method == http.MethodPost { limit = 8 << 10 }
	s.videoAction(w, r, "comments", r.Method, limit)
}

func (s *service) videoHLS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use GET to read an HLS video segment", requestID(r)))
		return
	}
	id := strings.TrimSpace(r.PathValue("video_id"))
	file := strings.TrimSpace(r.PathValue("file"))
	query := r.URL.Query()
	tokens := query["token"]
	if !socialUUIDPattern.MatchString(id) || !validVideoHLSFilename(file) || len(query) != 1 || len(tokens) != 1 || len(tokens[0]) > 256 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_VIDEO_PATH", "A valid video ID, HLS filename and signed playback token are required", requestID(r)))
		return
	}
	s.videoProxyRequest(w, r, "/v1/videos/"+id+"/hls/"+file, 0)
}

func validVideoHLSFilename(file string) bool {
	if file == "" || len(file) > 128 || strings.Contains(file, "..") || strings.ContainsAny(file, "/\\") { return false }
	for _, c := range file {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') { return false }
	}
	return strings.Contains(file, ".")
}