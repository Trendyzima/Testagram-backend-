package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"log"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxNativeBounceBytes = 1 << 20
var nativeBounceLocalPart = regexp.MustCompile(`^b\+([A-Za-z0-9_-]{32})$`)

type nativeBounceReport struct {
	Recipient string
	EventType string
	Status string
	Diagnostic string
	RemoteMTA string
}

func parseNativeBounceReport(raw []byte) (nativeBounceReport, error) {
	if len(raw) == 0 || len(raw) > maxNativeBounceBytes { return nativeBounceReport{}, errors.New("bounce report size outside allowed bounds") }
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil { return nativeBounceReport{}, err }
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/report" { return nativeBounceReport{}, errors.New("only RFC multipart/report feedback is accepted") }
	reportType := strings.ToLower(params["report-type"])
	if reportType != "delivery-status" && reportType != "feedback-report" { return nativeBounceReport{}, errors.New("unsupported mail report type") }
	reader := multipart.NewReader(message.Body, params["boundary"])
	var report nativeBounceReport
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) { break }
		if nextErr != nil { return nativeBounceReport{}, nextErr }
		partType, _, parseErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if parseErr != nil { _ = part.Close(); continue }
		if partType == "message/delivery-status" {
			data, readErr := io.ReadAll(io.LimitReader(part, 64<<10))
			_ = part.Close()
			if readErr != nil { return nativeBounceReport{}, readErr }
			fields := parseDSNFields(data)
			report.Recipient = addressFromDSN(fields["final-recipient"])
			report.Status = strings.TrimSpace(fields["status"])
			report.Diagnostic = safeReportField(fields["diagnostic-code"], 512)
			report.RemoteMTA = safeReportField(fields["remote-mta"], 255)
			switch strings.ToLower(strings.TrimSpace(fields["action"])) {
			case "delayed": report.EventType = "dsn.delayed"
			case "failed": report.EventType = "dsn.bounced"
			}
		}
		if partType == "message/feedback-report" {
			data, readErr := io.ReadAll(io.LimitReader(part, 64<<10))
			_ = part.Close()
			if readErr != nil { return nativeBounceReport{}, readErr }
			fields := parseDSNFields(data)
			if strings.EqualFold(strings.TrimSpace(fields["feedback-type"]), "complaint") {
				report.EventType = "arf.complaint"
				report.Recipient = addressFromDSN(fields["original-rcpt-to"])
				if report.Recipient == "" { report.Recipient = addressFromDSN(fields["original-recipient"]) }
			}
		}
	}
	if report.Recipient == "" || (report.EventType != "dsn.delayed" && report.EventType != "dsn.bounced" && report.EventType != "arf.complaint") {
		return nativeBounceReport{}, errors.New("report lacks a supported action and recipient")
	}
	if report.Status != "" && !regexp.MustCompile(`^[245]\.\d\.\d{1,3}$`).MatchString(report.Status) {
		return nativeBounceReport{}, errors.New("invalid enhanced status code")
	}
	return report, nil
}

func parseDSNFields(data []byte) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") { continue }
		i := strings.IndexByte(line, ':')
		if i <= 0 { continue }
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		value := strings.TrimSpace(line[i+1:])
		if key != "" { result[key] = value }
	}
	return result
}

func addressFromDSN(value string) string {
	if i := strings.Index(value, ";"); i >= 0 { value = value[i+1:] }
	value = strings.TrimSpace(value)
	if parsed, err := mail.ParseAddress(value); err == nil { return strings.ToLower(parsed.Address) }
	return ""
}

func safeReportField(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if len(value) > limit { value = value[:limit] }
	return value
}

