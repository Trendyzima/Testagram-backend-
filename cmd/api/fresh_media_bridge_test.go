package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFreshMediaBridgeRejectsOwnerAndMetadataMismatch(t *testing.T) {
	valid := freshMediaBridgeRequest{
		OwnerID:"user_123.test",
		Key:"users/user_123.test/0123456789abcdef0123456789abcdef",
		Visibility:"private",
		R2Bucket:"testagram-media",
		B2Bucket:"testagram-b2-media",
		ContentType:"image/webp",
		SizeBytes:50_000,
		SHA256:strings.Repeat("a",64),
	}
	if !validFreshMediaBridgeRequest(valid) { t.Fatal("valid Appwrite media metadata rejected") }
	cases:=[]freshMediaBridgeRequest{
		{OwnerID:"other",Key:valid.Key,Visibility:"private",ContentType:valid.ContentType,SizeBytes:valid.SizeBytes,SHA256:valid.SHA256},
		{OwnerID:valid.OwnerID,Key:"users/user_123.test/not-a-digest",Visibility:"private",ContentType:valid.ContentType,SizeBytes:valid.SizeBytes,SHA256:valid.SHA256},
		{OwnerID:valid.OwnerID,Key:valid.Key,Visibility:"public",ContentType:valid.ContentType,SizeBytes:valid.SizeBytes,SHA256:valid.SHA256},
		{OwnerID:valid.OwnerID,Key:valid.Key,Visibility:"private",ContentType:"text/html",SizeBytes:valid.SizeBytes,SHA256:valid.SHA256},
		{OwnerID:valid.OwnerID,Key:valid.Key,Visibility:"private",ContentType:valid.ContentType,SizeBytes:501<<20,SHA256:valid.SHA256},
		{OwnerID:valid.OwnerID,Key:valid.Key,Visibility:"private",ContentType:valid.ContentType,SizeBytes:valid.SizeBytes,SHA256:"bad"},
	}
	for i, candidate:=range cases {
		if validFreshMediaBridgeRequest(candidate) { t.Fatalf("invalid media registration case %d accepted",i) }
	}
}

func TestFreshMediaBridgeUsesConstantTimeServiceTokenCheck(t *testing.T) {
	s:=&service{freshMediaInternalToken:strings.Repeat("t",32)}
	req:=httptest.NewRequest("POST","/internal/media/fresh/register",nil)
	req.Header.Set("X-Testagram-Fresh-Media-Token",strings.Repeat("t",32))
	if !s.authorizeFreshMediaBridge(req) { t.Fatal("valid service token rejected") }
	req.Header.Set("X-Testagram-Fresh-Media-Token",strings.Repeat("x",32))
	if s.authorizeFreshMediaBridge(req) { t.Fatal("invalid service token accepted") }
}
