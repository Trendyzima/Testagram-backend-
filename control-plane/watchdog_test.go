package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServicesForImpulseAllowlist(t *testing.T) {
	cases:=map[string][]string{
		"login":{"algorithm"},
		"feed_open":{"algorithm"},
		"post_publish":{"data-centre"},
		"media_upload":{"cdn","data-centre"},
		"media_view":{"cdn","data-centre"},
		"federation_interaction":{"federation"},
	}
	for event,want:=range cases {
		got,ok:=servicesForImpulse(event)
		if !ok || strings.Join(got,",")!=strings.Join(want,",") { t.Fatalf("%s: got %v, %v; want %v",event,got,ok,want) }
	}
	if _,ok:=servicesForImpulse("admin_command"); ok { t.Fatal("unsupported event must not activate a service") }
}

func TestWatchdogRoutesRequireInternalBearer(t *testing.T) {
	s:=&Server{internal:"test-internal-token"}
	tests:=[]struct{name string; handler http.HandlerFunc; method,path string}{
		{"enqueue",s.watchdogImpulses,http.MethodPost,"/v1/watchdog/impulses"},
		{"claim",s.watchdogClaimImpulses,http.MethodPost,"/v1/watchdog/impulses/claim"},
		{"ack",s.watchdogAckImpulse,http.MethodPost,"/v1/watchdog/impulses/ack"},
		{"services",s.watchdogServices,http.MethodGet,"/v1/watchdog/services"},
	}
	for _,tt:=range tests { t.Run(tt.name,func(t *testing.T){
		req:=httptest.NewRequest(tt.method,tt.path,nil); rec:=httptest.NewRecorder(); tt.handler(rec,req)
		if rec.Code!=http.StatusUnauthorized { t.Fatalf("wanted 401, got %d",rec.Code) }
	})}
}

func TestWatchdogRejectsInvalidImpulseBeforeDatabase(t *testing.T) {
	s:=&Server{internal:"test-internal-token"}
	body:=`{"idempotencyKey":"short","eventType":"login"}`
	req:=httptest.NewRequest(http.MethodPost,"/v1/watchdog/impulses",strings.NewReader(body))
	req.Header.Set("Authorization","Bearer test-internal-token")
	rec:=httptest.NewRecorder(); s.watchdogImpulses(rec,req)
	if rec.Code!=http.StatusBadRequest { t.Fatalf("wanted 400, got %d: %s",rec.Code,rec.Body.String()) }
}

func TestWatchdogRejectsUnknownEventBeforeDatabase(t *testing.T) {
	s:=&Server{internal:"test-internal-token"}
	body:=`{"idempotencyKey":"0123456789abcdef","eventType":"delete_all"}`
	req:=httptest.NewRequest(http.MethodPost,"/v1/watchdog/impulses",strings.NewReader(body))
	req.Header.Set("Authorization","Bearer test-internal-token")
	rec:=httptest.NewRecorder(); s.watchdogImpulses(rec,req)
	if rec.Code!=http.StatusBadRequest { t.Fatalf("wanted 400, got %d: %s",rec.Code,rec.Body.String()) }
}
