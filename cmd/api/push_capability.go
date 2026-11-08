package main

import (
  "context"
  "net/http"
  "strings"
  "time"
)

func (s *service) nativePushCapability(w http.ResponseWriter, r *http.Request, requestID string, body capabilityRequest) bool {
  name := strings.TrimSpace(body.Capability)
  if name != "testagram.notifications.fcm.register" && name != "testagram.notifications.fcm.unregister" {
    return false
  }
  if s.db == nil {
    writeJSON(w, http.StatusServiceUnavailable, errorBody("PUSH_DATABASE_UNAVAILABLE", "Push registration is unavailable", requestID))
    return true
  }
  userID, status, err := s.authenticatedMediaUser(r)
  if err != nil || !socialUUIDPattern.MatchString(userID) {
    if err == nil { status = http.StatusUnauthorized }
    writeJSON(w, status, errorBody("AUTH_REQUIRED", "A valid authenticated session is required", requestID))
    return true
  }
  input := body.Input
  token, _ := input["token"].(string)
  token = strings.TrimSpace(token)
  platform, _ := input["platform"].(string)
  provider, _ := input["provider"].(string)
  platform, provider = strings.ToLower(strings.TrimSpace(platform)), strings.ToLower(strings.TrimSpace(provider))
  if len(token) < 16 || len(token) > 4096 || provider != "fcm" || (platform != "android" && platform != "ios" && platform != "web") {
    writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PUSH_TOKEN", "Push token, platform or provider is invalid", requestID))
    return true
  }
  ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
  defer cancel()
  if name == "testagram.notifications.fcm.unregister" {
    _, err = s.db.Exec(ctx, "UPDATE testagram_local.push_tokens SET enabled=FALSE,updated_at=now() WHERE user_id=$1 AND token=$2", userID, token)
    if err != nil {
      writeJSON(w, http.StatusServiceUnavailable, errorBody("PUSH_TOKEN_UPDATE_FAILED", "Could not disable this push token", requestID))
      return true
    }
    writeJSON(w, http.StatusOK, map[string]any{"registered": false})
    return true
  }
  _, err = s.db.Exec(ctx, `INSERT INTO testagram_local.push_tokens(user_id,token,platform,provider,enabled,last_seen_at,updated_at)
    VALUES($1,$2,$3,$4,TRUE,now(),now())
    ON CONFLICT(user_id,token) DO UPDATE SET platform=EXCLUDED.platform,provider=EXCLUDED.provider,enabled=TRUE,last_seen_at=now(),updated_at=now()`, userID, token, platform, provider)
  if err != nil {
    writeJSON(w, http.StatusServiceUnavailable, errorBody("PUSH_TOKEN_REGISTER_FAILED", "Could not register this push token", requestID))
    return true
  }
  writeJSON(w, http.StatusOK, map[string]any{"registered": true, "platform": platform, "provider": provider})
  return true
}