func processNativeBounceReport(ctx context.Context, db *pgxpool.Pool, tokenHash []byte, raw []byte, report nativeBounceReport) error {
	if db == nil { return errors.New("bounce database unavailable") }
	fingerprint, err := mailRecipientFingerprint(report.Recipient)
	if err != nil { return err }
	reportHash := sha256.Sum256(raw)
	tx, err := db.Begin(ctx)
	if err != nil { return err }
	defer tx.Rollback(ctx)
	var outboxID int64
	var storedFingerprint []byte
	var providerMessageID string
	err = tx.QueryRow(ctx, `
		SELECT o.id,o.recipient_fingerprint,COALESCE(NULLIF(o.provider_message_id,''),'native-outbox-'||o.id::text)
		FROM testagram_mail.return_path_tokens t
		JOIN testagram_mail.outbox o ON o.id=t.outbox_id
		WHERE t.token_hash=$1 AND t.expires_at>now()
		FOR UPDATE OF t,o
	`, tokenHash).Scan(&outboxID,&storedFingerprint,&providerMessageID)
	if errors.Is(err, pgx.ErrNoRows) { return errors.New("unknown or expired native return-path token") }
	if err != nil { return err }
	if len(storedFingerprint) != len(fingerprint) || subtle.ConstantTimeCompare(storedFingerprint,fingerprint) != 1 {
		return errors.New("bounce report recipient does not match the original message")
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO testagram_mail.inbound_reports(token_hash,report_sha256,event_type,enhanced_status_code,diagnostic_code,remote_mta)
		VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''))
		ON CONFLICT(token_hash,report_sha256) DO NOTHING
	`,tokenHash,reportHash[:],report.EventType,report.Status,report.Diagnostic,report.RemoteMTA)
	if err != nil { return err }
	if tag.RowsAffected()==0 { return tx.Commit(ctx) }
	eventID := "native-report:"+hex.EncodeToString(reportHash[:])
	bounceType := ""
	if report.EventType=="dsn.bounced" {
		if strings.HasPrefix(report.Status,"5.") { bounceType="hard" } else { bounceType="unknown" }
	} else if report.EventType=="dsn.delayed" { bounceType="soft" }
	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_mail.delivery_events(
			provider_event_id,provider_message_id,event_type,recipient_fingerprint,
			smtp_status_code,bounce_type,rejection_reason,rejection_party,transport,
			enhanced_status_code,diagnostic_code,remote_mta,report_sha256
		) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),'native',NULLIF($5,''),NULLIF($7,''),NULLIF($8,''),$9)
		ON CONFLICT(provider_event_id) DO NOTHING
	`,eventID,providerMessageID,report.EventType,storedFingerprint,report.Status,bounceType,report.Diagnostic,report.RemoteMTA,reportHash[:])
	if err != nil { return err }
	switch report.EventType {
	case "dsn.delayed":
		_,err=tx.Exec(ctx,`UPDATE testagram_mail.outbox SET delivery_status='deferred' WHERE id=$1`,outboxID)
	case "dsn.bounced":
		_,err=tx.Exec(ctx,`UPDATE testagram_mail.outbox SET delivery_status='bounced',delivery_failed_at=COALESCE(delivery_failed_at,now()) WHERE id=$1`,outboxID)
		if err==nil && bounceType=="hard" {
			_,err=tx.Exec(ctx,`INSERT INTO testagram_mail.recipient_suppressions(recipient_fingerprint,reason,source_event_id) VALUES($1,'hard_bounce',$2) ON CONFLICT(recipient_fingerprint) DO NOTHING`,storedFingerprint,eventID)
		}
	case "arf.complaint":
		_,err=tx.Exec(ctx,`UPDATE testagram_mail.outbox SET delivery_status='complained',complained_at=COALESCE(complained_at,now()) WHERE id=$1`,outboxID)
		if err==nil {
			_,err=tx.Exec(ctx,`INSERT INTO testagram_mail.recipient_suppressions(recipient_fingerprint,reason,source_event_id) VALUES($1,'complaint',$2) ON CONFLICT(recipient_fingerprint) DO NOTHING`,storedFingerprint,eventID)
		}
	}
	if err != nil { return err }
	_,err=tx.Exec(ctx,`UPDATE testagram_mail.return_path_tokens SET consumed_at=CASE WHEN $2='dsn.delayed' THEN consumed_at ELSE COALESCE(consumed_at,now()) END WHERE token_hash=$1`,tokenHash,report.EventType)
	if err != nil { return err }
	return tx.Commit(ctx)
}

func startNativeBounceSMTP(ctx context.Context, db *pgxpool.Pool, addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil { return fmt.Errorf("listen for native mail feedback on %s: %w", addr, err) }
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				if ctx.Err() != nil { return }
				log.Printf("native mail feedback listener stopped: %v", acceptErr)
				return
			}
			go handleNativeBounceSMTP(ctx, db, conn)
		}
	}()
	return nil
}

