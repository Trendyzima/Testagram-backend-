package appwritedb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateDocumentUsesServerCredentialsAndPayload(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.Header.Get("X-Appwrite-Project") != "project_1" || r.Header.Get("X-Appwrite-Key") != "server-secret" {
			t.Error("Appwrite server headers missing")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["documentId"] != "user_1" {
			t.Errorf("documentId = %v", payload["documentId"])
		}
		if payload["data"].(map[string]any)["handle"] != "tester" {
			t.Error("data missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$id":"user_1"}`))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL + "/v1", ProjectID: "project_1", APIKey: "server-secret", DatabaseID: "main_db"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateDocument(context.Background(), "profiles", "user_1", map[string]any{"handle": "tester"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/databases/main_db/collections/profiles/documents") {
		t.Fatalf("unexpected path: %s", gotPath)
	}
}

func TestNewRejectsNonLocalHTTPAndInvalidEndpoint(t *testing.T) {
	_, err := New(Config{Endpoint: "http://example.com/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err == nil {
		t.Fatal("expected non-local HTTP to be rejected")
	}
	_, err = New(Config{Endpoint: "https://example.com", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err == nil {
		t.Fatal("expected endpoint without /v1 to be rejected")
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client, err := New(Config{Endpoint: source.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetDocument(context.Background(), "profiles", "u1")
	if err == nil {
		t.Fatal("expected redirect response to fail")
	}
	if redirected {
		t.Fatal("client followed redirect")
	}
}
