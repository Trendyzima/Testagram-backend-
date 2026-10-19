package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const authPasswordRounds = 210000
const authTokenTTL = 24 * time.Hour

type authInput struct {
	Email    string `json:"email"`
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

type authClaims struct {
	Subject string `json:"sub"`
	Email string `json:"email"`
	Role string `json:"role"`
	Issuer string `json:"iss"`
	IssuedAt int64 `json:"iat"`
	ExpiresAt int64 `json:"exp"`
	TokenVersion int64 `json:"ver"`
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
	if _, err := rand.Read(raw); err != nil { return "", err }
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func authTokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// PBKDF2-HMAC-SHA256 password storage; salt and work factor are encoded with the hash.
func authPasswordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 128 {
		return "", errors.New("password must be between 12 and 128 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil { return "", err }
	dk := authPBKDF2([]byte(password), salt, authPasswordRounds, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", authPasswordRounds, base64.RawURLEncoding.EncodeToString(salt), base64.RawURLEncoding.EncodeToString(dk)), nil
}

func authPasswordVerify(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" { return false }
	var rounds int
	if _, err := fmt.Sscanf(parts[1], "%d", &rounds); err != nil || rounds < 100000 || rounds > 1000000 { return false }
	salt, err1 := base64.RawURLEncoding.DecodeString(parts[2])
	expected, err2 := base64.RawURLEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(salt) < 16 || len(expected) != 32 { return false }
	actual := authPBKDF2([]byte(password), salt, rounds, len(expected))
	return subtle.ConstantTimeCompare(actual, expected) == 1
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
			for j := range t { t[j] ^= u[j] }
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func authSignJWT(userID, email string, version int64) (string, error) {
	secret := strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET"))
	if len(secret) < 32 { return "", errors.New("TESTAGRAM_AUTH_JWT_SECRET must contain at least 32 characters") }
	now := time.Now().UTC()
	header, _ := json.Marshal(map[string]string{"alg":"HS256","typ":"JWT"})
	claims, _ := json.Marshal(authClaims{Subject:userID, Email:email, Role:"authenticated", Issuer:"testagram-auth", IssuedAt:now.Unix(), ExpiresAt:now.Add(15*time.Minute).Unix(), TokenVersion:version})
	unsigned := base64.RawURLEncoding.EncodeToString(header)+"."+base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(secret)); _, _ = mac.Write([]byte(unsigned))
	return unsigned+"."+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
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
	if err != nil { host = r.RemoteAddr }
	// Do not trust X-Forwarded-For unless deployment explicitly configures a trusted proxy.
	return host
}

func (s *service) authSendEmail(ctx context.Context, recipient, subject, html string) error {
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	port := strings.TrimSpace(env("SMTP_PORT", "587"))
	username := strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
	password := os.Getenv("SMTP_PASSWORD")
	from := strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL"))
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
	if host == "" || from == "" || base == "" || username == "" || password == "" {
		return errors.New("SMTP_HOST, SMTP_USERNAME, SMTP_PASSWORD, AUTH_FROM_EMAIL and AUTH_PUBLIC_BASE_URL are required")
	}
	if !strings.HasPrefix(base, "https://") { return errors.New("AUTH_PUBLIC_BASE_URL must use HTTPS") }
	if _, err := strconv.Atoi(port); err != nil { return errors.New("SMTP_PORT must be numeric") }
	if _, err := mail.ParseAddress(from); err != nil { return errors.New("AUTH_FROM_EMAIL is invalid") }
	if _, err := mail.ParseAddress(recipient); err != nil { return errors.New("recipient email is invalid") }
	address := net.JoinHostPort(host, port)
	dialer := &net.Dialer{Timeout: 4*time.Second}
	var conn net.Conn
	var err error
	if port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName:host, MinVersion:tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil { return err }
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5*time.Second))
	client, err := smtp.NewClient(conn, host)
	if err != nil { return err }
	defer client.Close()
	if port != "465" {
		if ok, _ := client.Extension("STARTTLS"); !ok { return errors.New("SMTP server does not support required STARTTLS") }
		if err := client.StartTLS(&tls.Config{ServerName:host, MinVersion:tls.VersionTLS12}); err != nil { return err }
	}
	if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil { return err }
	if err := client.Mail(from); err != nil { return err }
	if err := client.Rcpt(recipient); err != nil { return err }
	writer, err := client.Data()
	if err != nil { return err }
	message := "From: " + from + "\r\nTo: " + recipient + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + html
	if _, err := writer.Write([]byte(message)); err != nil { _ = writer.Close(); return err }
	if err := writer.Close(); err != nil { return err }
	return client.Quit()
}

func (s *service) authRespond(w http.ResponseWriter, r *http.Request, action string) bool {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",requestID(r)))
		return true
	}
	if r.Method == http.MethodOptions { w.WriteHeader(http.StatusNoContent); return true }
	if r.Method != http.MethodPost { writeJSON(w,http.StatusMethodNotAllowed,errorBody("METHOD_NOT_ALLOWED","POST required",requestID(r))); return true }
	r.Body = http.MaxBytesReader(w,r.Body,16<<10)
	defer r.Body.Close()
	var in authInput
	if err := decodeSingleJSON(r.Body,&in); err != nil {
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_JSON","Request body is invalid",requestID(r))); return true
	}
	if s.db == nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_STORE_UNAVAILABLE","Authentication store is unavailable",requestID(r))); return true }
	if (action == "signup" || action == "login") && len(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET"))) < 32 { writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_NOT_CONFIGURED","Independent authentication is not configured",requestID(r))); return true }
	ctx,cancel := context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	ip := authClientIP(r)
	switch action {
	case "signup":
		email, err := authEmail(in.Email)
		if err != nil || !authUsernamePattern.MatchString(in.Username) {
			writeJSON(w,http.StatusBadRequest,errorBody("INVALID_SIGNUP","Enter a valid email, username, and password of at least 12 characters",requestID(r))); return true
		}
		ok, err := s.authRateAllowed(ctx,"signup-ip",ip,8)
		if err != nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_STORE_UNAVAILABLE","Authentication store is unavailable",requestID(r))); return true }
		if !ok { writeJSON(w,http.StatusTooManyRequests,errorBody("RATE_LIMITED","Try again later",requestID(r))); return true }
		passwordHash, err := authPasswordHash(in.Password)
		if err != nil { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_PASSWORD","Password must be 12–128 bytes",requestID(r))); return true }
		token, err := authRandomToken()
		if err != nil { writeJSON(w,http.StatusInternalServerError,errorBody("TOKEN_FAILED","Unable to create verification token",requestID(r))); return true }
		var userID string
		err = s.db.QueryRow(ctx,`
			INSERT INTO testagram_auth.identities(email,username,password_hash,verification_token_hash,verification_expires_at)
			VALUES($1,$2,$3,$4,now()+interval '24 hours')
			ON CONFLICT DO NOTHING RETURNING user_id::text
		`,email,in.Username,passwordHash,authTokenHash(token)).Scan(&userID)
		if errors.Is(err,pgx.ErrNoRows) {
			writeJSON(w,http.StatusAccepted,map[string]any{"ok":true,"message":"If the address is eligible, a verification email will be sent."}); return true
		}
		if err != nil { writeJSON(w,http.StatusConflict,errorBody("SIGNUP_FAILED","Account could not be created",requestID(r))); return true }
		base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")),"/")
		link := base+"/verify-email?token="+url.QueryEscape(token)
		if err := s.authSendEmail(ctx,email,"Verify your Testagram account","<p>Verify your Testagram account:</p><p><a href=\""+link+"\">Verify email</a></p><p>This link expires in 24 hours.</p>"); err != nil {
			// Keep the token persisted for retry by a future delivery worker; never log it.
			writeJSON(w,http.StatusServiceUnavailable,errorBody("EMAIL_UNAVAILABLE","Account created but verification email could not be delivered. Configure email delivery and request another verification.",requestID(r))); return true
		}
		writeJSON(w,http.StatusAccepted,map[string]any{"ok":true,"message":"Check your email to verify your Testagram account."}); return true
	case "resend-verification":
		email, emailErr := authEmail(in.Email)
		if emailErr == nil {
			allowed, rateErr := s.authRateAllowed(ctx, "verify-resend-ip", ip, 5)
			if rateErr == nil && allowed {
				token, tokenErr := authRandomToken()
				if tokenErr == nil {
					var savedEmail string
					updateErr := s.db.QueryRow(ctx, `
						UPDATE testagram_auth.identities
						SET verification_token_hash=$2,verification_expires_at=now()+interval '24 hours',updated_at=now()
						WHERE email_normalized=$1 AND email_verified_at IS NULL RETURNING email
					`, email, authTokenHash(token)).Scan(&savedEmail)
					if updateErr == nil {
						base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
						link := base + "/verify-email?token=" + url.QueryEscape(token)
						_ = s.authSendEmail(ctx, savedEmail, "Verify your Testagram account", "<p>Verify your Testagram account:</p><p><a href=\""+link+"\">Verify email</a></p><p>This link expires in 24 hours.</p>")
					}
				}
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok":true,"message":"If the address has an unverified Testagram account, a verification email will be sent."})
		return true
	case "verify-email":
		token := strings.TrimSpace(in.Token)
		if token == "" { token = strings.TrimSpace(r.URL.Query().Get("token")) }
		if len(token) < 32 || len(token) > 128 { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_TOKEN","Verification token is invalid or expired",requestID(r))); return true }
		var userID,email,username string
		err := s.db.QueryRow(ctx,`
			UPDATE testagram_auth.identities
			SET email_verified_at=COALESCE(email_verified_at,now()), updated_at=now()
			WHERE verification_token_hash=$1 AND verification_expires_at>now()
			RETURNING user_id::text,email,username
		`,authTokenHash(token)).Scan(&userID,&email,&username)
		if errors.Is(err,pgx.ErrNoRows) {
			// Idempotently report success for an already-verified address only if a valid session isn't possible;
			// invalid tokens intentionally receive the same generic response.
			writeJSON(w,http.StatusBadRequest,errorBody("INVALID_TOKEN","Verification token is invalid or expired",requestID(r))); return true
		}
		if err != nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_STORE_UNAVAILABLE","Unable to verify account",requestID(r))); return true }
		_, err = s.db.Exec(ctx,`
			INSERT INTO testagram_local.accounts(user_id,username,display_name)
			VALUES($1::uuid,$2,$2)
			ON CONFLICT(user_id) DO NOTHING
		`,userID,username)
		if err != nil { writeJSON(w,http.StatusConflict,errorBody("PROFILE_CREATE_FAILED","Email verified but profile could not be initialized; retry verification after resolving the username conflict",requestID(r))); return true }
		_, _ = s.db.Exec(ctx, `UPDATE testagram_auth.identities SET verification_token_hash=NULL,verification_expires_at=NULL,updated_at=now() WHERE user_id=$1::uuid`, userID)
		writeJSON(w,http.StatusOK,map[string]any{"ok":true,"message":"Email verified. You can now sign in."}); return true
	case "login":
		email, err := authEmail(in.Email)
		if err != nil || len(in.Password) > 128 { writeJSON(w,http.StatusUnauthorized,errorBody("INVALID_CREDENTIALS","Email or password is incorrect",requestID(r))); return true }
		ok, err := s.authRateAllowed(ctx,"login-ip",ip,20)
		if err != nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_STORE_UNAVAILABLE","Authentication store is unavailable",requestID(r))); return true }
		if !ok { writeJSON(w,http.StatusTooManyRequests,errorBody("RATE_LIMITED","Too many sign-in attempts. Try again later.",requestID(r))); return true }
		ok, err = s.authRateAllowed(ctx,"login-email",email,10)
		if err != nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_STORE_UNAVAILABLE","Authentication store is unavailable",requestID(r))); return true }
		if !ok { writeJSON(w,http.StatusTooManyRequests,errorBody("RATE_LIMITED","Too many sign-in attempts. Try again later.",requestID(r))); return true }
		var userID,hash string; var verified bool; var version int64
		err = s.db.QueryRow(ctx,`SELECT user_id::text,password_hash,email_verified_at IS NOT NULL,token_version FROM testagram_auth.identities WHERE email_normalized=$1`,email).Scan(&userID,&hash,&verified,&version)
		if err != nil || !authPasswordVerify(hash,in.Password) || !verified {
			writeJSON(w,http.StatusUnauthorized,errorBody("INVALID_CREDENTIALS","Email or password is incorrect, or email verification is pending.",requestID(r))); return true
		}
		jwt, err := authSignJWT(userID,email,version)
		if err != nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_NOT_CONFIGURED","Independent authentication is not configured",requestID(r))); return true }
		writeJSON(w,http.StatusOK,map[string]any{"ok":true,"access_token":jwt,"token_type":"Bearer","expires_in":900,"user":map[string]any{"id":userID,"email":email}}); return true
	case "forgot-password":
		email, err := authEmail(in.Email)
		if err != nil { writeJSON(w,http.StatusAccepted,map[string]any{"ok":true,"message":"If the account exists, password recovery instructions will be sent."}); return true }
		ok, err := s.authRateAllowed(ctx,"recovery-ip",ip,8)
		if err != nil || !ok { writeJSON(w,http.StatusAccepted,map[string]any{"ok":true,"message":"If the account exists, password recovery instructions will be sent."}); return true }
		ok, err = s.authRateAllowed(ctx,"recovery-email",email,3)
		if err != nil || !ok { writeJSON(w,http.StatusAccepted,map[string]any{"ok":true,"message":"If the account exists, password recovery instructions will be sent."}); return true }
		token, err := authRandomToken()
		if err != nil { writeJSON(w,http.StatusAccepted,map[string]any{"ok":true,"message":"If the account exists, password recovery instructions will be sent."}); return true }
		var savedEmail string
		err = s.db.QueryRow(ctx,`
			UPDATE testagram_auth.identities SET reset_token_hash=$2,reset_expires_at=now()+interval '30 minutes',updated_at=now()
			WHERE email_normalized=$1 AND email_verified_at IS NOT NULL RETURNING email
		`,email,authTokenHash(token)).Scan(&savedEmail)
		if err == nil {
			base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")),"/")
			link := base+"/reset-password?token="+url.QueryEscape(token)
			if sendErr := s.authSendEmail(ctx,savedEmail,"Reset your Testagram password","<p>Use this link to reset your Testagram password:</p><p><a href=\""+link+"\">Reset password</a></p><p>This link expires in 30 minutes and can only be used once. If you did not request this, ignore this email.</p>"); sendErr != nil {
				writeJSON(w,http.StatusAccepted,map[string]any{"ok":true,"message":"If the account exists, password recovery instructions will be sent."}); return true
			}
		}
		writeJSON(w,http.StatusAccepted,map[string]any{"ok":true,"message":"If the account exists, password recovery instructions will be sent."}); return true
	case "reset-password":
		token := strings.TrimSpace(in.Token)
		if len(token)<32 || len(token)>128 { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_TOKEN","Reset token is invalid or expired",requestID(r))); return true }
		tokenHash := authTokenHash(token)
		var tokenValid bool
		if err := s.db.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_auth.identities WHERE reset_token_hash=$1 AND reset_expires_at>now() AND email_verified_at IS NOT NULL)`,tokenHash).Scan(&tokenValid); err != nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_STORE_UNAVAILABLE","Unable to validate reset token",requestID(r))); return true
		}
		if !tokenValid { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_TOKEN","Reset token is invalid or expired",requestID(r))); return true }
		hash, err := authPasswordHash(in.Password)
		if err != nil { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_PASSWORD","Password must be 12–128 bytes",requestID(r))); return true }
		tag, err := s.db.Exec(ctx,`
			UPDATE testagram_auth.identities SET password_hash=$2,reset_token_hash=NULL,reset_expires_at=NULL,
				password_changed_at=now(),token_version=token_version+1,updated_at=now()
			WHERE reset_token_hash=$1 AND reset_expires_at>now() AND email_verified_at IS NOT NULL
		`,tokenHash,hash)
		if err != nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("AUTH_STORE_UNAVAILABLE","Unable to reset password",requestID(r))); return true }
		if tag.RowsAffected()==0 { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_TOKEN","Reset token is invalid or expired",requestID(r))); return true }
		writeJSON(w,http.StatusOK,map[string]any{"ok":true,"message":"Password reset. Sign in with your new password."}); return true
	default:
		writeJSON(w,http.StatusNotFound,errorBody("AUTH_ROUTE_NOT_FOUND","Authentication route not found",requestID(r))); return true
	}
}
