package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

)

const maxMediaIntentBody = 16 << 10

type mediaUploadIntentRequest struct {
	ContentType string `json:"content_type"`
	Visibility string `json:"visibility"`
	SizeBytes int64 `json:"size_bytes"`
}

type privateMediaDownloadRequest struct {
	Key string `json:"key"`
}

func (s *service) mediaPreflight(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *service) mediaUploadIntent(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.media == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage is not configured on this server", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	var body mediaUploadIntentRequest
	if err := decodeMediaJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "Request must contain valid JSON media metadata", id))
		return
	}
	if s.platform != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		result, err := s.platform.Check(ctx, "media-upload:"+userID, 10, time.Minute)
		cancel()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_RATE_LIMIT_UNAVAILABLE", "Media upload protection is temporarily unavailable", id))
			return
		}
		if !result.Allowed {
			retryAfter := result.RetryAfterSeconds
			if retryAfter < 1 { retryAfter = 1 }
			w.Header().Set("Retry-After", strconvFormatInt(retryAfter))
			writeJSON(w, http.StatusTooManyRequests, errorBody("MEDIA_RATE_LIMITED", "Too many upload intents; try again later", id))
			return
		}
	}
	intent, err := s.media.CreateUploadIntent(userID, body.ContentType, body.Visibility, body.SizeBytes, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", err.Error(), id))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": intent, "error": nil, "request_id": id})
}

func (s *service) mediaPrivateDownloadURL(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.media == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage is not configured on this server", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	var body privateMediaDownloadRequest
	if err := decodeMediaJSON(w, r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A valid media key is required", id))
		return
	}
	signedURL, err := s.media.PresignPrivateDownload(userID, body.Key, 5*time.Minute, time.Now())
	if err != nil {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_ACCESS_DENIED", "This private media object is not available to this account", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"url": signedURL, "expires_in_seconds": 300}, "error": nil, "request_id": id})
}

func decodeMediaJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxMediaIntentBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil { return err }
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil { return errors.New("multiple JSON values") }
		return err
	}
	return nil
}

func (s *service) authenticatedMediaUser(r *http.Request) (string, int, error) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(authorization), "bearer ") || len(strings.TrimSpace(authorization[7:])) < 8 {
		return "", http.StatusUnauthorized, errors.New("missing bearer token")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(env("SUPABASE_URL", "")), "/")
	apiKey := strings.TrimSpace(env("SUPABASE_PUBLISHABLE_KEY", ""))
	if baseURL == "" || apiKey == "" {
		return "", http.StatusServiceUnavailable, errors.New("authentication backend is not configured")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/auth/v1/user", nil)
	if err != nil { return "", http.StatusBadGateway, err }
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	client := s.upstream
	if client == nil { client = &http.Client{Timeout: 3 * time.Second} }
	resp, err := client.Do(req)
	if err != nil { return "", http.StatusBadGateway, err }
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", http.StatusUnauthorized, errors.New("session rejected")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", http.StatusBadGateway, errors.New("authentication service failed")
	}
	var user struct { ID string `json:"id"` }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&user); err != nil || user.ID == "" {
		return "", http.StatusBadGateway, errors.New("authentication service returned invalid user")
	}
	return user.ID, http.StatusOK, nil
}

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

var _ = media.UploadIntent{}
