package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type appwriteRegistrationConsentInput struct {
	AdultConfirmed bool `json:"adult_confirmed"`
	TermsAccepted  bool `json:"terms_accepted"`
	PrivacyAccepted bool `json:"privacy_accepted"`
}

// appwriteRegistrationConsent records a verified Appwrite account identity's
// age self-declaration and exact Terms/Privacy versions before that identity
// can be provisioned into the canonical Testagram account store. Email
// verification is deliberately not required here: consent is collected
// immediately after account creation and before the verification email.
func (s *service) appwriteRegistrationConsent(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration consent cannot be recorded right now", id))
		return
	}
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) < 8 || len(parts[1]) > 8192 {
		writeJSON(w, http.StatusUnauthorized, errorBody("APPWRITE_SESSION_REQUIRED", "A valid Appwrite account session is required", id))
		return
	}
	var input appwriteRegistrationConsentInput
	if err := decodeMediaJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REGISTRATION_CONSENT", "Registration consent payload is invalid", id))
		return
	}
	if !input.AdultConfirmed || !input.TermsAccepted || !input.PrivacyAccepted {
		writeJSON(w, http.StatusBadRequest, errorBody("REGISTRATION_CONSENT_REQUIRED", "Confirm that you are at least 18 and accept the Terms and Privacy Notice", id))
		return
	}

	endpoint := strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT"))
	projectID := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	if endpoint == "" || projectID == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("APPWRITE_NOT_CONFIGURED", "Appwrite authentication is not configured on the Backend", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	account, status, err := fetchAppwriteAccount(ctx, s.upstream, endpoint, projectID, parts[1])
	if err != nil {
		writeJSON(w, status, errorBody("APPWRITE_SESSION_INVALID", "The Appwrite account session could not be verified", id))
		return
	}
	email, err := authEmail(account.Email)
	if err != nil || !account.Status {
		writeJSON(w, http.StatusUnauthorized, errorBody("APPWRITE_ACCOUNT_INVALID", "The Appwrite account is unavailable", id))
		return
	}
	allowed, err := s.authRateAllowed(ctx, "registration-consent-ip", authClientIP(r), 12)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Registration is temporarily unavailable", id))
		return
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many registration attempts. Try again later.", id))
		return
	}

	termsVersion := strings.TrimSpace(env("TESTAGRAM_TERMS_VERSION", "2026-11-11"))
	privacyVersion := strings.TrimSpace(env("TESTAGRAM_PRIVACY_VERSION", "2026-11-11"))
	if termsVersion == "" || len(termsVersion) > 64 || privacyVersion == "" || len(privacyVersion) > 64 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REGISTRATION_POLICY_NOT_CONFIGURED", "Registration policy versions are not configured", id))
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration consent cannot be recorded right now", id))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, item := range []struct{ kind, version string }{
		{"age_confirmation", "age-v1"},
		{"terms", termsVersion},
		{"privacy", privacyVersion},
	} {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_identity.appwrite_registration_consents
			  (appwrite_user_id,email,consent_type,policy_version,accepted_at,request_id)
			VALUES($1,$2,$3,$4,now(),$5)
			ON CONFLICT(appwrite_user_id,consent_type,policy_version,email) DO NOTHING
		`, account.ID, email, item.kind, item.version, id)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_AUDIT_FAILED", "Registration consent could not be durably recorded", id))
			return
		}
	}
	var count int
	err = tx.QueryRow(ctx, `
		SELECT count(DISTINCT consent_type)
		FROM testagram_identity.appwrite_registration_consents
		WHERE appwrite_user_id=$1 AND email=$2
		  AND (consent_type='age_confirmation' AND policy_version='age-v1'
		    OR consent_type='terms' AND policy_version=$3
		    OR consent_type='privacy' AND policy_version=$4)
	`, account.ID, email, termsVersion, privacyVersion).Scan(&count)
	if err != nil || count != 3 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_AUDIT_FAILED", "Registration consent could not be verified", id))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_AUDIT_FAILED", "Registration consent could not be committed", id))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "consent_recorded": true,
		"terms_version": termsVersion, "privacy_version": privacyVersion,
		"email_verified": account.EmailVerification,
	})
}

