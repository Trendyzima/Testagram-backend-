package main

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
)

func TestMailCipherEncryptsAndDecrypts(t *testing.T) {
	key := strings.Repeat("ab", 32)
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", key)
	aead, err := mailCipher()
	if err != nil { t.Fatal(err) }
	plain := []byte(`{"recipient":"person@example.test","subject":"test","html":"secret-link"}`)
	nonce := make([]byte, aead.NonceSize())
	for i := range nonce { nonce[i] = byte(i+1) }
	ciphertext := aead.Seal(nil, nonce, plain, []byte("testagram-mail-v1"))
	if string(ciphertext) == string(plain) { t.Fatal("mail payload was not encrypted") }
	got, err := aead.Open(nil, nonce, ciphertext, []byte("testagram-mail-v1"))
	if err != nil { t.Fatal(err) }
	if string(got) != string(plain) { t.Fatalf("decrypted payload mismatch: %q", got) }
	if _, err := hex.DecodeString(key); err != nil { t.Fatal(err) }
}

func TestMailCipherRejectsMissingOrWeakKey(t *testing.T) {
	for _, key := range []string{"", "abcd", strings.Repeat("z",64), strings.Repeat("ab",31)} {
		t.Run(key,func(t *testing.T) {
			t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY",key)
			if _,err:=mailCipher(); err==nil { t.Fatal("invalid mail encryption key accepted") }
		})
	}
}

func TestCloudflareMailRequiresConfiguredAccount(t *testing.T) {
	t.Setenv("CLOUDFLARE_EMAIL_ACCOUNT_ID","")
	t.Setenv("CLOUDFLARE_EMAIL_API_TOKEN","")
	t.Setenv("AUTH_FROM_EMAIL","")
	svc:=&service{}
	_,err:=svc.sendCloudflareMail(context.Background(),queuedMail{Recipient:"person@example.test",Subject:"test",HTML:"<p>test</p>"})
	if err==nil || !strings.Contains(err.Error(),"not configured") { t.Fatalf("expected safe not-configured error, got %v",err) }
}