func handleNativeBounceSMTP(ctx context.Context, db *pgxpool.Pool, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5*time.Minute))
	reader:=bufio.NewReaderSize(conn,8192)
	writer:=bufio.NewWriter(conn)
	reply:=func(code int,message string) bool { _,e:=fmt.Fprintf(writer,"%d %s\r\n",code,message); if e!=nil{return false}; return writer.Flush()==nil }
	if !reply(220,"mail.testagram.site Testagram native feedback receiver"){return}
	mailFrom:=""
	mailFromSeen:=false
	var tokenHash []byte
	for {
		line,err:=reader.ReadString('\n')
		if err!=nil{return}
		if len(line)>8192 { _=reply(500,"Line too long");return }
		command:=strings.TrimSpace(line)
		upper:=strings.ToUpper(command)
		switch {
		case strings.HasPrefix(upper,"EHLO ") || strings.HasPrefix(upper,"HELO "):
			_,_=writer.WriteString("250-mail.testagram.site\r\n250 SIZE 1048576\r\n"); if writer.Flush()!=nil{return}
		case upper=="NOOP": if !reply(250,"OK"){return}
		case upper=="RSET": mailFrom="";mailFromSeen=false;tokenHash=nil;if !reply(250,"Reset"){return}
		case upper=="QUIT": _=reply(221,"Bye");return
		case strings.HasPrefix(upper,"STARTTLS"):
			if !reply(454,"TLS not configured on this receiver"){return}
		case strings.HasPrefix(upper,"AUTH "):
			if !reply(503,"Authentication is not supported"){return}
		case strings.HasPrefix(upper,"MAIL FROM:"):
			value:=smtpPathValue(command[len("MAIL FROM:"):])
			if value!="" { if _,e:=mail.ParseAddress(value);e!=nil {if !reply(501,"Invalid sender"){return};continue} }
			mailFrom=value
			mailFromSeen=true
			if !reply(250,"Sender accepted"){return}
		case strings.HasPrefix(upper,"RCPT TO:"):
			if !mailFromSeen || len(tokenHash)>0 { if !reply(503,"MAIL FROM required; one recipient per transaction"){return}; continue }
			value:=smtpPathValue(command[len("RCPT TO:"):])
			parsed,e:=mail.ParseAddress(value)
			if e!=nil || !strings.EqualFold(parsed.Address[strings.LastIndex(parsed.Address,"@")+1:],nativeBounceDomain) {
				if !reply(550,"Recipient not accepted"){return};continue
			}
			local:=parsed.Address[:strings.LastIndex(parsed.Address,"@")]
			match:=nativeBounceLocalPart.FindStringSubmatch(local)
			if len(match)!=2 {if !reply(550,"Unknown feedback token"){return};continue}
			hash:=sha256.Sum256([]byte(match[1]))
			tokenHash=hash[:]
			if !reply(250,"Feedback recipient accepted"){return}
		case upper=="DATA":
			if !mailFromSeen || len(tokenHash)==0 {if !reply(503,"MAIL FROM and valid RCPT TO required"){return};continue}
			if !reply(354,"End data with <CRLF>.<CRLF>"){return}
			var data bytes.Buffer
			for {
				part,e:=reader.ReadString('\n'); if e!=nil{return}
				if part==" .\r\n" || part==".\r\n" || part==".\n" {break}
				if strings.HasPrefix(part,".."){part=part[1:]}
				if data.Len()+len(part)>maxNativeBounceBytes { _=reply(552,"Message exceeds size limit");return }
				data.WriteString(part)
			}
			report,e:=parseNativeBounceReport(data.Bytes())
			if e!=nil {if !reply(550,"Invalid delivery report"){return};continue}
			processCtx,cancel:=context.WithTimeout(ctx,8*time.Second)
			e=processNativeBounceReport(processCtx,db,tokenHash,data.Bytes(),report)
			cancel()
			if e!=nil {if !reply(550,"Feedback could not be correlated"){return};continue}
			if !reply(250,"Feedback recorded"){return}
			mailFrom="";tokenHash=nil
		default:
			if !reply(502,"Command not supported"){return}
		}
	}
}

func smtpPathValue(value string) string {
	value=strings.TrimSpace(value)
	if strings.HasPrefix(value,"<") && strings.Contains(value,">") { return strings.TrimSpace(value[1:strings.Index(value,">")]) }
	return ""
}

