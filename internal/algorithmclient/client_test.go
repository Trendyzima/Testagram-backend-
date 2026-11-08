package algorithmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewRequiresStrongTokenAndSafeOrigin(t *testing.T) {
	if _, err := New("http://ranking.example.com:8092", strings.Repeat("x", 32), time.Second); err == nil {
		t.Fatal("non-loopback HTTP endpoint must be rejected")
	}
	if _, err := New("https://algorithm.internal", "short", time.Second); err == nil {
		t.Fatal("short internal token must be rejected")
	}
	if _, err := New("https://user:pass@algorithm.internal", strings.Repeat("x", 32), time.Second); err == nil {
		t.Fatal("endpoint credentials must be rejected")
	}
	if _, err := New("http://127.0.0.1:8092", strings.Repeat("x", 32), time.Second); err != nil {
		t.Fatalf("loopback development endpoint rejected: %v", err)
	}
	if _, err := New("http://algorithm:8092", strings.Repeat("x", 32), time.Second); err != nil {
		t.Fatalf("private Compose service hostname rejected: %v", err)
	}
}

func TestRankSendsAuthenticatedBoundedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/feed/rank" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
			t.Fatal("missing service authorization")
		}
		if r.Header.Get("X-Testagram-API-Version") != "1" || r.Header.Get("X-Request-ID") == "" {
			t.Fatal("missing versioned API contract headers")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"post-1","score":0.9,"reasons":["recent"]}]}`))
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("s", 32), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Rank(context.Background(), Request{
		Now: time.Now().UTC(), Limit: 1,
		Candidates: []Candidate{{ID: "post-1", AuthorID: "user-1", Visible: true, CreatedAt: time.Now().UTC()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "post-1" {
		t.Fatalf("unexpected response: %#v", got)
	}
}

func TestRankRejectsNon200AndOversizedCandidateBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("s", 32), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Rank(context.Background(), Request{Limit: 1, Candidates: []Candidate{{ID: "p", AuthorID: "u"}}})
	if err == nil {
		t.Fatal("expected upstream error")
	}
	_, err = client.Rank(context.Background(), Request{Limit: 1, Candidates: make([]Candidate, 201)})
	if err == nil {
		t.Fatal("expected candidate bound error")
	}
}

func TestRankDoesNotFollowRedirects(t *testing.T) {
	var destinationCalled bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"items\":[]}"))
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	client, err := New(redirect.URL, strings.Repeat("s", 32), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Rank(context.Background(), Request{Limit: 1, Candidates: []Candidate{{ID: "p", AuthorID: "u"}}})
	if err == nil {
		t.Fatal("expected redirect response to be rejected")
	}
	if destinationCalled {
		t.Fatal("ranking client followed an upstream redirect")
	}
}

func TestRankRejectsInvalidScoresAndDuplicateIDs(t *testing.T) {
	for name, body := range map[string]string{
		"out of range score": "{\"items\":[{\"id\":\"p\",\"score\":2}]}",
		"empty ID":           "{\"items\":[{\"id\":\" \",\"score\":0.5}]}",
		"duplicate ID":       "{\"items\":[{\"id\":\"p\",\"score\":0.5},{\"id\":\"p\",\"score\":0.4}]}",
		"unknown candidate":  "{\"items\":[{\"id\":\"not-supplied\",\"score\":0.5}]}",
		"too many reasons":   "{\"items\":[{\"id\":\"p\",\"score\":0.5,\"reasons\":[\"a\",\"b\",\"c\",\"d\",\"e\",\"f\",\"g\",\"h\",\"i\"]}]}",
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client, err := New(server.URL, strings.Repeat("s", 32), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Rank(context.Background(), Request{Limit: 2, Candidates: []Candidate{{ID: "p", AuthorID: "u"}}})
			if err == nil {
				t.Fatal("expected invalid ranker response to be rejected")
			}
		})
	}
}

func TestReadyChecksAlgorithmServiceHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Method != http.MethodGet {
			t.Fatalf("unexpected readiness request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("s", 32), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
}
