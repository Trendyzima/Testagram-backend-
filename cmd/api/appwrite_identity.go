package main

import (
	"context"
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

type appwriteProvisionInput struct {
	Username              string
	DisplayName           string
	BirthDate             string
	PolicyVersion         string
	TermsAccepted         bool
	PrivacyAccepted       bool
	ContentPolicyAccepted bool
	AgeConfirmed          bool
}

var appwriteUsernamePattern = regexp.MustCompile("^[A-Za-z0-9_]{3,30}$")

func decodeAppwriteProvisionInput(r *http.Request) (appwriteProvisionInput, error) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil {
		return appwriteProvisionInput{}, err
	}
	for key := range raw {
		if key != "username" && key != "display_name" && key != "birth_date" && key != "policy_version" && key != "terms_accepted" && key != "privacy_accepted" && key != "content_policy_accepted" && key != "age_confirmed" {
			return appwriteProvisionInput{}, errors.New("unknown field")
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return appwriteProvisionInput{}, errors.New("request must contain exactly one JSON value")
	}
	var in appwriteProvisionInput
	if value := raw["username"]; len(value) > 0 {
		if err := json.Unmarshal(value, &in.Username); err != nil {
			return in, err
		}
	}
	if value := raw["display_name"]; len(value) > 0 {
		if err := json.Unmarshal(value, &in.DisplayName); err != nil {
			return in, err
		}
	}
	if value := raw["birth_date"]; len(value) > 0 {
		if err := json.Unmarshal(value, &in.BirthDate); err != nil {
			return in, err
		}
	}
	if value := raw["policy_version"]; len(value) > 0 {
		if err := json.Unmarshal(value, &in.PolicyVersion); err != nil {
			return in, err
		}
	}
	for key, target := range map[string]*bool{"terms_accepted": &in.TermsAccepted, "privacy_accepted": &in.PrivacyAccepted, "content_policy_accepted": &in.ContentPolicyAccepted, "age_confirmed": &in.AgeConfirmed} {
		if value := raw[key]; len(value) > 0 {
			if err := json.Unmarshal(value, target); err != nil {
				return in, err
			}
		}
	}
	return in, nil
}

func validateAppwriteProvisionInput(in appwriteProvisionInput) error {
	if !appwriteUsernamePattern.MatchString(in.Username) {
		return errors.New("username must contain 3 to 30 letters, numbers or underscores")
	}
	if strings.TrimSpace(in.DisplayName) == "" || len(in.DisplayName) > 80 {
		return errors.New("display name must contain 1 to 80 characters")
	}
	birthDate, err := time.Parse("2006-01-02", in.BirthDate)
	if err != nil || birthDate.After(time.Now().UTC().AddDate(-18, 0, 0)) {
		return errors.New("account creation requires a valid adult birth date")
	}
	if in.PolicyVersion != env("TESTAGRAM_LEGAL_POLICY_VERSION", "2026-09") || !in.TermsAccepted || !in.PrivacyAccepted || !in.ContentPolicyAccepted || !in.AgeConfirmed {
		return errors.New("all current Testagram policies and age confirmation must be accepted")
	}
	return nil
}

func (s *service) verifiedAppwriteRequest(r *http.Request) (appwriteAccount, int, error) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return appwriteAccount{}, http.StatusUnauthorized, errors.New("Appwrite session is required")
	}
	endpoint := strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT"))
	project := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	if endpoint == "" || project == "" {
		return appwriteAccount{}, http.StatusServiceUnavailable, errors.New("Appwrite is not configured")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	account, status, err := fetchAppwriteAccount(ctx, s.upstream, endpoint, project, token)
	if err != nil {
		return appwriteAccount{}, status, err
	}
	if !account.EmailVerification {
		return appwriteAccount{}, http.StatusForbidden, errors.New("verify your Appwrite email before continuing")
	}
	if account.Email == "" || len(account.Email) > 254 || strings.ContainsAny(account.Email, "\r\n") {
		return appwriteAccount{}, http.StatusBadGateway, errors.New("Appwrite returned an invalid email address")
	}
	return account, http.StatusOK, nil
}

func appwriteIdentityPayload(out appwriteIdentityResponse) map[string]any {
	return map[string]any{
		"canonical_user_id": out.CanonicalUserID,
		"appwrite_user_id":  out.AppwriteUserID,
		"email":             out.Email,
		"email_verified":    out.EmailVerified,
		"username":          out.Username,
		"display_name":      out.DisplayName,
		"account_status":    out.AccountStatus,
	}
}

