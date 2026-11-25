package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

const authPasswordRounds = 210000
const authTokenTTL = 24 * time.Hour

type authInput struct {
	Email        string `json:"email"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

type authClaims struct {
	Subject            string `json:"sub"`
	Email              string `json:"email"`
	Role               string `json:"role"`
	Issuer             string `json:"iss"`
	Audience           string `json:"aud"`
	IssuedAt           int64  `json:"iat"`
	ExpiresAt          int64  `json:"exp"`
	TokenVersion       int64  `json:"ver"`
	SessionID          string `json:"sid"`
	MFAAuthenticatedAt int64  `json:"mfa_at,omitempty"`
	MFAAuthenticatorID string `json:"mfa_credential,omitempty"`
}

var authUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,30}$`)

func authEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid email")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(parsed.Address, value) || !strings.Contains(parsed.Address, "@") {
		return "", errors.New("invalid email")
	}
	return strings.ToLower(value), nil
}

func authRandomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func authRandomOTP() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func authOTPHash(code string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(os.Getenv("TESTAGRAM_MAIL_ENCRYPTION_KEY")))
	if err != nil || len(key) != 32 {
		return nil, errors.New("email OTP hashing key is unavailable")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("testagram-email-otp-v1\x00"))
	_, _ = mac.Write([]byte(code))
	return mac.Sum(nil), nil
}

func authTokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Argon2id is the default password KDF. The encoded format is self-describing so
// parameters can be upgraded without a schema migration.
const authArgon2Memory uint32 = 64 * 1024
const authArgon2Iterations uint32 = 3
const authArgon2Parallelism uint8 = 2
const authArgon2KeyLength uint32 = 32

// authDummyPasswordHash uses the same Argon2id profile as real credentials.
// Verifying it for unknown email addresses prevents a cheap timing oracle
// that distinguishes nonexistent accounts from incorrect passwords.
const authDummyPasswordHash = "argon2id$v=19$m=65536,t=3,p=2$ek8bLJ2Ob1BxQqO0xdbn-A$S4uEMZNvr8VVRXHYwFrEDQ-1MEaTaBFXL8kbVPsuD3Y"

func authPasswordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 128 {
		return "", errors.New("password must be between 12 and 128 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived := argon2.IDKey([]byte(password), salt, authArgon2Iterations, authArgon2Memory, authArgon2Parallelism, authArgon2KeyLength)
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		authArgon2Memory, authArgon2Iterations, authArgon2Parallelism,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(derived)), nil
}

// authPasswordVerify accepts the previous PBKDF2 format only as a migration
// bridge. A successful legacy login is transparently rehashed by the caller.
func authPasswordVerify(encoded, password string) bool {
	if strings.HasPrefix(encoded, "pbkdf2-sha256$") {
		parts := strings.Split(encoded, "$")
		if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
			return false
		}
		var rounds int
		if _, err := fmt.Sscanf(parts[1], "%d", &rounds); err != nil || rounds < 100000 || rounds > 1000000 {
			return false
		}
		salt, saltErr := base64.RawURLEncoding.DecodeString(parts[2])
		expected, keyErr := base64.RawURLEncoding.DecodeString(parts[3])
		if saltErr != nil || keyErr != nil || len(salt) < 16 || len(salt) > 64 || len(expected) != 32 {
			return false
		}
		actual := authPBKDF2([]byte(password), salt, rounds, len(expected))
		return subtle.ConstantTimeCompare(actual, expected) == 1
	}

	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" {
		return false
	}
	var memory, iterations uint32
	var parallelism uint16
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil ||
		memory < 19*1024 || memory > 128*1024 || iterations < 1 || iterations > 6 ||
		parallelism < 1 || parallelism > 4 || memory < uint32(parallelism)*8 {
		return false
	}
	salt, saltErr := base64.RawURLEncoding.DecodeString(parts[3])
	expected, keyErr := base64.RawURLEncoding.DecodeString(parts[4])
	if saltErr != nil || keyErr != nil || len(salt) < 16 || len(salt) > 64 || len(expected) < 16 || len(expected) > 64 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(parallelism), uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func authPasswordNeedsRehash(encoded string) bool {
	return !strings.HasPrefix(encoded, "argon2id$v=19$m=65536,t=3,p=2$")
}

