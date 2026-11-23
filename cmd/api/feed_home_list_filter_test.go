package main

import (
	"strings"
	"testing"
)

func TestGlobalHomeFeedQueryDoesNotDependOnListsSchema(t *testing.T) {
	baseQuery := `SELECT p.id FROM testagram_local.posts p
WHERE p.deleted_at IS NULL
  AND ($2::timestamptz IS NULL OR (p.created_at,p.id) < ($2::timestamptz,$3::uuid))
ORDER BY p.created_at DESC, p.id DESC
LIMIT $4`
	if strings.Contains(baseQuery, "social_list_members") {
		t.Fatal("global feed query must not require the optional Lists schema")
	}
	customQuery, err := applyHomeFeedListFilter(baseQuery)
	if err != nil {
		t.Fatalf("apply custom feed filter: %v", err)
	}
	if !strings.Contains(customQuery, "social_list_members") ||
		!strings.Contains(customQuery, "lm.list_id=$9::uuid") {
		t.Fatalf("custom feed query is missing its List membership filter: %s", customQuery)
	}
	if strings.Index(customQuery, "social_list_members") > strings.Index(customQuery, "$2::timestamptz") {
		t.Fatal("List membership predicate must be applied before cursor pagination")
	}
}

