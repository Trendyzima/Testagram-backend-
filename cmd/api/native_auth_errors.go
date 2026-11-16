package main

import (
	"net/http"
	"strings"
)

func writeAuthFailure(w http.ResponseWriter, status int, cause error, requestID string) {
	code := "AUTH_REQUIRED"
	message := "A valid Testagram session is required"
	switch {
	case status == http.StatusForbidden && cause != nil &&
		strings.Contains(strings.ToLower(cause.Error()), "two-person account approval"):
		code = "IDENTITY_APPROVAL_REQUIRED"
		message = "Identity review and two-person account approval are required before accessing Testagram"
	case status == http.StatusForbidden && cause != nil &&
		strings.Contains(strings.ToLower(cause.Error()), "email verification"):
		code = "EMAIL_VERIFICATION_REQUIRED"
		message = "Verify your email address before accessing Testagram"
	case status == http.StatusForbidden:
		code = "AUTH_FORBIDDEN"
		message = "This Testagram account is not authorized to access this resource"
	case status == http.StatusUnauthorized:
		code = "AUTH_TOKEN_INVALID"
		message = "The Testagram session is invalid or has expired"
	case status >= 500:
		code = "AUTH_BACKEND_UNAVAILABLE"
		message = "Testagram authentication is temporarily unavailable"
	}
	writeJSON(w, status, errorBody(code, message, requestID))
}
