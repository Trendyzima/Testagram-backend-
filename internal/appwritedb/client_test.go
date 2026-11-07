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

func TestGetUserUsesServerCredentialsAndReturnsAccountStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/users/user_123" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Appwrite-Key") != "server-secret" {
			t.Fatal("server API key was not used")
		}
		_, _ = w.Write([]byte(`{"$id":"user_123","status":true}`))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL+"/v1", ProjectID:"project_1", APIKey:"server-secret", DatabaseID:"main_db"})
	if err != nil { t.Fatal(err) }
	user, err := client.GetUser(context.Background(), "user_123")
	if err != nil { t.Fatal(err) }
	if user.ID != "user_123" || !user.Status { t.Fatalf("unexpected user: %#v", user) }
}

func TestGetUserRejectsInvalidID(t *testing.T) {
	client, err := New(Config{Endpoint:"https://appwrite.example/v1", ProjectID:"project_1", APIKey:"server-secret", DatabaseID:"main_db"})
	if err != nil { t.Fatal(err) }
	if _, err := client.GetUser(context.Background(), "../admin"); err == nil {
		t.Fatal("invalid user ID accepted")
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

func TestAppwriteTransactionsAndAtomicQuotaOperations(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/databases/transactions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["ttl"] != float64(120) {
				t.Errorf("transaction TTL = %v", body["ttl"])
			}
			_, _ = w.Write([]byte(`{"$id":"tx_123"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/databases/transactions/tx_123":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["commit"] != true || body["rollback"] != false {
				t.Errorf("commit payload = %#v", body)
			}
			_, _ = w.Write([]byte(`{"$id":"tx_123","status":"committed"}`))
		case strings.HasSuffix(r.URL.Path, "/documents/owner_1/reserved_mib/increment"):
			if r.URL.Query().Get("transactionId") != "tx_123" {
				t.Errorf("missing transaction ID: %s", r.URL.RawQuery)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["value"] != float64(4) || body["max"] != float64(100) {
				t.Errorf("atomic increment payload = %#v", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"$id":"owner_1"}`))
		case strings.HasSuffix(r.URL.Path, "/documents"):
			if r.URL.Query().Get("transactionId") != "tx_123" {
				t.Errorf("missing transaction ID on create: %s", r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"$id":"res_123"}`))
		default:
			http.Error(w, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := client.CreateTransaction(context.Background(), 120)
	if err != nil || tx != "tx_123" {
		t.Fatalf("transaction=%q err=%v", tx, err)
	}
	if err := client.IncrementDocumentAttribute(context.Background(), "storage_quotas", "owner_1", "reserved_mib", 4, 100, tx); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateDocumentInTransaction(context.Background(), "storage_reservations", "res_123", map[string]any{"status": "reserved"}, tx); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateTransaction(context.Background(), tx, true); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("unexpected Appwrite calls: %#v", calls)
	}
}

func TestGetDocumentInTransactionAndListDocumentsEncodeQueries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/databases/db/collections/storage_reservations/documents/res_1":
			if r.URL.Query().Get("transactionId") != "tx_1" {
				t.Errorf("transaction read missing tx: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"$id":"res_1"}`))
		case "/v1/databases/db/collections/storage_reservations/documents":
			if r.URL.Query().Get("queries[]") != `{"method":"equal","column":"status","values":["reserved"]}` {
				t.Errorf("query encoding incorrect: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("limit") != "10" {
				t.Errorf("limit missing: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"documents":[{"$id":"res_1"}],"total":1}`))
		default:
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetDocumentInTransaction(context.Background(), "storage_reservations", "res_1", "tx_1"); err != nil {
		t.Fatal(err)
	}
	docs, err := client.ListDocuments(context.Background(), "storage_reservations", `{"method":"equal","column":"status","values":["reserved"]}`, 10)
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents=%d err=%v", len(docs), err)
	}
}

func TestListDocumentsPageReturnsTotalAndUsesOffset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("offset") != "20" {
			t.Errorf("unexpected pagination query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("{\"documents\":[{\"$id\":\"m1\"}],\"total\":37}"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	docs, total, err := client.ListDocumentsPage(context.Background(), "media_objects", "{\"method\":\"greaterThanEqual\",\"column\":\"created_at\",\"values\":[\"2026-11-01T00:00:00Z\"]}", 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || total != 37 {
		t.Fatalf("docs=%d total=%d", len(docs), total)
	}
}

func TestListDocumentsWithQueriesPreservesRepeatedParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries := r.URL.Query()["queries[]"]
		if len(queries) != 2 {
			t.Errorf("expected two repeated queries[] values, got %#v", queries)
		}
		if r.URL.Query().Get("limit") != "25" {
			t.Errorf("unexpected limit: %s", r.URL.Query().Get("limit"))
		}
		_, _ = w.Write([]byte("{\"documents\":[{\"$id\":\"p1\"}]}"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := client.ListDocumentsWithQueries(context.Background(), "posts", []string{
		"{\"method\":\"equal\",\"column\":\"status\",\"values\":[\"published\"]}",
		"{\"method\":\"orderDesc\",\"column\":\"created_at\"}",
	}, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || !strings.Contains(string(docs[0]), "p1") {
		t.Fatalf("unexpected documents: %#v", docs)
	}
}
