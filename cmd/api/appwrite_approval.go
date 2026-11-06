package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type appwriteApprovalInput struct {
	AppwriteUserID string
	RegistrationID string
	ProviderEventID string
}

var identityApprovalIDPattern = regexp.MustCompile("^[A-Za-z0-9._:-]{1,128}$")

func decodeAppwriteApprovalInput(r *http.Request) (appwriteApprovalInput, error) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil { return appwriteApprovalInput{}, err }
	for key := range raw {
		if key != "appwrite_user_id" && key != "registration_id" && key != "provider_event_id" {
			return appwriteApprovalInput{}, errors.New("unknown field")
		}
	}
	var in appwriteApprovalInput
	for key, target := range map[string]*string{
		"appwrite_user_id": &in.AppwriteUserID,
		"registration_id": &in.RegistrationID,
		"provider_event_id": &in.ProviderEventID,
	} {
		if value := raw[key]; len(value)>0 {
			if err := json.Unmarshal(value,target); err != nil { return in,err }
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF { return in, errors.New("request must contain exactly one JSON value") }
	return in,nil
}

// recordAppwriteIdentityApproval is an internal, server-to-server trust boundary.
// Only the configured identity-verification worker may attest that KYC passed.
func (s *service) recordAppwriteIdentityApproval(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if r.Method != http.MethodPost {
		writeJSON(w,http.StatusMethodNotAllowed,errorBody("METHOD_NOT_ALLOWED","POST required",id))
		return
	}
	expected := strings.TrimSpace(os.Getenv("APPWRITE_IDENTITY_INGEST_TOKEN"))
	provided := strings.TrimSpace(r.Header.Get("X-Testagram-Identity-Ingest"))
	if len(expected)<32 || len(provided)!=len(expected) || subtle.ConstantTimeCompare([]byte(expected),[]byte(provided))!=1 {
		writeJSON(w,http.StatusUnauthorized,errorBody("IDENTITY_INGEST_UNAUTHORIZED","Identity verification service authentication failed",id))
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")),"application/json") {
		writeJSON(w,http.StatusUnsupportedMediaType,errorBody("UNSUPPORTED_MEDIA_TYPE","Content-Type must be application/json",id))
		return
	}
	r.Body=http.MaxBytesReader(w,r.Body,4<<10)
	defer r.Body.Close()
	in,err:=decodeAppwriteApprovalInput(r)
	if err!=nil || !validAppwriteUserID(in.AppwriteUserID) ||
		!identityApprovalIDPattern.MatchString(in.RegistrationID) ||
		!identityApprovalIDPattern.MatchString(in.ProviderEventID) {
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_APPROVAL","A valid Appwrite user, registration and provider event ID are required",id))
		return
	}
	if s.db==nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_DATABASE_UNAVAILABLE","Identity database is unavailable",id))
		return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second)
	defer cancel()
	tx,err:=s.db.Begin(ctx)
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_DATABASE_UNAVAILABLE","Identity database is unavailable",id))
		return
	}
	defer tx.Rollback(ctx)
	var registrationID,providerEventID string
	err=tx.QueryRow(ctx,"SELECT registration_id,provider_event_id FROM testagram_identity.verification_approvals WHERE appwrite_user_id=$1 FOR UPDATE",in.AppwriteUserID).Scan(&registrationID,&providerEventID)
	if err==nil {
		if registrationID!=in.RegistrationID || providerEventID!=in.ProviderEventID {
			writeJSON(w,http.StatusConflict,errorBody("IDENTITY_APPROVAL_CONFLICT","A different verification approval is already recorded for this Appwrite account",id))
			return
		}
		if err=tx.Commit(ctx); err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_FAILED","Approval could not be confirmed",id))
			return
		}
		writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"already_recorded":true},"error":nil,"request_id":id})
		return
	}
	if err.Error()!="no rows in result set" {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_LOOKUP_FAILED","Approval status could not be checked",id))
		return
	}
	_,err=tx.Exec(ctx,"INSERT INTO testagram_identity.verification_approvals(appwrite_user_id,registration_id,provider_event_id,approved_at) VALUES($1,$2,$3,now())",in.AppwriteUserID,in.RegistrationID,in.ProviderEventID)
	if err!=nil {
		writeJSON(w,http.StatusConflict,errorBody("IDENTITY_APPROVAL_CONFLICT","Verification event is already attached to another account",id))
		return
	}
	if err=tx.Commit(ctx); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_FAILED","Approval could not be committed",id))
		return
	}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"recorded":true},"error":nil,"request_id":id})
}
