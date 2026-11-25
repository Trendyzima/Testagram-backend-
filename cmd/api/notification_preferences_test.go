package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type notificationPreferenceTestRow struct {
	social   bool
	wallet   bool
	messages bool
	err      error
}

func (r notificationPreferenceTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 3 {
		return errors.New("unexpected notification preference scan shape")
	}
	*dest[0].(*bool) = r.social
	*dest[1].(*bool) = r.wallet
	*dest[2].(*bool) = r.messages
	return nil
}

type notificationPreferenceTestDB struct {
	row pgx.Row
}

func (db notificationPreferenceTestDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return db.row
}

func TestLoadNotificationPreferencesDefaultsEnabledWhenMissing(t *testing.T) {
	social, wallet, messages, err := loadNotificationPreferences(
		context.Background(),
		notificationPreferenceTestDB{row: notificationPreferenceTestRow{err: pgx.ErrNoRows}},
		"00000000-0000-4000-8000-000000000001",
	)
	if err != nil {
		t.Fatalf("loadNotificationPreferences() error = %v", err)
	}
	if !social || !wallet || !messages {
		t.Fatalf("missing preferences = (%v, %v, %v), want all enabled", social, wallet, messages)
	}
}

func TestLoadNotificationPreferencesReturnsPersistedValues(t *testing.T) {
	social, wallet, messages, err := loadNotificationPreferences(
		context.Background(),
		notificationPreferenceTestDB{row: notificationPreferenceTestRow{social: false, wallet: true, messages: false}},
		"00000000-0000-4000-8000-000000000001",
	)
	if err != nil {
		t.Fatalf("loadNotificationPreferences() error = %v", err)
	}
	if social || !wallet || messages {
		t.Fatalf("persisted preferences = (%v, %v, %v), want (false, true, false)", social, wallet, messages)
	}
}

func TestLoadNotificationPreferencesPropagatesDatabaseFailure(t *testing.T) {
	wantErr := errors.New("database unavailable")
	_, _, _, err := loadNotificationPreferences(
		context.Background(),
		notificationPreferenceTestDB{row: notificationPreferenceTestRow{err: wantErr}},
		"00000000-0000-4000-8000-000000000001",
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("loadNotificationPreferences() error = %v, want %v", err, wantErr)
	}
}

func TestWriteNotificationPreferencesReturnsBackendEnvelope(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/me/notification-preferences", nil)
	response := httptest.NewRecorder()
	writeNotificationPreferences(
		response,
		request,
		notificationPreferenceTestDB{row: notificationPreferenceTestRow{social: true, wallet: false, messages: true}},
		"00000000-0000-4000-8000-000000000001",
		"request-123",
	)
	if response.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, marker := range []string{
		"\"ok\":true",
		"\"push_social_enabled\":true",
		"\"push_wallet_enabled\":false",
		"\"push_messages_enabled\":true",
		"\"request_id\":\"request-123\"",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("response missing %s: %s", marker, body)
		}
	}
}
