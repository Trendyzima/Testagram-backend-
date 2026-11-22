package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *service) nativeCommunicationCapabilityHardened(w http.ResponseWriter, r *http.Request, requestID string, body capabilityRequest) bool {
	name := strings.TrimSpace(body.Capability)
	switch name {
	case "testagram.conversations.create":
		return s.communicationCreateWithPolicy(w, r, requestID, body)
	case "testagram.conversations.list":
		return s.communicationListWithBlockFilter(w, r, requestID, body)
	case "testagram.realtime.ticket", "testagram.media.token",
		"testagram.messages.list", "testagram.messages.send_encrypted",
		"testagram.messages.edit_encrypted", "testagram.messages.mark_read",
		"testagram.messages.react", "testagram.messages.attach",
		"testagram.messages.delete",
		"testagram.calls.create", "testagram.calls.join",
		"testagram.calls.end", "testagram.calls.list", "testagram.calls.get":
		return s.communicationWithConversationPolicy(w, r, requestID, body)
	default:
		return s.nativeCommunicationCapability(w, r, requestID, body)
	}
}

func (s *service) communicationCreateWithPolicy(w http.ResponseWriter, r *http.Request, requestID string, body capabilityRequest) bool {
	raw, ok := body.Input["member_ids"].([]any)
	if !ok || len(raw) != 1 {
		writeJSON(w, http.StatusConflict, errorBody(
			"GROUP_INVITATIONS_REQUIRED",
			"Only one-to-one conversations are enabled until group invitation acceptance is implemented",
			requestID,
		))
		return true
	}
	recipientID, ok := raw[0].(string)
	if !ok || !socialUUIDPattern.MatchString(recipientID) {
		return s.nativeCommunicationCapability(w, r, requestID, body)
	}

	userID, _, authErr := s.authenticatedMediaUser(r)
	if authErr != nil || !socialUUIDPattern.MatchString(userID) {
		return s.nativeCommunicationCapability(w, r, requestID, body)
	}
	userID = strings.ToLower(userID)
	recipientID = strings.ToLower(recipientID)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if s.db.Stat().MaxConns() < 2 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"CONVERSATION_POLICY_UNAVAILABLE",
			"Communication privacy checks require a transactional database connection",
			requestID,
		))
		return true
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"CONVERSATION_POLICY_UNAVAILABLE",
			"Could not verify recipient privacy and block policy",
			requestID,
		))
		return true
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := lockCommunicationAccounts(ctx, tx, userID, recipientID); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody(
			"MEMBER_UNAVAILABLE",
			"This account is unavailable for a new conversation",
			requestID,
		))
		return true
	}
	allowed, err := communicationRecipientAllowed(ctx, tx, userID, recipientID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"CONVERSATION_POLICY_UNAVAILABLE",
			"Could not verify recipient privacy and block policy",
			requestID,
		))
		return true
	}
	if !allowed {
		writeJSON(w, http.StatusNotFound, errorBody(
			"MEMBER_UNAVAILABLE",
			"This account is unavailable for a new conversation",
			requestID,
		))
		return true
	}

	// Keep the ordered account locks while the canonical transactional handler
	// creates or reuses the direct conversation. A concurrent block/follow
	// mutation cannot slip between the policy check and conversation write.
	return s.nativeCommunicationCapability(w, r, requestID, body)
}

func (s *service) communicationWithConversationPolicy(w http.ResponseWriter, r *http.Request, requestID string, body capabilityRequest) bool {
	userID, _, authErr := s.authenticatedMediaUser(r)
	if authErr != nil || !socialUUIDPattern.MatchString(userID) {
		return s.nativeCommunicationCapability(w, r, requestID, body)
	}
	userID = strings.ToLower(userID)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	conversationID, err := communicationResolveConversationID(ctx, s.db, body)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Preserve the canonical handler's established not-found response
			// for genuinely absent message/call resources.
			return s.nativeCommunicationCapability(w, r, requestID, body)
		}
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"COMMUNICATION_POLICY_UNAVAILABLE",
			"Could not resolve the conversation safety boundary",
			requestID,
		))
		return true
	}
	if !socialUUIDPattern.MatchString(conversationID) {
		// Invalid or absent identifiers are left to the canonical handler for
		// its established validation/not-found response.
		return s.nativeCommunicationCapability(w, r, requestID, body)
	}

	if s.db.Stat().MaxConns() < 2 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"COMMUNICATION_POLICY_UNAVAILABLE",
			"Communication privacy checks require a transactional database connection",
			requestID,
		))
		return true
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"COMMUNICATION_POLICY_UNAVAILABLE",
			"Could not verify conversation safety",
			requestID,
		))
		return true
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := lockCommunicationConversationAccounts(ctx, tx, userID, conversationID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"COMMUNICATION_POLICY_UNAVAILABLE",
			"Could not lock conversation participants for a safety check",
			requestID,
		))
		return true
	}
	blocked, err := communicationConversationBlocked(ctx, tx, userID, conversationID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"COMMUNICATION_POLICY_UNAVAILABLE",
			"Could not verify conversation safety",
			requestID,
		))
		return true
	}
	if blocked {
		writeJSON(w, http.StatusForbidden, errorBody(
			"CONVERSATION_ACCESS_DENIED",
			"A block prevents access to this conversation",
			requestID,
		))
		return true
	}

	// Keep account locks until the canonical operation has completed so a
	// concurrent block cannot race a message send, read, reaction, delete,
	// call join or call end.
	if body.Capability == "testagram.calls.create" {
		recorder := httptest.NewRecorder()
		handled := s.nativeCommunicationCapability(recorder, r, requestID, body)
		if !handled || recorder.Code < 200 || recorder.Code >= 300 {
			copyCommunicationResponse(w, recorder)
			return handled
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			copyCommunicationResponse(w, recorder)
			return true
		}
		// Signaling authorization is not proof of end-to-end media encryption.
		payload["e2ee_enabled"] = false
		encoded, err := json.Marshal(payload)
		if err != nil {
			copyCommunicationResponse(w, recorder)
			return true
		}
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(encoded)
		return true
	}
	return s.nativeCommunicationCapability(w, r, requestID, body)
}

