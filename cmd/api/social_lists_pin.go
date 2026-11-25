package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const socialListPinnedLimit = 5

type socialListPinInput struct {
	Pinned bool `json:"pinned"`
}

func socialListPinCapacityExceeded(count int64, currentlyPinned, requestedPinned bool) bool {
	return requestedPinned && !currentlyPinned && count >= socialListPinnedLimit
}

func (s *service) socialListPinUpdate(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, uid, rid, "social_list_pin", 30) {
		return
	}

	id := strings.ToLower(strings.TrimSpace(r.PathValue("list_id")))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_ID", "A valid list ID is required", rid))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var in socialListPinInput
	if err := decoder.Decode(&in); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PIN_STATE", "A boolean pinned value is required", rid))
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_PIN_FAILED", "List pin state could not be updated", rid))
		return
	}
	defer tx.Rollback(r.Context())

	// Serialize pin changes per owner so concurrent requests cannot exceed the
	// five pinned-list limit. The advisory lock is scoped to this feature.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('testagram:social-list-pin:' || $1, 0))`, uid); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_PIN_FAILED", "List pin state could not be updated", rid))
		return
	}

	var owner string
	var currentlyPinned bool
	err = tx.QueryRow(r.Context(), `
SELECT owner_id::text,is_pinned
FROM testagram_local.social_lists
WHERE id=$1::uuid AND deleted_at IS NULL
FOR UPDATE`, id).Scan(&owner, &currentlyPinned)
	if err != nil || owner != uid {
		writeJSON(w, http.StatusNotFound, errorBody("LIST_NOT_FOUND", "List is unavailable", rid))
		return
	}

	if in.Pinned && !currentlyPinned {
		var count int64
		if err = tx.QueryRow(r.Context(), `
SELECT count(*)
FROM testagram_local.social_lists
WHERE owner_id=$1::uuid AND deleted_at IS NULL AND is_pinned`, uid).Scan(&count); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_PIN_FAILED", "Pinned lists could not be counted", rid))
			return
		}
		if socialListPinCapacityExceeded(count, currentlyPinned, in.Pinned) {
			writeJSON(w, http.StatusConflict, errorBody("PINNED_LIST_LIMIT", "You can pin up to five lists. Unpin one before pinning another.", rid))
			return
		}
	}

	_, err = tx.Exec(r.Context(), `
UPDATE testagram_local.social_lists
SET is_pinned=$2,updated_at=now()
WHERE id=$1::uuid AND owner_id=$3::uuid AND deleted_at IS NULL`, id, in.Pinned, uid)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_PIN_FAILED", "List pin state could not be updated", rid))
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_PIN_FAILED", "List pin state could not be committed", rid))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"list_id": id, "pinned": in.Pinned},
		"error": nil,
		"request_id": rid,
	})
}
