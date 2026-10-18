package virtualstorage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestS3ProviderPresignsScopedOperations(t *testing.T) {
	p, err := NewS3Provider(S3Config{Endpoint:"https://objects.example.test", AccessKey:"access", SecretKey:"secret", Region:"us-east-1"})
	if err != nil { t.Fatal(err) }
	put, err := p.PresignPut(context.Background(), "private-media", "tenants/u1/a.jpg", "image/jpeg", 1024, 5*time.Minute)
	if err != nil { t.Fatal(err) }
	u, err := url.Parse(put)
	if err != nil { t.Fatal(err) }
	if u.Host!="objects.example.test" || u.Path!="/private-media/tenants/u1/a.jpg" || u.Query().Get("X-Amz-SignedHeaders")!="content-type;host" || len(u.Query().Get("X-Amz-Signature"))!=64 {
		t.Fatalf("unexpected signed PUT URL: %s", put)
	}
	get, err := p.PresignGet(context.Background(), "private-media", "tenants/u1/a.jpg", time.Minute)
	if err != nil { t.Fatal(err) }
	gu, _ := url.Parse(get)
	if gu.Query().Get("X-Amz-SignedHeaders")!="host" || gu.Query().Get("X-Amz-Signature")=="" { t.Fatalf("invalid signed GET URL: %s", get) }
	if strings.Contains(put, "secret") { t.Fatal("secret key leaked into signed URL") }
}

func TestS3ProviderListParsesContinuationAndMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method!="GET" || r.URL.Query().Get("list-type")!="2" || r.URL.Query().Get("prefix")!="tenants/u1/" { t.Errorf("unexpected list request: %s %s", r.Method, r.URL.String()) }
		w.Header().Set("Content-Type","application/xml")
		_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>tenants/u1/a.jpg</Key><Size>12</Size><ETag>"opaque"</ETag><LastModified>2026-10-18T10:00:00Z</LastModified></Contents></ListBucketResult>`))
	}))
	defer server.Close()
	p, err := NewS3Provider(S3Config{Endpoint:server.URL, AccessKey:"access", SecretKey:"secret"})
	if err != nil { t.Fatal(err) }
	items, cursor, err := p.List(context.Background(), "private-media", "tenants/u1/", "", 10)
	if err != nil { t.Fatal(err) }
	if len(items)!=1 || items[0].Key!="tenants/u1/a.jpg" || items[0].Size!=12 || items[0].ETag!="opaque" || cursor!="next" { t.Fatalf("unexpected list result: %+v cursor=%q",items,cursor) }
}

func TestNewS3ProviderRejectsInvalidConfiguration(t *testing.T) {
	for _, cfg := range []S3Config{
		{Endpoint:"file:///tmp",AccessKey:"a",SecretKey:"b"},
		{Endpoint:"https://objects.example.test/prefix",AccessKey:"a",SecretKey:"b"},
		{Endpoint:"https://objects.example.test",AccessKey:"a"},
	} {
		if _, err := NewS3Provider(cfg); err==nil { t.Errorf("expected invalid config rejected: %+v",cfg) }
	}
}
