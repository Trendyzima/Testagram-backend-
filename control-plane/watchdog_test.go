package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWatchdogServiceMappingIsFixed(t *testing.T) {
	cases:=map[string]string{
		"login":"algorithm",
		"feed_open":"algorithm",
		"post_publish":"worker,notification",
		"media_upload":"cdn,media,video-worker",
		"media_view":"cdn,media",
		"federation_interaction":"federation",
	}
	for event,want:=range cases {
		got,ok:=servicesForImpulse(event)
		if !ok || strings.Join(got,",")!=want { t.Fatalf("%s: got %v,%v want %s",event,got,ok,want) }
	}
	if _,ok:=servicesForImpulse("docker_command");ok { t.Fatal("arbitrary event must not activate services") }
}

func TestWatchdogEndpointsUseSeparateCredentials(t *testing.T) {
	s:=&Server{internal:"general-internal",watchdogToken:"worker-only-secret",watchdogIngressToken:"api-ingress-secret"}
	tests:=[]struct{name string;handler http.HandlerFunc;method,path,token string;want int}{
		{"enqueue-no-token",s.watchdogImpulses,http.MethodPost,"/v1/watchdog/impulses","",http.StatusUnauthorized},
		{"enqueue-worker-token",s.watchdogImpulses,http.MethodPost,"/v1/watchdog/impulses","worker-only-secret",http.StatusUnauthorized},
		{"claim-api-token",s.watchdogClaimImpulses,http.MethodPost,"/v1/watchdog/impulses/claim","api-ingress-secret",http.StatusUnauthorized},
		{"ack-api-token",s.watchdogAckImpulse,http.MethodPost,"/v1/watchdog/impulses/ack","api-ingress-secret",http.StatusUnauthorized},
	}
	for _,tt:=range tests { t.Run(tt.name,func(t *testing.T){
		req:=httptest.NewRequest(tt.method,tt.path,strings.NewReader("{}"))
		if tt.token!="" { req.Header.Set("Authorization","Bearer "+tt.token) }
		rec:=httptest.NewRecorder();tt.handler(rec,req)
		if rec.Code!=tt.want { t.Fatalf("wanted %d got %d: %s",tt.want,rec.Code,rec.Body.String()) }
	}) }
}

func TestWatchdogRejectsUnsupportedEventBeforeDatabase(t *testing.T) {
	s:=&Server{watchdogIngressToken:"api-ingress-secret"}
	req:=httptest.NewRequest(http.MethodPost,"/v1/watchdog/impulses",strings.NewReader(`{"idempotencyKey":"0123456789abcdef","eventType":"delete_all"}`))
	req.Header.Set("Authorization","Bearer api-ingress-secret")
	rec:=httptest.NewRecorder();s.watchdogImpulses(rec,req)
	if rec.Code!=http.StatusBadRequest { t.Fatalf("wanted 400 got %d: %s",rec.Code,rec.Body.String()) }
}

func TestWatchdogRejectsMalformedIdempotencyKeyBeforeDatabase(t *testing.T) {
	s:=&Server{watchdogIngressToken:"api-ingress-secret"}
	req:=httptest.NewRequest(http.MethodPost,"/v1/watchdog/impulses",strings.NewReader(`{"idempotencyKey":"short","eventType":"login"}`))
	req.Header.Set("Authorization","Bearer api-ingress-secret")
	rec:=httptest.NewRecorder();s.watchdogImpulses(rec,req)
	if rec.Code!=http.StatusBadRequest { t.Fatalf("wanted 400 got %d: %s",rec.Code,rec.Body.String()) }
}
