package main

import (
	"net/http"
	"strings"
)

// writeNativeAuthFailure emits stable, provider-neutral authorization errors.
// Native access-token verification is the only authenticated API boundary.
func writeNativeAuthFailure(w http.ResponseWriter, status int, cause error, requestID string) {
	code := "AUTH_REQUIRED"
	message := "A valid authenticated session is required"
	switch {
	case status == http.StatusForbidden && cause != nil &&
		strings.Contains(strings.ToLower(cause.Error()), "two-person account approval"):
		code = "IDENTITY_APPROVAL_REQUIRED"
		message = "This protected capability requires separate identity review and two-person approval. Basic Testagram account access does not."
	case status == http.StatusForbidden && cause != nil &&
		strings.Contains(strings.ToLower(cause.Error()), "email verification"):
		code = "EMAIL_VERIFICATION_REQUIRED"
		message = "Verify your email address before accessing Testagram"
	case status == http.StatusForbidden:
		code = "AUTH_FORBIDDEN"
		message = "This authenticated account is not authorized to access this resource"
	case status == http.StatusBadGateway:
		code = "AUTH_TOKEN_INVALID"
		message = "Authentication token could not be validated"
	case status >= 500:
		code = "AUTH_BACKEND_UNAVAILABLE"
		message = "Authentication service is temporarily unavailable"
	}
	writeJSON(w, status, errorBody(code, message, requestID))
}
