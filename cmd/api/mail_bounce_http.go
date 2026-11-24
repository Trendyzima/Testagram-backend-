package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const nativeBounceIngressPath = "/internal/mail/bounce"
const nativeBounceIngressMaxSkew = 5 * time.Minute

func canonicalNativeBounceRecipient(recipient string) string {
	recipient = strings.TrimSpace(recipient)
	at := strings.LastIndex(recipient, "@")
	if at <= 0 {
		return recipient
	}
	// VERP local parts contain a case-sensitive base64url token. Normalize only
	// the DNS domain; lowercasing the entire address would corrupt the token.
	return recipient[:at] + "@" + strings.ToLower(recipient[at+1:])
}

func nativeBounceIngressMAC(secret, timestamp, recipient string, body []byte) []byte {
	bodyHash := sha256.Sum256(body)
	payload := timestamp + "\n" + canonicalNativeBounceRecipient(recipient) + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (s *service) nativeBounceHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Testagram-Bounce-Endpoint", "native-v1")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	secret := strings.TrimSpace(os.Getenv("TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET"))
	if len(secret) < 32 || s.db == nil {
		http.Error(w, "bounce ingress unavailable", http.StatusServiceUnavailable)
		return
	}
	timestamp := strings.TrimSpace(r.Header.Get("X-Testagram-Timestamp"))
	recipient := strings.TrimSpace(r.Header.Get("X-Testagram-Recipient"))
	signature := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("X-Testagram-Signature"), "sha256="))
	unixTime, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || timestamp == "" || recipient == "" || signature == "" {
		http.Error(w, "invalid ingress authentication", http.StatusUnauthorized)
		return
	}
	signedAt := time.Unix(unixTime, 0)
	if delta := time.Since(signedAt); delta > nativeBounceIngressMaxSkew || delta < -nativeBounceIngressMaxSkew {
		http.Error(w, "stale ingress request", http.StatusUnauthorized)
		return
	}
	if !strings.Contains(recipient, "@") || !strings.EqualFold(recipient[strings.LastIndex(recipient, "@")+1:], nativeBounceDomain) {
		http.Error(w, "invalid bounce recipient", http.StatusBadRequest)
		return
	}
	local := recipient[:strings.LastIndex(recipient, "@")]
	match := nativeBounceLocalPart.FindStringSubmatch(local)
	if len(match) != 2 {
		http.Error(w, "invalid bounce token", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNativeBounceBytes))
	if err != nil || len(body) == 0 {
		http.Error(w, "invalid bounce payload", http.StatusRequestEntityTooLarge)
		return
	}
	provided, decodeErr := hex.DecodeString(signature)
	expected := nativeBounceIngressMAC(secret, timestamp, recipient, body)
	if decodeErr != nil || len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
		http.Error(w, "invalid ingress authentication", http.StatusUnauthorized)
		return
	}
	report, err := parseNativeBounceReport(body)
	if err != nil {
		http.Error(w, "invalid delivery report", http.StatusUnprocessableEntity)
		return
	}
	tokenHash := sha256.Sum256([]byte(match[1]))
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := processNativeBounceReport(ctx, s.db, tokenHash[:], body, report); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "bounce processing temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(err.Error(), "unknown or expired") || strings.Contains(err.Error(), "does not match") {
			http.Error(w, "bounce correlation rejected", http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, "bounce processing temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"ok\":true,\"accepted\":true}\n"))
}