func communicationResolveConversationID(ctx context.Context, q communicationQueryRower, body capabilityRequest) (string, error) {
	input := body.Input
	stringValue := func(key string) string {
		value, _ := input[key].(string)
		return strings.TrimSpace(value)
	}
	switch strings.TrimSpace(body.Capability) {
	case "testagram.messages.list", "testagram.messages.send_encrypted",
		"testagram.calls.list", "testagram.calls.create",
		"testagram.realtime.ticket":
		return stringValue("conversation_id"), nil
	case "testagram.media.token":
		return communicationConversationForCall(ctx, q, stringValue("room_id"))
	case "testagram.calls.get", "testagram.calls.join", "testagram.calls.end":
		return communicationConversationForCall(ctx, q, stringValue("call_id"))
	case "testagram.messages.edit_encrypted", "testagram.messages.mark_read",
		"testagram.messages.react", "testagram.messages.attach", "testagram.messages.delete":
		return communicationConversationForMessage(ctx, q, stringValue("message_id"))
	default:
		return "", nil
	}
}

func communicationConversationForCall(ctx context.Context, q communicationQueryRower, callID string) (string, error) {
	if !socialUUIDPattern.MatchString(callID) {
		return "", nil
	}
	var conversationID string
	err := q.QueryRow(ctx,
		"SELECT conversation_id::text FROM testagram_local.call_sessions WHERE id=$1",
		callID,
	).Scan(&conversationID)
	return conversationID, err
}

func communicationConversationForMessage(ctx context.Context, q communicationQueryRower, messageID string) (string, error) {
	if !socialUUIDPattern.MatchString(messageID) {
		return "", nil
	}
	var conversationID string
	err := q.QueryRow(ctx,
		"SELECT conversation_id::text FROM testagram_local.messages WHERE id=$1",
		messageID,
	).Scan(&conversationID)
	return conversationID, err
}

func (s *service) communicationListWithBlockFilter(w http.ResponseWriter, r *http.Request, requestID string, body capabilityRequest) bool {
	userID, _, authErr := s.authenticatedMediaUser(r)
	if authErr != nil || !socialUUIDPattern.MatchString(userID) {
		return s.nativeCommunicationCapability(w, r, requestID, body)
	}
	userID = strings.ToLower(userID)

	recorder := httptest.NewRecorder()
	handled := s.nativeCommunicationCapability(recorder, r, requestID, body)
	if !handled || recorder.Code < 200 || recorder.Code >= 300 {
		copyCommunicationResponse(w, recorder)
		return true
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		copyCommunicationResponse(w, recorder)
		return true
	}
	rawItems, ok := payload["items"].([]any)
	if !ok {
		copyCommunicationResponse(w, recorder)
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	filtered := make([]any, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		conversationID, _ := item["id"].(string)
		if !socialUUIDPattern.MatchString(conversationID) {
			continue
		}
		blocked, err := communicationConversationBlocked(ctx, s.db, userID, conversationID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody(
				"CONVERSATIONS_LIST_FAILED",
				"Could not verify conversation safety",
				requestID,
			))
			return true
		}
		if !blocked {
			filtered = append(filtered, item)
		}
	}
	payload["items"] = filtered
	encoded, err := json.Marshal(payload)
	if err != nil {
		copyCommunicationResponse(w, recorder)
		return true
	}
	for key, values := range recorder.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(recorder.Code)
	_, _ = w.Write(encoded)
	return true
}

func copyCommunicationResponse(w http.ResponseWriter, recorder *httptest.ResponseRecorder) {
	for key, values := range recorder.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	status := recorder.Code
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(recorder.Body.Bytes())
}