func authPBKDF2(password, salt []byte, iterations, keyLen int) []byte {
	out := make([]byte, 0, keyLen)
	for block := uint32(1); len(out) < keyLen; block++ {
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		_, _ = mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func authSignJWT(userID, email string, version int64, sessionID string) (string, error) {
	now := time.Now().UTC()
	return authSignJWTClaims(authClaims{
		Subject: userID, Email: email, Role: "authenticated",
		Issuer: "testagram-auth", Audience: "testagram-api",
		IssuedAt: now.Unix(), ExpiresAt: now.Add(15 * time.Minute).Unix(),
		TokenVersion: version, SessionID: sessionID,
	})
}

func authSignJWTClaims(claims authClaims) (string, error) {
	secret := strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET"))
	if len(secret) < 32 {
		return "", errors.New("TESTAGRAM_AUTH_JWT_SECRET must contain at least 32 characters")
	}
	if claims.MFAAuthenticatedAt > 0 {
		now := time.Now().UTC().Unix()
		if claims.MFAAuthenticatedAt > now+60 || now-claims.MFAAuthenticatedAt > int64((5*time.Minute)/time.Second) {
			return "", errors.New("MFA proof timestamp must be fresh and within the five-minute step-up window")
		}
	}
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *service) authRateAllowed(ctx context.Context, action, identity string, limit int) (bool, error) {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(identity))))
	key := hex.EncodeToString(sum[:])
	var attempts int
	err := s.db.QueryRow(ctx, `
		INSERT INTO testagram_auth.rate_limits(action,bucket_hash,window_start,attempts)
		VALUES($1,$2,date_trunc('minute',now()),1)
		ON CONFLICT(action,bucket_hash,window_start)
		DO UPDATE SET attempts=testagram_auth.rate_limits.attempts+1
		RETURNING attempts
	`, action, key).Scan(&attempts)
	return attempts <= limit, err
}

func authClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Caddy overwrites X-Real-IP with its observed client address. Trust that
	// header only when the immediate peer is a private/loopback network hop;
	// ignore caller-supplied forwarding headers on public connections.
	remoteIP := net.ParseIP(host)
	if remoteIP != nil && (remoteIP.IsPrivate() || remoteIP.IsLoopback()) {
		if realIP := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); realIP != nil {
			return realIP.String()
		}
	}
	return host
}

func (s *service) sendAuthEmail(ctx context.Context, recipient, subject, html string) error {
	if s.authEmailSender != nil {
		return s.authEmailSender(ctx, recipient, subject, html)
	}
	return s.authSendEmail(ctx, recipient, subject, html)
}

func (s *service) authSendEmail(ctx context.Context, recipient, subject, html string) error {
	return s.enqueueAuthEmail(ctx, recipient, subject, html)
}

