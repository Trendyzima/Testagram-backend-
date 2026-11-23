package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrendingWordsEndpointFailsClosedWithoutDatabase(t *testing.T) {
	handler := (&service{}).routes()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/trends/words?limit=6", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("trending words without DB: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "SOCIAL_DATABASE_UNAVAILABLE") {
		t.Fatalf("expected the standard database-unavailable response, got %s", recorder.Body.String())
	}
}

func TestTrendingWordStopwordsAreUniqueAndNormalized(t *testing.T) {
	seen := make(map[string]struct{}, len(trendingWordStopwords))
	for _, word := range trendingWordStopwords {
		if word == "" || word != strings.ToLower(word) || strings.TrimSpace(word) != word {
			t.Fatalf("invalid stopword %q", word)
		}
		if _, exists := seen[word]; exists {
			t.Fatalf("duplicate stopword %q", word)
		}
		seen[word] = struct{}{}
	}
}
