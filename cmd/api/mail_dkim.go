package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

const testagramDKIMDomain = "testagram.site"

func signNativeMailDKIM(message []byte) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE"))
	selector := strings.TrimSpace(os.Getenv("TESTAGRAM_DKIM_SELECTOR"))
	if path == "" { return nil, errors.New("TESTAGRAM_DKIM_PRIVATE_KEY_FILE is not configured") }
	if selector == "" { selector = "tg2026" }
	if len(selector) > 63 || strings.ContainsAny(selector, ".; \r\n") { return nil, errors.New("invalid DKIM selector") }
	info, err := os.Stat(path)
	if err != nil { return nil, fmt.Errorf("read DKIM key metadata: %w", err) }
	if info.Mode().Perm()&0077 != 0 { return nil, errors.New("DKIM private key permissions must not allow group or other access") }
	raw, err := os.ReadFile(path)
	if err != nil { return nil, fmt.Errorf("read DKIM private key: %w", err) }
	block, _ := pem.Decode(raw)
	if block == nil { return nil, errors.New("DKIM private key is not PEM encoded") }
	var key *rsa.PrivateKey
	if parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes); parseErr == nil { key, _ = parsed.(*rsa.PrivateKey) }
	if key == nil {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil { return nil, errors.New("DKIM private key must be RSA PKCS#8 or PKCS#1") }
	}
	if key.N.BitLen() < 2048 { return nil, errors.New("DKIM RSA key must be at least 2048 bits") }
	if err := key.Validate(); err != nil { return nil, errors.New("DKIM RSA private key failed validation") }
	headerEnd := bytes.Index(message, []byte("\r\n\r\n"))
	if headerEnd < 0 { return nil, errors.New("mail message has no header/body separator") }
	headerBlock, body := message[:headerEnd], message[headerEnd+4:]
	bodyHash := sha256.Sum256(dkimRelaxedBody(body))
	bodyHashText := base64.StdEncoding.EncodeToString(bodyHash[:])
	signedNames := []string{"from", "to", "subject", "date", "message-id", "mime-version", "content-type"}
	canonicalHeaders, err := dkimSelectHeaders(headerBlock, signedNames)
	if err != nil { return nil, err }
	unsigned := "v=1; a=rsa-sha256; c=relaxed/relaxed; d=" + testagramDKIMDomain +
		"; s=" + selector + "; h=" + strings.Join(signedNames, ":") + "; bh=" + bodyHashText + "; b="
	signingInput := append(append([]byte(nil), canonicalHeaders...), []byte(dkimRelaxedHeader("DKIM-Signature", unsigned))...)
	digest := sha256.Sum256(signingInput)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil { return nil, fmt.Errorf("DKIM signing failed: %w", err) }
	dkimHeader := "DKIM-Signature: " + strings.TrimSpace(strings.TrimSuffix(unsigned, "b=")) + "b=" + base64.StdEncoding.EncodeToString(signature) + "\r\n"
	result := make([]byte, 0, len(message)+len(dkimHeader))
	result = append(result, []byte(dkimHeader)...)
	result = append(result, message...)
	return result, nil
}

func dkimRelaxedBody(body []byte) []byte {
	lines := bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n"))
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		var b bytes.Buffer
		space := false
		for _, c := range line {
			if c == ' ' || c == '\t' { space = true; continue }
			if space && b.Len() > 0 { b.WriteByte(' ') }
			space = false
			b.WriteByte(c)
		}
		out = append(out, b.Bytes())
	}
	for len(out) > 0 && len(out[len(out)-1]) == 0 { out = out[:len(out)-1] }
	var canonical bytes.Buffer
	for _, line := range out { canonical.Write(line); canonical.WriteString("\r\n") }
	if len(out) == 0 { canonical.WriteString("\r\n") }
	return canonical.Bytes()
}

func dkimRelaxedHeader(name, value string) string {
	value = strings.ReplaceAll(value, "\r\n", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.ToLower(name) + ":" + strings.Join(strings.Fields(value), " ") + "\r\n"
}

func dkimSelectHeaders(raw []byte, names []string) ([]byte, error) {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	type field struct { name, value string }
	fields := make([]field, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if len(fields) == 0 { return nil, errors.New("malformed folded mail header") }
			fields[len(fields)-1].value += " " + strings.TrimSpace(line)
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 { return nil, errors.New("malformed mail header") }
		fields = append(fields, field{name: strings.ToLower(line[:i]), value: strings.TrimSpace(line[i+1:])})
	}
	var out bytes.Buffer
	for _, name := range names {
		found := false
		for i := len(fields)-1; i >= 0; i-- {
			if fields[i].name == name { out.WriteString(dkimRelaxedHeader(name, fields[i].value)); found = true; break }
		}
		if !found { return nil, fmt.Errorf("required DKIM header %s is missing", name) }
	}
	return out.Bytes(), nil
}
