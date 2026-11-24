package main

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

func TestParseNativeDSNHardBounce(t *testing.T) {
	raw := []byte("From: Mail Delivery Subsystem <mailer-daemon@example.test>\r\n" +
		"Message-ID: <dsn-001@example.test>\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=dsn-boundary\r\n\r\n" +
		"--dsn-boundary\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"Delivery failed.\r\n" +
		"--dsn-boundary\r\n" +
		"Content-Type: message/delivery-status\r\n\r\n" +
		"Reporting-MTA: dns; mx.example.test\r\n" +
		"Final-Recipient: rfc822; Person@example.test\r\n" +
		"Action: failed\r\n" +
		"Status: 5.1.1\r\n" +
		"Diagnostic-Code: smtp; 550 5.1.1 mailbox does not exist\r\n" +
		"--dsn-boundary--\r\n")
	report, err := parseNativeBounceReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if report.EventType != "dsn.bounced" || report.Recipient != "person@example.test" || report.Status != "5.1.1" {
		t.Fatalf("unexpected DSN report: %#v", report)
	}
	if report.RemoteMTA != "dns; mx.example.test" || !strings.Contains(report.Diagnostic, "550 5.1.1") {
		t.Fatalf("DSN diagnostic fields were not preserved: %#v", report)
	}
}

func TestParseNativeDSNDelayedAndDelivered(t *testing.T) {
	for _, tc := range []struct {
		action string
		status string
		want   string
	}{
		{action: "delayed", status: "4.2.0", want: "dsn.delayed"},
		{action: "delivered", status: "2.0.0", want: "dsn.delivered"},
	} {
		raw := []byte("Content-Type: multipart/report; report-type=delivery-status; boundary=x\r\n\r\n" +
			"--x\r\nContent-Type: message/delivery-status\r\n\r\n" +
			"Final-Recipient: rfc822; user@example.test\r\n" +
			"Action: " + tc.action + "\r\n" +
			"Status: " + tc.status + "\r\n" +
			"--x--\r\n")
		report, err := parseNativeBounceReport(raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.action, err)
		}
		if report.EventType != tc.want {
			t.Fatalf("%s mapped to %q, want %q", tc.action, report.EventType, tc.want)
		}
	}
}

func TestParseNativeARFComplaint(t *testing.T) {
	raw := []byte("Content-Type: multipart/report; report-type=feedback-report; boundary=arf-boundary\r\n\r\n" +
		"--arf-boundary\r\nContent-Type: text/plain\r\n\r\n" +
		"Complaint received.\r\n" +
		"--arf-boundary\r\nContent-Type: message/feedback-report\r\n\r\n" +
		"Feedback-Type: complaint\r\n" +
		"Original-Rcpt-To: rfc822; user@example.test\r\n" +
		"User-Agent: Example Mail System\r\n" +
		"--arf-boundary--\r\n")
	report, err := parseNativeBounceReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if report.EventType != "arf.complaint" || report.Recipient != "user@example.test" {
		t.Fatalf("unexpected ARF report: %#v", report)
	}
}

func TestParseNativeBounceRejectsMalformedOrUnsupportedReports(t *testing.T) {
	cases := [][]byte{
		[]byte("From: x@example.test\r\nContent-Type: text/plain\r\n\r\nnot a report"),
		[]byte("Content-Type: multipart/report; report-type=delivery-status; boundary=x\r\n\r\n--x\r\nContent-Type: message/delivery-status\r\n\r\nAction: failed\r\nStatus: 5.1.1\r\n--x--\r\n"),
		[]byte("Content-Type: multipart/report; report-type=delivery-status; boundary=x\r\n\r\n--x\r\nContent-Type: message/delivery-status\r\n\r\nFinal-Recipient: rfc822; user@example.test\r\nAction: failed\r\nStatus: 9.1.1\r\n--x--\r\n"),
	}
	for i, raw := range cases {
		if _, err := parseNativeBounceReport(raw); err == nil {
			t.Errorf("case %d: malformed or unsupported report was accepted", i)
		}
	}
}

func TestParseNativeBounceRejectsOversizedReport(t *testing.T) {
	raw := make([]byte, maxNativeBounceBytes+1)
	if _, err := parseNativeBounceReport(raw); err == nil {
		t.Fatal("oversized report was accepted")
	}
}

func TestParseNativeBounceRejectsOversizedMIMEPart(t *testing.T) {
	raw := []byte("Content-Type: multipart/report; report-type=delivery-status; boundary=x\r\n\r\n" +
		"--x\r\nContent-Type: message/delivery-status\r\n\r\n" +
		"Final-Recipient: rfc822; user@example.test\r\n" +
		"Action: failed\r\nStatus: 5.1.1\r\n" +
		strings.Repeat("X", (64<<10)+1) + "\r\n--x--\r\n")
	if _, err := parseNativeBounceReport(raw); err == nil {
		t.Fatal("oversized MIME report part was accepted")
	}
}

func TestParseNativeBounceRejectsExcessiveMIMEParts(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("Content-Type: multipart/report; report-type=delivery-status; boundary=x\r\n\r\n")
	for i := 0; i < 11; i++ {
		raw.WriteString("--x\r\nContent-Type: text/plain\r\n\r\npart\r\n")
	}
	raw.WriteString("--x--\r\n")
	if _, err := parseNativeBounceReport([]byte(raw.String())); err == nil {
		t.Fatal("report with excessive MIME parts was accepted")
	}
}

func TestNativeBounceSMTPRejectsExternalRecipientsAndCannotRelay(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go handleNativeBounceSMTP(context.Background(), nil, serverConn, nil)

	reader := bufio.NewReader(clientConn)
	readReply := func(wantPrefix string) {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SMTP reply: %v", err)
		}
		if !strings.HasPrefix(strings.TrimSpace(line), wantPrefix) {
			t.Fatalf("SMTP reply=%q, want prefix %q", strings.TrimSpace(line), wantPrefix)
		}
	}
	send := func(command string) {
		t.Helper()
		if _, err := clientConn.Write([]byte(command + "\r\n")); err != nil {
			t.Fatalf("send SMTP command %q: %v", command, err)
		}
	}

	readReply("220 ")
	send("EHLO test-client.example")
	readReply("250-mail.testagram.site")
	readReply("250 SIZE")
	send("MAIL FROM:<>")
	readReply("250 Sender accepted")
	send("RCPT TO:<attacker@example.test>")
	readReply("550 Recipient not accepted")
	send("DATA")
	readReply("503 MAIL FROM and valid RCPT TO required")
	send("QUIT")
	readReply("221 Bye")
}

func TestNativeBounceVERPTokenShape(t *testing.T) {
	valid := "b+abcdefghijklmnopqrstuvwxyzABCDEF"
	if !nativeBounceLocalPart.MatchString(valid) {
		t.Fatal("valid opaque VERP token was rejected")
	}
	for _, invalid := range []string{"b+short", "b+abcdefghijklmnopqrstuvwxyzABCDE!", "bounce+abcdefghijklmnopqrstuvwxyzABCDEF"} {
		if nativeBounceLocalPart.MatchString(invalid) {
			t.Errorf("invalid VERP local part was accepted: %q", invalid)
		}
	}
	if nativeBounceDomain != "bounces.testagram.site" {
		t.Fatalf("unexpected native bounce domain: %q", nativeBounceDomain)
	}
}