type appwriteIdentityResponse struct {
	CanonicalUserID string
	AppwriteUserID  string
	Email           string
	EmailVerified   bool
	Username        string
	DisplayName     string
	AccountStatus   string
}

func (s *service) appwriteRegistrationStatus(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "GET required", id))
		return
	}
	enabled := false
	reason := "legacy_inventory_incomplete"
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APPWRITE_KYC_BINDING_ENABLED")), "true") &&
		strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT")) != "" &&
		strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID")) != "" &&
		len(strings.TrimSpace(os.Getenv("APPWRITE_IDENTITY_INGEST_TOKEN"))) >= 32 {
		if s.db != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			var inventoryComplete bool
			if err := s.db.QueryRow(ctx, "SELECT legacy_inventory_complete FROM testagram_identity.migration_control WHERE singleton=TRUE").Scan(&inventoryComplete); err == nil && inventoryComplete {
				enabled = true
				reason = ""
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"new_account_registration_enabled": enabled, "reason": reason},
		"error": nil,
		"request_id": id,
	})
}

func (s *service) appwriteIdentitySession(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	account, status, err := s.verifiedAppwriteRequest(r)
	if err != nil {
		writeJSON(w, status, errorBody("APPWRITE_SESSION_INVALID", err.Error(), id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_DATABASE_UNAVAILABLE", "Identity database is unavailable", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var out appwriteIdentityResponse
	err = s.db.QueryRow(ctx,
		"SELECT m.canonical_user_id::text,m.appwrite_user_id,COALESCE(m.email,''),m.email_verified,a.username,a.display_name,a.account_status "+
			"FROM testagram_identity.appwrite_accounts m JOIN testagram_local.accounts a ON a.user_id=m.canonical_user_id "+
			"WHERE m.appwrite_user_id=$1", account.ID,
	).Scan(&out.CanonicalUserID, &out.AppwriteUserID, &out.Email, &out.EmailVerified, &out.Username, &out.DisplayName, &out.AccountStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, errorBody("IDENTITY_NOT_PROVISIONED", "This Appwrite identity is not linked to a Testagram account. Existing users must complete verified account linking; new accounts require the migration inventory gate.", id))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_LOOKUP_FAILED", "Testagram identity could not be resolved", id))
		return
	}
	if out.AccountStatus != "active" {
		writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This Testagram account is not active", id))
		return
	}
	out.Email = strings.ToLower(strings.TrimSpace(account.Email))
	out.EmailVerified = account.EmailVerification
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": appwriteIdentityPayload(out), "error": nil, "request_id": id})
}

func (s *service) provisionAppwriteIdentity(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", id))
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, errorBody("UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json", id))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	in, err := decodeAppwriteProvisionInput(r)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("REQUEST_TOO_LARGE", "Provisioning request exceeds 8 KiB", id))
			return
		}
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REQUEST", "Username and display name are required in one JSON object", id))
		return
	}
	if err := validateAppwriteProvisionInput(in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE", "Choose a username (3–30 letters, numbers or underscores) and a display name up to 80 characters", id))
		return
	}
	account, status, err := s.verifiedAppwriteRequest(r)
	if err != nil {
		writeJSON(w, status, errorBody("APPWRITE_SESSION_INVALID", err.Error(), id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_DATABASE_UNAVAILABLE", "Identity database is unavailable", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_DATABASE_UNAVAILABLE", "Identity database is unavailable", id))
		return
	}
	defer tx.Rollback(ctx)
	email := strings.ToLower(strings.TrimSpace(account.Email))
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", email); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_LOCK_FAILED", "Identity provisioning is temporarily unavailable", id))
		return
	}
	var existing appwriteIdentityResponse
	err = tx.QueryRow(ctx,
		"SELECT m.canonical_user_id::text,m.appwrite_user_id,COALESCE(m.email,''),m.email_verified,a.username,a.display_name,a.account_status "+
			"FROM testagram_identity.appwrite_accounts m JOIN testagram_local.accounts a ON a.user_id=m.canonical_user_id "+
			"WHERE m.appwrite_user_id=$1", account.ID,
	).Scan(&existing.CanonicalUserID, &existing.AppwriteUserID, &existing.Email, &existing.EmailVerified, &existing.Username, &existing.DisplayName, &existing.AccountStatus)
	if err == nil {
		if existing.AccountStatus != "active" {
			writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This Testagram account is not active", id))
			return
		}
		if err = tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_COMMIT_FAILED", "Identity could not be confirmed", id))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": appwriteIdentityPayload(existing), "error": nil, "request_id": id})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_LOOKUP_FAILED", "Identity lookup failed", id))
		return
	}
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("APPWRITE_KYC_BINDING_ENABLED")), "true") {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("APPWRITE_KYC_BINDING_DISABLED", "Appwrite registration is not enabled until the trusted KYC worker is configured", id))
		return
	}
	var kycApproved bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM testagram_identity.verification_approvals WHERE appwrite_user_id=$1)", account.ID).Scan(&kycApproved)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_VERIFICATION_LOOKUP_FAILED", "Identity verification status could not be checked", id))
		return
	}
	if !kycApproved {
		writeJSON(w, http.StatusForbidden, errorBody("IDENTITY_VERIFICATION_REQUIRED", "Complete Testagram's identity verification before creating the canonical account", id))
		return
	}
	var inventoryComplete bool
	err = tx.QueryRow(ctx, "SELECT legacy_inventory_complete FROM testagram_identity.migration_control WHERE singleton=TRUE FOR SHARE").Scan(&inventoryComplete)
	if err != nil || !inventoryComplete {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LEGACY_INVENTORY_INCOMPLETE", "New account creation is paused until the complete legacy account inventory has been imported. Existing data is protected.", id))
		return
	}
	var reserved bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM testagram_identity.legacy_email_reservations WHERE email_normalized=$1)", email).Scan(&reserved)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_LOOKUP_FAILED", "Legacy account protection check failed", id))
		return
	}
	if reserved {
		writeJSON(w, http.StatusConflict, errorBody("LEGACY_ACCOUNT_REQUIRES_LINK", "An existing Testagram account uses this email. Sign in to the existing account and complete verified linking; do not create a duplicate.", id))
		return
	}
	var legacyEmailExists bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM testagram_auth.identities WHERE email_normalized=$1)", email).Scan(&legacyEmailExists)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LEGACY_ACCOUNT_CHECK_FAILED", "Legacy account protection check failed", id))
		return
	}
	if legacyEmailExists {
		writeJSON(w, http.StatusConflict, errorBody("LEGACY_ACCOUNT_REQUIRES_LINK", "An existing Testagram account uses this email. Complete verified account linking.", id))
		return
	}
	var canonicalID string
	err = tx.QueryRow(ctx, "INSERT INTO testagram_local.accounts(user_id,username,display_name) VALUES(gen_random_uuid(),$1,$2) RETURNING user_id::text", in.Username, strings.TrimSpace(in.DisplayName)).Scan(&canonicalID)
	if err != nil {
		writeJSON(w, http.StatusConflict, errorBody("PROFILE_CREATE_FAILED", "Username is unavailable or the account could not be created", id))
		return
	}
	_, err = tx.Exec(ctx, "INSERT INTO testagram_identity.appwrite_accounts(appwrite_user_id,canonical_user_id,email,email_verified,linked_at) VALUES($1,$2,$3,TRUE,now())", account.ID, canonicalID, email)
	if err != nil {
		writeJSON(w, http.StatusConflict, errorBody("IDENTITY_ALREADY_LINKED", "This Appwrite identity is already linked to another Testagram account", id))
		return
	}
	_, err = tx.Exec(ctx, "INSERT INTO testagram_identity.legal_acceptances(canonical_user_id,birth_date,terms_accepted,privacy_accepted,content_policy_accepted,age_confirmed,policy_version) VALUES($1,$2::date,TRUE,TRUE,TRUE,TRUE,$3)", canonicalID, in.BirthDate, in.PolicyVersion)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LEGAL_ACCEPTANCE_FAILED", "Required policy acceptance could not be recorded; no account was committed", id))
		return
	}
	_, err = tx.Exec(ctx, "INSERT INTO testagram_identity.identity_events(appwrite_user_id,canonical_user_id,event_type,request_id) VALUES($1,$2,'provisioned',$3)", account.ID, canonicalID, id)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Account creation could not be audited; no changes were committed", id))
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_COMMIT_FAILED", "Account creation could not be committed", id))
		return
	}
	out := appwriteIdentityResponse{CanonicalUserID: canonicalID, AppwriteUserID: account.ID, Email: email, EmailVerified: true, Username: in.Username, DisplayName: strings.TrimSpace(in.DisplayName), AccountStatus: "active"}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": appwriteIdentityPayload(out), "error": nil, "request_id": id})
}