func (s *service) authRespond(w http.ResponseWriter, r *http.Request, action string) bool {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return true
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return true
	}
	// Public self-service signup is fail-closed. Only the evidence-backed identity
	// provisioning flow may create an account after independent evidence review and
	// two-person approval. Keep this gate before database readiness checks so an
	// unavailable review service cannot fall through to ordinary account creation.
	if action == "signup" {
		writeJSON(w, http.StatusForbidden, errorBody("IDENTITY_REVIEW_REQUIRED", "Identity evidence review and two-person account approval are required before account creation", requestID(r)))
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var in authInput
	if err := decodeSingleJSON(r.Body, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_JSON", "Request body is invalid", requestID(r)))
		return true
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Authentication store is unavailable", requestID(r)))
		return true
	}
	if action == "login" && len(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET"))) < 32 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_NOT_CONFIGURED", "Independent authentication is not configured", requestID(r)))
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ip := authClientIP(r)
	switch action {
	case "signup":
		email, err := authEmail(in.Email)
		if err != nil || !authUsernamePattern.MatchString(in.Username) {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_SIGNUP", "Enter a valid email, username, and password of at least 12 characters", requestID(r)))
			return true
		}
		ok, err := s.authRateAllowed(ctx, "signup-ip", ip, 8)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Authentication store is unavailable", requestID(r)))
			return true
		}
		if !ok {
			writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Try again later", requestID(r)))
			return true
		}
		passwordHash, err := authPasswordHash(in.Password)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PASSWORD", "Password must be 12–128 bytes", requestID(r)))
			return true
		}
		token, err := authRandomToken()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Unable to create verification token", requestID(r)))
			return true
		}
		otp, err := authRandomOTP()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("OTP_FAILED", "Unable to create verification code", requestID(r)))
			return true
		}
		otpHash, err := authOTPHash(otp)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("EMAIL_UNAVAILABLE", "Account verification email is temporarily unavailable", requestID(r)))
			return true
		}
		tx, txErr := s.db.Begin(ctx)
		if txErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SIGNUP_UNAVAILABLE", "Account could not be created", requestID(r)))
			return true
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var userID string
		txErr = tx.QueryRow(ctx, `
			INSERT INTO testagram_auth.identities(
			email,username,password_hash,verification_token_hash,verification_expires_at,
			verification_otp_hash,verification_otp_expires_at,verification_otp_attempts
		)
		VALUES($1,$2,$3,$4,now()+interval '24 hours',$5,now()+interval '10 minutes',0)
		ON CONFLICT DO NOTHING RETURNING user_id::text
		`, email, in.Username, passwordHash, authTokenHash(token), otpHash).Scan(&userID)
		if errors.Is(txErr, pgx.ErrNoRows) {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the address is eligible, a verification email will be sent."})
			return true
		}
		if txErr != nil {
			writeJSON(w, http.StatusConflict, errorBody("SIGNUP_FAILED", "Account could not be created", requestID(r)))
			return true
		}
		base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
		link := base + "/verify-email?token=" + url.QueryEscape(token)
		if err := enqueueAuthEmailWith(ctx, tx, email, "Your Testagram verification code", "<p>Welcome to Testagram. Enter this verification code in the app:</p><p style=\"font-size:28px;font-weight:700;letter-spacing:8px\">"+otp+"</p><p>Your code expires in 10 minutes and can be used once.</p><p>You can also verify using this secure link:</p><p><a href=\""+link+"\">Verify email</a></p><p>The link expires in 24 hours. If you did not create this account, ignore this email.</p>"); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("EMAIL_UNAVAILABLE", "Unable to queue verification email. Please try again.", requestID(r)))
			return true
		}
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SIGNUP_UNAVAILABLE", "Account could not be created", requestID(r)))
			return true
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "Check your email to verify your Testagram account."})
		return true
	case "resend-verification":
		email, emailErr := authEmail(in.Email)
		if emailErr == nil {
			allowed, rateErr := s.authRateAllowed(ctx, "verify-resend-ip", ip, 5)
			if rateErr == nil && allowed {
				allowed, rateErr = s.authRateAllowed(ctx, "verify-resend-email", email, 3)
			}
			if rateErr == nil && allowed {
				token, tokenErr := authRandomToken()
				otp, otpErr := authRandomOTP()
				otpHash, otpHashErr := authOTPHash(otp)
				if tokenErr == nil && otpErr == nil && otpHashErr == nil {
					var savedEmail string
					tx, txErr := s.db.Begin(ctx)
					if txErr == nil {
						defer func() { _ = tx.Rollback(context.Background()) }()
						updateErr := tx.QueryRow(ctx, `
							UPDATE testagram_auth.identities
							SET verification_token_hash=$2,verification_expires_at=now()+interval '24 hours',
							    verification_otp_hash=$3,verification_otp_expires_at=now()+interval '10 minutes',
							    verification_otp_attempts=0,updated_at=now()
							WHERE email_normalized=$1 AND email_verified_at IS NULL RETURNING email
						`, email, authTokenHash(token), otpHash).Scan(&savedEmail)
						if updateErr == nil {
							base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
							link := base + "/verify-email?token=" + url.QueryEscape(token)
							updateErr = enqueueAuthEmailWith(ctx, tx, savedEmail, "Your Testagram verification code", "<p>Enter this verification code in Testagram:</p><p style=\"font-size:28px;font-weight:700;letter-spacing:8px\">"+otp+"</p><p>Your code expires in 10 minutes and can be used once.</p><p><a href=\""+link+"\">Verify email</a> using the secure link (expires in 24 hours).</p>")
						}
						if updateErr == nil {
							_ = tx.Commit(ctx)
						}
					}
				}
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the address has an unverified Testagram account, a verification email will be sent."})
		return true
	case "verify-email":
		token := strings.TrimSpace(in.Token)
		if token == "" {
			token = strings.TrimSpace(r.URL.Query().Get("token"))
		}
		isOTP := len(token) == 6
		if isOTP {
			for _, digit := range token {
				if digit < '0' || digit > '9' {
					isOTP = false
					break
				}
			}
		}
		var userID, email, username string
		var err error
		if isOTP {
			email, err = authEmail(in.Email)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, errorBody("INVALID_OTP", "Verification code is invalid or expired", requestID(r)))
				return true
			}
			allowed, rateErr := s.authRateAllowed(ctx, "verify-otp-ip", ip, 10)
			if rateErr == nil && allowed {
				allowed, rateErr = s.authRateAllowed(ctx, "verify-otp-email", email, 5)
			}
			if rateErr != nil || !allowed {
				writeJSON(w, http.StatusTooManyRequests, errorBody("OTP_RATE_LIMITED", "Too many verification attempts. Request a new code and try again later.", requestID(r)))
				return true
			}
			tx, txErr := s.db.Begin(ctx)
			if txErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to verify account", requestID(r)))
				return true
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			var storedHash []byte
			var expires time.Time
			var attempts int
			txErr = tx.QueryRow(ctx, `
				SELECT verification_otp_hash,verification_otp_expires_at,verification_otp_attempts
				FROM testagram_auth.identities
				WHERE email_normalized=$1 AND email_verified_at IS NULL
				FOR UPDATE
			`, email).Scan(&storedHash, &expires, &attempts)
			if txErr != nil || len(storedHash) != sha256.Size || !expires.After(time.Now().UTC()) || attempts >= 5 {
				writeJSON(w, http.StatusBadRequest, errorBody("INVALID_OTP", "Verification code is invalid or expired", requestID(r)))
				return true
			}
			otpHash, hashErr := authOTPHash(token)
			if hashErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("EMAIL_UNAVAILABLE", "Account verification is temporarily unavailable", requestID(r)))
				return true
			}
			if subtle.ConstantTimeCompare(otpHash, storedHash) != 1 {
				_, txErr = tx.Exec(ctx, `
					UPDATE testagram_auth.identities
					SET verification_otp_attempts=verification_otp_attempts+1,updated_at=now()
					WHERE email_normalized=$1 AND verification_otp_attempts<5
				`, email)
				if txErr == nil {
					txErr = tx.Commit(ctx)
				}
				if txErr != nil {
					writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to verify account", requestID(r)))
					return true
				}
				writeJSON(w, http.StatusBadRequest, errorBody("INVALID_OTP", "Verification code is invalid or expired", requestID(r)))
				return true
			}
			txErr = tx.QueryRow(ctx, `
				UPDATE testagram_auth.identities
				SET email_verified_at=now(),verification_token_hash=NULL,verification_expires_at=NULL,
				    verification_otp_hash=NULL,verification_otp_expires_at=NULL,
				    verification_otp_attempts=0,updated_at=now()
				WHERE email_normalized=$1 AND email_verified_at IS NULL
				RETURNING user_id::text,email,username
			`, email).Scan(&userID, &email, &username)
			if txErr == nil {
				txErr = tx.Commit(ctx)
			}
			if txErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to verify account", requestID(r)))
				return true
			}
		} else {
			if len(token) < 32 || len(token) > 128 {
				writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOKEN", "Verification token is invalid or expired", requestID(r)))
				return true
			}
			err = s.db.QueryRow(ctx, `
				UPDATE testagram_auth.identities
				SET email_verified_at=COALESCE(email_verified_at,now()), updated_at=now(),
				    verification_token_hash=NULL,verification_expires_at=NULL,
				    verification_otp_hash=NULL,verification_otp_expires_at=NULL,
				    verification_otp_attempts=0
				WHERE verification_token_hash=$1 AND verification_expires_at>now()
				RETURNING user_id::text,email,username
			`, authTokenHash(token)).Scan(&userID, &email, &username)
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOKEN", "Verification token is invalid or expired", requestID(r)))
				return true
			}
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to verify account", requestID(r)))
				return true
			}
		}
		_, err = s.db.Exec(ctx, `
			INSERT INTO testagram_local.accounts(user_id,username,display_name)
			VALUES($1::uuid,$2,$2)
			ON CONFLICT(user_id) DO NOTHING
		`, userID, username)
		if err != nil {
			writeJSON(w, http.StatusConflict, errorBody("PROFILE_CREATE_FAILED", "Email verified but profile could not be initialized; retry verification after resolving the username conflict", requestID(r)))
			return true
		}
		_, _ = s.db.Exec(ctx, `UPDATE testagram_auth.identities SET verification_token_hash=NULL,verification_expires_at=NULL,updated_at=now() WHERE user_id=$1::uuid`, userID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Email verified. You can now sign in."})
		return true
	case "login":
		email, err := authEmail(in.Email)
		if err != nil || len(in.Password) > 128 {
			writeJSON(w, http.StatusUnauthorized, errorBody("INVALID_CREDENTIALS", "Email or password is incorrect", requestID(r)))
			return true
		}
		ok, err := s.authRateAllowed(ctx, "login-ip", ip, 20)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Authentication store is unavailable", requestID(r)))
			return true
		}
		if !ok {
			writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many sign-in attempts. Try again later.", requestID(r)))
			return true
		}
		ok, err = s.authRateAllowed(ctx, "login-email", email, 10)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Authentication store is unavailable", requestID(r)))
			return true
		}
		if !ok {
			writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many sign-in attempts. Try again later.", requestID(r)))
			return true
		}
		var userID, hash string
		var verified, disabled, resetRequired bool
		var version int64
		err = s.db.QueryRow(ctx, `SELECT i.user_id::text,i.password_hash,i.email_verified_at IS NOT NULL,
			i.token_version,i.disabled_at IS NOT NULL,i.password_reset_required
			FROM testagram_auth.identities i WHERE i.email_normalized=$1`, email).Scan(
			&userID, &hash, &verified, &version, &disabled, &resetRequired)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = authPasswordVerify(authDummyPasswordHash, in.Password)
			writeJSON(w, http.StatusUnauthorized, errorBody("INVALID_CREDENTIALS", "Email or password is incorrect.", requestID(r)))
			return true
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Authentication store is unavailable", requestID(r)))
			return true
		}
		if !authPasswordVerify(hash, in.Password) || disabled {
			writeJSON(w, http.StatusUnauthorized, errorBody("INVALID_CREDENTIALS", "Email or password is incorrect.", requestID(r)))
			return true
		}
		if !verified {
			// A valid password proves control of this account; only then reveal
			// the actionable verification state rather than enabling enumeration.
			writeJSON(w, http.StatusForbidden, errorBody("EMAIL_VERIFICATION_REQUIRED", "Verify your Testagram email before signing in.", requestID(r)))
			return true
		}
		if resetRequired {
			writeJSON(w, http.StatusForbidden, errorBody("PASSWORD_RESET_REQUIRED", "Reset your password before signing in.", requestID(r)))
			return true
		}
		if authPasswordNeedsRehash(hash) {
			upgradedHash, hashErr := authPasswordHash(in.Password)
			if hashErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("CREDENTIAL_UPGRADE_FAILED", "Secure credential upgrade could not be completed", requestID(r)))
				return true
			}
			result, updateErr := s.db.Exec(ctx, `UPDATE testagram_auth.identities SET password_hash=$2,updated_at=now() WHERE user_id=$1::uuid AND password_hash=$3`, userID, upgradedHash, hash)
			if updateErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("CREDENTIAL_UPGRADE_FAILED", "Secure credential upgrade could not be completed", requestID(r)))
				return true
			}
			if result.RowsAffected() != 1 {
				// The password or account state changed while the legacy hash was
				// being upgraded. Do not continue with a potentially stale login.
				writeJSON(w, http.StatusUnauthorized, errorBody("INVALID_CREDENTIALS", "Email or password is incorrect.", requestID(r)))
				return true
			}
		}
		tokens, err := issueNativeSession(ctx, s.db, userID, email, version, r.UserAgent(), ip)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SESSION_CREATE_FAILED", "A secure session could not be created", requestID(r)))
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": tokens, "error": nil, "request_id": requestID(r)})
		return true
	case "refresh":
		tokens, err := s.rotateNativeRefresh(ctx, in.RefreshToken, r.UserAgent(), ip)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorBody("INVALID_REFRESH_TOKEN", "Refresh token is invalid, expired, or has already been used.", requestID(r)))
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": tokens, "error": nil, "request_id": requestID(r)})
		return true
	case "logout", "logout-all":
		bearer, err := nativeBearerToken(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorBody("AUTH_REQUIRED", "A valid native access token is required.", requestID(r)))
			return true
		}
		claims, status, err := verifyNativeAccessToken(ctx, s.db, bearer)
		if err != nil {
			writeJSON(w, status, errorBody("AUTH_REQUIRED", "The native session is invalid or expired.", requestID(r)))
			return true
		}
		if err := s.revokeNativeSession(ctx, claims, action == "logout-all", ip); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SESSION_REVOKE_FAILED", "Session revocation could not be completed.", requestID(r)))
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"revoked": true}, "error": nil, "request_id": requestID(r)})
		return true
	case "forgot-password":
		email, err := authEmail(in.Email)
		if err != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the account exists, password recovery instructions will be sent."})
			return true
		}
		ok, err := s.authRateAllowed(ctx, "recovery-ip", ip, 8)
		if err != nil || !ok {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the account exists, password recovery instructions will be sent."})
			return true
		}
		ok, err = s.authRateAllowed(ctx, "recovery-email", email, 3)
		if err != nil || !ok {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the account exists, password recovery instructions will be sent."})
			return true
		}
		token, err := authRandomToken()
		if err != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the account exists, password recovery instructions will be sent."})
			return true
		}
		var savedEmail string
		tx, txErr := s.db.Begin(ctx)
		if txErr == nil {
			defer func() { _ = tx.Rollback(context.Background()) }()
			_, txErr = tx.Exec(ctx, `
				UPDATE testagram_auth.password_reset_tokens
				SET consumed_at=now()
				WHERE user_id=(SELECT user_id FROM testagram_auth.identities WHERE email_normalized=$1)
				  AND consumed_at IS NULL
			`, email)
			if txErr == nil {
				err = tx.QueryRow(ctx, `
					WITH eligible AS (
						SELECT user_id,email FROM testagram_auth.identities
						WHERE email_normalized=$1 AND email_verified_at IS NOT NULL AND disabled_at IS NULL
						FOR UPDATE
					), inserted AS (
						INSERT INTO testagram_auth.password_reset_tokens(token_hash,user_id,expires_at,request_ip_hash)
						SELECT $2,user_id,now()+interval '30 minutes',$3 FROM eligible
						RETURNING user_id
					)
					SELECT eligible.email FROM eligible JOIN inserted USING(user_id)
				`, email, authTokenHash(token), authFingerprint(ip)).Scan(&savedEmail)
				txErr = err
			}
			if txErr == nil && savedEmail != "" {
				base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
				link := base + "/reset-password?token=" + url.QueryEscape(token)
				txErr = enqueueAuthEmailWith(ctx, tx, savedEmail, "Reset your Testagram password", "<p>Use this link to reset your Testagram password:</p><p><a href=\""+link+"\">Reset password</a></p><p>This link expires in 30 minutes and can only be used once. If you did not request this, ignore this email.</p>")
			}
			if txErr == nil {
				txErr = tx.Commit(ctx)
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the account exists, password recovery instructions will be sent."})
		return true
	case "reset-password":
		token := strings.TrimSpace(in.Token)
		if len(token) < 32 || len(token) > 128 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOKEN", "Reset token is invalid or expired", requestID(r)))
			return true
		}
		// Rate-limit unauthenticated recovery before running the deliberately
		// expensive Argon2id KDF. A cheap token preflight avoids spending KDF
		// resources on unknown, expired or already-consumed capabilities; the
		// token is re-locked and revalidated in the transaction below.
		allowed, rateErr := s.authRateAllowed(ctx, "password-reset-ip", ip, 10)
		if rateErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Password recovery is temporarily unavailable", requestID(r)))
			return true
		}
		if !allowed {
			writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many password reset attempts. Try again later.", requestID(r)))
			return true
		}
		allowed, rateErr = s.authRateAllowed(ctx, "password-reset-token", token, 3)
		if rateErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Password recovery is temporarily unavailable", requestID(r)))
			return true
		}
		if !allowed {
			writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "This password reset link has been attempted too many times. Request a new link.", requestID(r)))
			return true
		}
		var tokenEligible bool
		if err := s.db.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM testagram_auth.password_reset_tokens
				WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now()
			)
		`, authTokenHash(token)).Scan(&tokenEligible); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to validate reset token", requestID(r)))
			return true
		}
		if !tokenEligible {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOKEN", "Reset token is invalid or expired", requestID(r)))
			return true
		}
		hash, err := authPasswordHash(in.Password)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PASSWORD", "Password must be 12–128 bytes", requestID(r)))
			return true
		}
		tx, err := s.db.Begin(ctx)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to reset password", requestID(r)))
			return true
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var userID string
		err = tx.QueryRow(ctx, `
			SELECT user_id::text FROM testagram_auth.password_reset_tokens
			WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now()
			FOR UPDATE
		`, authTokenHash(token)).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOKEN", "Reset token is invalid or expired", requestID(r)))
			return true
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to validate reset token", requestID(r)))
			return true
		}
		var accountEmail string
		if err := tx.QueryRow(ctx, "SELECT email FROM testagram_auth.identities WHERE user_id=$1::uuid", userID).Scan(&accountEmail); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to load account notification address", requestID(r)))
			return true
		}
		identityTag, updateErr := tx.Exec(ctx, `
			UPDATE testagram_auth.identities SET password_hash=$2,password_reset_required=false,
				reset_token_hash=NULL,reset_expires_at=NULL,password_changed_at=now(),
				token_version=token_version+1,updated_at=now()
			WHERE user_id=$1::uuid AND email_verified_at IS NOT NULL AND disabled_at IS NULL
		`, userID, hash)
		if updateErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to update password", requestID(r)))
			return true
		}
		if identityTag.RowsAffected() != 1 {
			writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This account cannot reset its password.", requestID(r)))
			return true
		}
		if _, err = tx.Exec(ctx, "UPDATE testagram_auth.password_reset_tokens SET consumed_at=now() WHERE token_hash=$1 AND consumed_at IS NULL", authTokenHash(token)); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to consume reset token", requestID(r)))
			return true
		}
		if _, err = tx.Exec(ctx, "UPDATE testagram_auth.sessions SET revoked_at=COALESCE(revoked_at,now()),revoke_reason='password_reset' WHERE user_id=$1::uuid AND revoked_at IS NULL", userID); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to revoke existing sessions", requestID(r)))
			return true
		}
		if _, err = tx.Exec(ctx, `INSERT INTO testagram_auth.security_audit(subject_user_id,event_type,outcome,ip_hash)
			VALUES($1::uuid,'auth.password_reset','success',$2)`, userID, authFingerprint(ip)); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to record password reset", requestID(r)))
			return true
		}
		if err = tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AUTH_STORE_UNAVAILABLE", "Unable to commit password reset", requestID(r)))
			return true
		}
		_ = s.sendAuthEmail(ctx, accountEmail, "Your Testagram password was changed",
			"<p>Your Testagram password was successfully changed.</p><p>If you did not make this change, contact Testagram support immediately.</p>")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Password reset. Sign in with your new password."})
		return true
	default:
		writeJSON(w, http.StatusNotFound, errorBody("AUTH_ROUTE_NOT_FOUND", "Authentication route not found", requestID(r)))
		return true
	}
}
