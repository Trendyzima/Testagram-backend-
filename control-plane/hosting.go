package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	hostingSlugPattern = regexp.MustCompile("^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
	envKeyPattern      = regexp.MustCompile("^[A-Z][A-Z0-9_]{0,63}$")
	secretRefPattern   = regexp.MustCompile("^secret://[a-zA-Z0-9._/-]{1,180}$")
	sha256Pattern      = regexp.MustCompile("^[0-9a-f]{64}$")
)

type DomainProvisionRequest struct {
	Slug          string `json:"slug"`
	TargetService string `json:"targetService"`
}

type DeploymentRequest struct {
	AppName     string            `json:"appName"`
	Image       string            `json:"image"`
	ImageDigest string            `json:"imageDigest"`
	Arch        string            `json:"arch"`
	CPUMillis   int               `json:"cpuMillis"`
	MemoryBytes int64             `json:"memoryBytes"`
	StorageBytes int64             `json:"storageBytes"`
	PublicEnv   map[string]string `json:"publicEnv"`
	SecretRefs  map[string]string `json:"secretRefs"`
	Command     []string          `json:"command"`
	Volume      string            `json:"volume"`
}

type BackupManifestRequest struct {
	Kind       string `json:"kind"`
	StorageURI string `json:"storageUri"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
	SourceHost string `json:"sourceHost"`
}

type BackupVerifyRequest struct {
	ObservedSHA256 string `json:"observedSha256"`
	ObservedBytes  int64  `json:"observedBytes"`
}

type RecoveryDrillRequest struct {
	BackupID       string    `json:"backupId"`
	TargetID       string    `json:"targetId"`
	RestoredSHA256 string    `json:"restoredSha256"`
	Success        bool      `json:"success"`
	Notes          string    `json:"notes"`
	StartedAt      time.Time `json:"startedAt"`
	CompletedAt    time.Time `json:"completedAt"`
}

func (s *Server) registerHostingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/hosting/health", s.hostingHealth)
	mux.HandleFunc("/v1/hosting/nodes", s.hostingNodes)
	mux.HandleFunc("/v1/hosting/usage", s.hostingUsage)
	mux.HandleFunc("/v1/hosting/domains", s.hostingDomains)
	mux.HandleFunc("/v1/hosting/domains/", s.hostingDomainActions)
	mux.HandleFunc("/v1/hosting/tls/authorize", s.hostingTLSAuthorize)
	mux.HandleFunc("/v1/hosting/dns/records", s.hostingDNSRecords)
	mux.HandleFunc("/v1/hosting/deployments", s.hostingDeployments)
	mux.HandleFunc("/v1/hosting/deployments/", s.hostingDeploymentDetail)
	mux.HandleFunc("/v1/hosting/backups", s.hostingBackups)
	mux.HandleFunc("/v1/hosting/backups/", s.hostingBackupActions)
	mux.HandleFunc("/v1/hosting/recovery/drills", s.hostingRecoveryDrills)
}

func (s *Server) hostingHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { methodNotAllowed(w); return }
	if !s.internalOnly(w, r) { return }
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var nodes, staleNodes, deployments, pendingBackups int
	err := s.db.QueryRow(ctx, "select "+
		"(select count(*) from public.vps_nodes), "+
		"(select count(*) from public.vps_nodes where status <> 'online' or last_seen_at < now() - interval '45 seconds'), "+
		"(select count(*) from testagram_hosting.deployments where status in ('queued','assigned','running')), "+
		"(select count(*) from testagram_hosting.backup_manifests where status <> 'verified')").
		Scan(&nodes, &staleNodes, &deployments, &pendingBackups)
	if err != nil { reply(w, http.StatusServiceUnavailable, map[string]string{"error":"hosting health unavailable"}); return }
	reply(w, http.StatusOK, map[string]any{
		"status":"ok", "nodes":nodes, "staleNodes":staleNodes,
		"activeDeployments":deployments, "backupsNotVerified":pendingBackups,
		"sampledAt":time.Now().UTC(),
	})
}

func (s *Server) hostingNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { methodNotAllowed(w); return }
	if !s.internalOnly(w, r) { return }
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, "select n.id::text,n.name,n.platform,n.arch,n.cpu_cores,n.memory_bytes,n.storage_bytes,n.free_bytes,"+
		"n.status,n.last_seen_at,coalesce(x.active_workloads,0),coalesce(x.cpu_reserved,0),coalesce(x.memory_reserved,0),coalesce(x.storage_reserved,0) "+
		"from public.vps_nodes n left join lateral ("+
		"select count(*) filter (where status in ('assigned','running'))::int active_workloads,"+
		"coalesce(sum(cpu_millis) filter (where status in ('assigned','running')),0)::bigint cpu_reserved,"+
		"coalesce(sum(memory_bytes) filter (where status in ('assigned','running')),0)::bigint memory_reserved,"+
		"coalesce(sum(storage_bytes) filter (where status in ('assigned','running')),0)::bigint storage_reserved "+
		"from public.vps_workloads where node_id=n.id) x on true order by n.last_seen_at desc limit 500")
	if err != nil { reply(w, http.StatusServiceUnavailable, map[string]string{"error":"node inventory unavailable"}); return }
	defer rows.Close()
	items := make([]map[string]any,0)
	for rows.Next() {
		var id,name,platform,arch,status string
		var cpu int
		var memory,storage,free,cpuReserved,memoryReserved,storageReserved int64
		var lastSeen time.Time
		var active int
		if err:=rows.Scan(&id,&name,&platform,&arch,&cpu,&memory,&storage,&free,&status,&lastSeen,&active,&cpuReserved,&memoryReserved,&storageReserved); err!=nil {
			reply(w,http.StatusInternalServerError,map[string]string{"error":"node inventory decode failed"}); return
		}
		items=append(items,map[string]any{"id":id,"name":name,"platform":platform,"arch":arch,"cpuCores":cpu,
			"memoryBytes":memory,"storageBytes":storage,"freeBytes":free,"status":status,"lastSeenAt":lastSeen,
			"activeWorkloads":active,"reservedCpuMillis":cpuReserved,"reservedMemoryBytes":memoryReserved,"reservedStorageBytes":storageReserved})
	}
	if err:=rows.Err(); err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"node inventory unavailable"}); return }
	reply(w,http.StatusOK,map[string]any{"nodes":items,"count":len(items)})
}

func (s *Server) hostingUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { methodNotAllowed(w); return }
	if !s.internalOnly(w, r) { return }
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	var nodes, freshNodes, active, queued, failed int
	var cpuReserved, memoryReserved, storageReserved int64
	err:=s.db.QueryRow(ctx, "select "+
		"(select count(*) from public.vps_nodes), "+
		"(select count(*) from public.vps_nodes where status='online' and last_seen_at > now()-interval '45 seconds'), "+
		"(select count(*) from public.vps_workloads where status in ('assigned','running')), "+
		"(select count(*) from public.vps_workloads where status='queued'), "+
		"(select count(*) from public.vps_workloads where status='failed'), "+
		"coalesce((select sum(cpu_millis) from public.vps_workloads where status in ('assigned','running')),0), "+
		"coalesce((select sum(memory_bytes) from public.vps_workloads where status in ('assigned','running')),0), "+
		"coalesce((select sum(storage_bytes) from public.vps_workloads where status in ('assigned','running')),0)").
		Scan(&nodes,&freshNodes,&active,&queued,&failed,&cpuReserved,&memoryReserved,&storageReserved)
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"usage query unavailable"}); return }
	reply(w,http.StatusOK,map[string]any{"nodes":nodes,"freshNodes":freshNodes,"activeWorkloads":active,
		"queuedWorkloads":queued,"failedWorkloads":failed,"reservedCpuMillis":cpuReserved,
		"reservedMemoryBytes":memoryReserved,"reservedStorageBytes":storageReserved,"sampledAt":time.Now().UTC()})
}

func normalizedBaseDomain() (string,error) {
	domain:=strings.ToLower(strings.TrimSuffix(strings.TrimSpace(os.Getenv("TESTAGRAM_BASE_DOMAIN")),"."))
	if domain=="" { return "",errors.New("TESTAGRAM_BASE_DOMAIN is not configured") }
	if len(strings.Split(domain,".")) < 2 { return "",errors.New("TESTAGRAM_BASE_DOMAIN must be a fully qualified domain") }
	if strings.Contains(domain,":") || net.ParseIP(domain)!=nil || len(domain)>253 { return "",errors.New("TESTAGRAM_BASE_DOMAIN is invalid") }
	for _,label:=range strings.Split(domain,".") { if !hostingSlugPattern.MatchString(label) { return "",errors.New("TESTAGRAM_BASE_DOMAIN is invalid") } }
	return domain,nil
}

func domainTargets() map[string]string {
	out:=map[string]string{}
	for _,entry:=range strings.Split(os.Getenv("TESTAGRAM_DOMAIN_TARGETS"),",") {
		parts:=strings.SplitN(strings.TrimSpace(entry),"=",2)
		if len(parts)!=2 { continue }
		name:=strings.TrimSpace(parts[0]); raw:=strings.TrimSpace(parts[1])
		if !hostingSlugPattern.MatchString(name) { continue }
		u,err:=url.Parse(raw)
		if err!=nil || (u.Scheme!="http" && u.Scheme!="https") || u.Host=="" || u.User!=nil || u.RawQuery!="" || u.Fragment!="" { continue }
		out[name]=u.String()
	}
	return out
}

func (s *Server) hostingDomains(w http.ResponseWriter, r *http.Request) {
	if !s.internalOnly(w,r) { return }
	switch r.Method {
	case http.MethodGet:
		ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
		rows,err:=s.db.Query(ctx,"select id::text,slug,hostname,target_service,status,dns_record,created_at,updated_at from testagram_hosting.domains order by created_at desc limit 1000")
		if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"domain inventory unavailable"}); return }
		defer rows.Close()
		items:=make([]map[string]any,0)
		for rows.Next() {
			var id,slug,hostname,target,status string
			var dns []byte
			var created,updated time.Time
			if err:=rows.Scan(&id,&slug,&hostname,&target,&status,&dns,&created,&updated); err!=nil { reply(w,500,map[string]string{"error":"domain inventory decode failed"}); return }
			var record any; _=json.Unmarshal(dns,&record)
			items=append(items,map[string]any{"id":id,"slug":slug,"hostname":hostname,"targetService":target,"status":status,"dnsRecord":record,"createdAt":created,"updatedAt":updated})
		}
		if err:=rows.Err(); err!=nil { reply(w,503,map[string]string{"error":"domain inventory unavailable"}); return }
		reply(w,200,map[string]any{"domains":items,"count":len(items)})
	case http.MethodPost:
		var in DomainProvisionRequest
		if err:=decodeJSON(w,r,&in); err!=nil { reply(w,400,map[string]string{"error":"invalid json"}); return }
		in.Slug=strings.ToLower(strings.TrimSpace(in.Slug))
		in.TargetService=strings.TrimSpace(in.TargetService)
		if !hostingSlugPattern.MatchString(in.Slug) || isReservedDomainSlug(in.Slug) {
			reply(w,400,map[string]string{"error":"slug must be a valid non-reserved DNS label"}); return
		}
		base,err:=normalizedBaseDomain()
		if err!=nil { reply(w,503,map[string]string{"error":err.Error()}); return }
		target,ok:=domainTargets()[in.TargetService]
		if !ok { reply(w,400,map[string]string{"error":"targetService is not in TESTAGRAM_DOMAIN_TARGETS allowlist"}); return }
		host:=in.Slug+"."+base
		dns:=map[string]any{"recordType":"CNAME","name":host,"target":strings.TrimSpace(os.Getenv("TESTAGRAM_PUBLIC_DNS_TARGET")),"requiresOperatorApply":true}
		if dns["target"]=="" { delete(dns,"target") }
		dnsJSON,_:=json.Marshal(dns)
		ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
		var id string; var created time.Time
		err=s.db.QueryRow(ctx,"insert into testagram_hosting.domains(slug,hostname,target_service,status,dns_record) "+
			"values($1,$2,$3,'pending_dns',$4) returning id::text,created_at",in.Slug,host,in.TargetService,dnsJSON).Scan(&id,&created)
		if err!=nil {
			var pgErr *pgconn.PgError
			if errors.As(err,&pgErr) && pgErr.Code=="23505" { reply(w,409,map[string]string{"error":"slug or hostname already reserved"}); return }
			reply(w,500,map[string]string{"error":"domain reservation failed"}); return
		}
		reply(w,http.StatusCreated,map[string]any{"id":id,"slug":in.Slug,"hostname":host,"targetService":in.TargetService,
			"targetOrigin":target,"status":"pending_dns","dnsRecord":dns,
			"nextStep":"Apply the DNS record and confirm the allowlisted target is reachable, then activate; Caddy will request HTTPS automatically on first access.",
			"createdAt":created})
	default: methodNotAllowed(w)
	}
}

func (s *Server) hostingDomainActions(w http.ResponseWriter, r *http.Request) {
	if !s.internalOnly(w, r) { return }
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/hosting/domains/"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" { reply(w, 404, map[string]string{"error":"expected /v1/hosting/domains/{slug}/activate or /disable"}); return }
	slug, action := strings.ToLower(parts[0]), parts[1]
	if r.Method != http.MethodPost { methodNotAllowed(w); return }
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	switch action {
	case "activate":
		base, err := normalizedBaseDomain()
		if err != nil { reply(w, 503, map[string]string{"error":err.Error()}); return }
		host := slug + "." + base
		expected := strings.TrimSpace(os.Getenv("TESTAGRAM_PUBLIC_ORIGIN_IP"))
		if net.ParseIP(expected) == nil { reply(w, 503, map[string]string{"error":"TESTAGRAM_PUBLIC_ORIGIN_IP must be configured before DNS activation"}); return }
		resolverCtx, resolverCancel := context.WithTimeout(ctx, 3*time.Second)
		defer resolverCancel()
		addresses, err := net.DefaultResolver.LookupIPAddr(resolverCtx, host)
		matched := false
		if err == nil {
			for _, address := range addresses { if address.IP.Equal(net.ParseIP(expected)) { matched = true; break } }
		}
		if !matched { reply(w, 409, map[string]string{"error":"DNS does not currently resolve to the configured Testagram origin IP","hostname":host}); return }
		var targetService string
		err = s.db.QueryRow(ctx, "select target_service from testagram_hosting.domains where slug=$1 and hostname=$2 and status='pending_dns'", slug, host).Scan(&targetService)
		if err != nil { reply(w, 404, map[string]string{"error":"pending domain reservation not found"}); return }
		if _, ok := domainTargets()[targetService]; !ok { reply(w, 409, map[string]string{"error":"domain target is no longer allowlisted"}); return }
		_, err = s.db.Exec(ctx, "update testagram_hosting.domains set status='active',updated_at=now() where slug=$1 and hostname=$2 and status='pending_dns'", slug, host)
		if err != nil { reply(w, 500, map[string]string{"error":"domain activation failed"}); return }
		reply(w, 200, map[string]any{"slug":slug,"hostname":host,"status":"active","dnsVerified":true,"https":"Caddy on-demand TLS will request a certificate when the hostname is first accessed"})
	case "disable":
		tag, err := s.db.Exec(ctx, "update testagram_hosting.domains set status='disabled',updated_at=now() where slug=$1 and status <> 'disabled'", slug)
		if err != nil { reply(w, 500, map[string]string{"error":"domain disable failed"}); return }
		if tag.RowsAffected() != 1 { reply(w, 404, map[string]string{"error":"domain reservation not found"}); return }
		reply(w, 200, map[string]any{"slug":slug,"status":"disabled"})
	default:
		reply(w, 404, map[string]string{"error":"unsupported domain action"})
	}
}

// hostingTLSAuthorize is an internal Caddy on-demand TLS ask endpoint. Caddy
// may request a certificate only for a hostname that has been DNS-verified and activated.
func (s *Server) hostingTLSAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { methodNotAllowed(w); return }
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("domain")), "."))
	base, err := normalizedBaseDomain()
	if err != nil || !strings.HasSuffix(host, "."+base) { http.Error(w, "denied", http.StatusForbidden); return }
	label := strings.TrimSuffix(host, "."+base)
	if !hostingSlugPattern.MatchString(label) || isReservedDomainSlug(label) { http.Error(w, "denied", http.StatusForbidden); return }
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var targetService string
	err = s.db.QueryRow(ctx, "select target_service from testagram_hosting.domains where hostname=$1 and status='active'", host).Scan(&targetService)
	if err != nil { http.Error(w, "denied", http.StatusForbidden); return }
	if _, ok := domainTargets()[targetService]; !ok { http.Error(w, "denied", http.StatusForbidden); return }
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("allowed"))
}

func normalizeRequestHost(value string) string {
	host := strings.TrimSpace(value)
	if parsed, _, err := net.SplitHostPort(host); err == nil { host = parsed }
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// withManagedDomainRouting sends activated tenant subdomains to their explicitly
// allowlisted private service origin. Unknown, pending and disabled subdomains fail closed.
func (s *Server) withManagedDomainRouting(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base, err := normalizedBaseDomain()
		host := normalizeRequestHost(r.Host)
		if err == nil && host == base {
			http.NotFound(w, r)
			return
		}
		if err == nil && host != "" && strings.HasSuffix(host, "."+base) {
			label := strings.TrimSuffix(host, "."+base)
			// Only one-label, non-reserved hostnames may enter tenant routing.
			// Never fall through to operator APIs for an unknown tenant hostname.
			if strings.Contains(label, ".") || isReservedDomainSlug(label) {
				http.NotFound(w, r)
				return
			}
			s.proxyManagedDomain(w, r, host)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) proxyManagedDomain(w http.ResponseWriter, r *http.Request, host string) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var targetService string
	err := s.db.QueryRow(ctx, "select target_service from testagram_hosting.domains where hostname=$1 and status='active'", host).Scan(&targetService)
	if err != nil { http.NotFound(w, r); return }
	target, ok := domainTargets()[targetService]
	if !ok { http.Error(w, "domain target unavailable", http.StatusBadGateway); return }
	targetURL, err := url.Parse(target)
	if err != nil { http.Error(w, "domain target invalid", http.StatusBadGateway); return }
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "managed application temporarily unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func isReservedDomainSlug(slug string) bool {
	switch slug {
	case "www","api","auth","admin","mail","smtp","imap","pop","ns1","ns2","status","internal","localhost","test","staging","prod","production","dev","assets","cdn","media","static","edge":
		return true
	}
	return strings.HasPrefix(slug,"xn--")
}

func (s *Server) hostingDNSRecords(w http.ResponseWriter,r *http.Request) {
	if r.Method!=http.MethodGet { methodNotAllowed(w); return }
	if !s.internalOnly(w,r) { return }
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
	rows,err:=s.db.Query(ctx,"select hostname,dns_record from testagram_hosting.domains where status <> 'disabled' order by hostname limit 5000")
	if err!=nil { reply(w,503,map[string]string{"error":"DNS inventory unavailable"}); return }
	defer rows.Close()
	records:=make([]map[string]any,0)
	for rows.Next() {
		var hostname string; var raw []byte
		if err:=rows.Scan(&hostname,&raw); err!=nil { reply(w,500,map[string]string{"error":"DNS inventory decode failed"}); return }
		var record map[string]any
		if json.Unmarshal(raw,&record)==nil { records=append(records,map[string]any{"hostname":hostname,"record":record}) }
	}
	if err:=rows.Err(); err!=nil { reply(w,503,map[string]string{"error":"DNS inventory unavailable"}); return }
	reply(w,200,map[string]any{"records":records,"count":len(records),"note":"Desired-state export only. This API does not silently modify the registrar or authoritative DNS server."})
}

func (s *Server) hostingDeployments(w http.ResponseWriter,r *http.Request) {
	if !s.internalOnly(w,r) { return }
	switch r.Method {
	case http.MethodGet:
		ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
		rows,err:=s.db.Query(ctx,"select id::text,app_name,image,image_digest,node_id::text,workload_id::text,cpu_millis,memory_bytes,storage_bytes,status,created_at,updated_at from testagram_hosting.deployments order by created_at desc limit 500")
		if err!=nil { reply(w,503,map[string]string{"error":"deployment inventory unavailable"}); return }
		defer rows.Close()
		items:=make([]map[string]any,0)
		for rows.Next() {
			var id,app,image,digest,status string; var node,workload *string
			var cpu int; var memory,storage int64; var created,updated time.Time
			if err:=rows.Scan(&id,&app,&image,&digest,&node,&workload,&cpu,&memory,&storage,&status,&created,&updated); err!=nil { reply(w,500,map[string]string{"error":"deployment inventory decode failed"}); return }
			items=append(items,map[string]any{"id":id,"appName":app,"image":image,"imageDigest":digest,"nodeId":node,"workloadId":workload,"cpuMillis":cpu,"memoryBytes":memory,"storageBytes":storage,"status":status,"createdAt":created,"updatedAt":updated})
		}
		if err:=rows.Err(); err!=nil { reply(w,503,map[string]string{"error":"deployment inventory unavailable"}); return }
		reply(w,200,map[string]any{"deployments":items,"count":len(items)})
	case http.MethodPost:
		var in DeploymentRequest
		if err:=decodeJSON(w,r,&in); err!=nil { reply(w,400,map[string]string{"error":"invalid json"}); return }
		in.AppName=strings.ToLower(strings.TrimSpace(in.AppName))
		in.Image=strings.TrimSpace(in.Image)
		in.Arch=strings.TrimSpace(in.Arch)
		if !hostingSlugPattern.MatchString(in.AppName) || in.Image=="" || len(in.Image)>255 || strings.ContainsAny(in.Image," \t\r\n") || !validImageDigest(in.ImageDigest) {
			reply(w,400,map[string]string{"error":"appName, image, and immutable lowercase sha256 imageDigest are required"}); return
		}
		if in.CPUMillis==0 { in.CPUMillis=250 }
		if in.MemoryBytes==0 { in.MemoryBytes=268435456 }
		if in.StorageBytes==0 { in.StorageBytes=268435456 }
		if in.CPUMillis<50 || in.CPUMillis>64000 || in.MemoryBytes<67108864 || in.MemoryBytes>68719476736 || in.StorageBytes<67108864 || in.StorageBytes>1099511627776 {
			reply(w,400,map[string]string{"error":"resource request is outside supported bounds (50-64000 CPU millis, 64MiB-64GiB RAM, 64MiB-1TiB storage)"}); return
		}
		if len(in.Command)>16 { reply(w,400,map[string]string{"error":"command exceeds 16 arguments"}); return }
		for _,arg:=range in.Command { if len(arg)>256 || strings.ContainsRune(arg,0) { reply(w,400,map[string]string{"error":"command argument invalid or too long"}); return } }
		if len(in.PublicEnv)>64 || len(in.SecretRefs)>64 { reply(w,400,map[string]string{"error":"too many environment variables or secret references"}); return }
		for key,value:=range in.PublicEnv {
			upper:=strings.ToUpper(key)
			if !envKeyPattern.MatchString(key) || strings.Contains(upper,"SECRET") || strings.Contains(upper,"TOKEN") || strings.Contains(upper,"PASSWORD") || strings.Contains(upper,"PRIVATE_KEY") || strings.Contains(upper,"API_KEY") || len(value)>2048 {
				reply(w,400,map[string]string{"error":"publicEnv contains an invalid key or secret-like field; use secret references after a secret resolver is configured"}); return
			}
		}
		if len(in.SecretRefs)>0 {
			for key,ref:=range in.SecretRefs { if !envKeyPattern.MatchString(key) || !secretRefPattern.MatchString(ref) { reply(w,400,map[string]string{"error":"secretRefs must map valid environment keys to secret:// references"}); return } }
			reply(w,http.StatusNotImplemented,map[string]string{"error":"secret reference resolution is not enabled; refusing to dispatch unresolved secret references"}); return
		}
		if len(in.PublicEnv)>0 { raw,_:=json.Marshal(in.PublicEnv); if len(raw)>8192 { reply(w,400,map[string]string{"error":"publicEnv payload too large"}); return } }
		if in.Volume!="" && (!hostingSlugPattern.MatchString(in.Volume) || strings.Contains(in.Volume,"..")) { reply(w,400,map[string]string{"error":"volume must be a named platform volume, not a host path"}); return }
		cmdJSON,_:=json.Marshal(in.Command); envJSON,_:=json.Marshal(in.PublicEnv)
		ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second); defer cancel()
		tx,err:=s.db.Begin(ctx); if err!=nil { reply(w,503,map[string]string{"error":"deployment transaction unavailable"}); return }
		defer func(){ _=tx.Rollback(context.Background()) }()
		var nodeID,endpoint string
		err=tx.QueryRow(ctx,"select n.id::text,n.endpoint from public.vps_nodes n "+
			"left join lateral (select coalesce(sum(cpu_millis),0)::bigint cpu_reserved,coalesce(sum(memory_bytes),0)::bigint memory_reserved,coalesce(sum(storage_bytes),0)::bigint storage_reserved "+
			"from public.vps_workloads where node_id=n.id and status in ('assigned','running')) r on true "+
			"where n.status='online' and n.last_seen_at>now()-interval '45 seconds' "+
			"and n.cpu_cores*1000-r.cpu_reserved >= $1 and n.memory_bytes-r.memory_reserved >= $2 and n.free_bytes-r.storage_reserved >= $3 "+
			"and ($4='' or n.arch=$4) order by (n.free_bytes-r.storage_reserved) desc limit 1 for update of n skip locked",
			in.CPUMillis,in.MemoryBytes,in.StorageBytes,in.Arch).Scan(&nodeID,&endpoint)
		if err==pgx.ErrNoRows { reply(w,503,map[string]string{"error":"no healthy node has sufficient unreserved CPU, RAM and storage"}); return }
		if err!=nil { reply(w,503,map[string]string{"error":"resource-aware scheduler failed"}); return }
		var workloadID string
		err=tx.QueryRow(ctx,"insert into public.vps_workloads(node_id,image,image_digest,command,env,cpu_millis,memory_bytes,storage_bytes,volume,status) "+
			"values($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,$9,'assigned') returning id::text",
			nodeID,in.Image,in.ImageDigest,cmdJSON,envJSON,in.CPUMillis,in.MemoryBytes,in.StorageBytes,nullIfEmpty(in.Volume)).Scan(&workloadID)
		if err!=nil { reply(w,500,map[string]string{"error":"workload could not be persisted"}); return }
		var deploymentID string
		err=tx.QueryRow(ctx,"insert into testagram_hosting.deployments(app_name,image,image_digest,node_id,workload_id,cpu_millis,memory_bytes,storage_bytes,public_env,secret_refs,status) "+
			"values($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,'{}'::jsonb,'assigned') returning id::text",
			in.AppName,in.Image,in.ImageDigest,nodeID,workloadID,in.CPUMillis,in.MemoryBytes,in.StorageBytes,envJSON).Scan(&deploymentID)
		if err!=nil { reply(w,500,map[string]string{"error":"deployment record could not be persisted"}); return }
		if err=tx.Commit(ctx); err!=nil { reply(w,500,map[string]string{"error":"deployment transaction commit failed"}); return }
		reply(w,http.StatusCreated,map[string]any{"id":deploymentID,"appName":in.AppName,"workloadId":workloadID,"nodeId":nodeID,"nodeEndpoint":endpoint,"status":"assigned","imageDigest":in.ImageDigest,"cpuMillis":in.CPUMillis,"memoryBytes":in.MemoryBytes,"storageBytes":in.StorageBytes,"note":"Runtime execution is asynchronous; poll deployment status. No public DNS or route is changed by this API."})
	default: methodNotAllowed(w)
	}
}

func nullIfEmpty(s string) any { if strings.TrimSpace(s)=="" { return nil }; return s }

func (s *Server) hostingDeploymentDetail(w http.ResponseWriter,r *http.Request) {
	if !s.internalOnly(w,r) { return }
	if r.Method!=http.MethodGet { methodNotAllowed(w); return }
	id:=strings.Trim(strings.TrimPrefix(r.URL.Path,"/v1/hosting/deployments/"),"/")
	if id=="" || strings.Contains(id,"/") { reply(w,400,map[string]string{"error":"deployment id required"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second); defer cancel()
	var app,image,digest,status string; var node,workload *string; var cpu int; var memory,storage int64; var created,updated time.Time
	err:=s.db.QueryRow(ctx,"select app_name,image,image_digest,node_id::text,workload_id::text,cpu_millis,memory_bytes,storage_bytes,status,created_at,updated_at from testagram_hosting.deployments where id=$1::uuid",id).Scan(&app,&image,&digest,&node,&workload,&cpu,&memory,&storage,&status,&created,&updated)
	if err!=nil { reply(w,404,map[string]string{"error":"deployment not found"}); return }
	reply(w,200,map[string]any{"id":id,"appName":app,"image":image,"imageDigest":digest,"nodeId":node,"workloadId":workload,"cpuMillis":cpu,"memoryBytes":memory,"storageBytes":storage,"status":status,"createdAt":created,"updatedAt":updated})
}

func (s *Server) hostingBackups(w http.ResponseWriter,r *http.Request) {
	if !s.internalOnly(w,r) { return }
	switch r.Method {
	case http.MethodGet:
		ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
		rows,err:=s.db.Query(ctx,"select id::text,backup_kind,storage_uri,sha256,size_bytes,source_host,status,verified_at,created_at from testagram_hosting.backup_manifests order by created_at desc limit 500")
		if err!=nil { reply(w,503,map[string]string{"error":"backup inventory unavailable"}); return }
		defer rows.Close()
		items:=make([]map[string]any,0)
		for rows.Next() {
			var id,kind,uri,hash,source,status string; var size int64; var verified *time.Time; var created time.Time
			if err:=rows.Scan(&id,&kind,&uri,&hash,&size,&source,&status,&verified,&created); err!=nil { reply(w,500,map[string]string{"error":"backup inventory decode failed"}); return }
			items=append(items,map[string]any{"id":id,"kind":kind,"storageUri":uri,"sha256":hash,"sizeBytes":size,"sourceHost":source,"status":status,"verifiedAt":verified,"createdAt":created})
		}
		if err:=rows.Err(); err!=nil { reply(w,503,map[string]string{"error":"backup inventory unavailable"}); return }
		reply(w,200,map[string]any{"backups":items,"count":len(items)})
	case http.MethodPost:
		var in BackupManifestRequest
		if err:=decodeJSON(w,r,&in); err!=nil { reply(w,400,map[string]string{"error":"invalid json"}); return }
		in.Kind=strings.TrimSpace(in.Kind); in.StorageURI=strings.TrimSpace(in.StorageURI); in.SourceHost=strings.TrimSpace(in.SourceHost)
		if in.Kind!="postgres" && in.Kind!="objects" && in.Kind!="config" && in.Kind!="full" { reply(w,400,map[string]string{"error":"unsupported backup kind"}); return }
		u,err:=url.Parse(in.StorageURI)
		if err!=nil || (u.Scheme!="s3" && u.Scheme!="file" && u.Scheme!="https") || u.Host=="" || u.User!=nil || u.RawQuery!="" || u.Fragment!="" { reply(w,400,map[string]string{"error":"storageUri must be an s3://, file://, or https:// object URI without embedded credentials or query strings"}); return }
		if !sha256Pattern.MatchString(in.SHA256) || in.SizeBytes<0 || in.SourceHost=="" || len(in.SourceHost)>128 { reply(w,400,map[string]string{"error":"valid SHA-256, nonnegative sizeBytes and sourceHost are required"}); return }
		ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
		var id string; var created time.Time
		err=s.db.QueryRow(ctx,"insert into testagram_hosting.backup_manifests(backup_kind,storage_uri,sha256,size_bytes,source_host) values($1,$2,$3,$4,$5) returning id::text,created_at",in.Kind,in.StorageURI,in.SHA256,in.SizeBytes,in.SourceHost).Scan(&id,&created)
		if err!=nil { reply(w,500,map[string]string{"error":"backup manifest registration failed"}); return }
		reply(w,201,map[string]any{"id":id,"status":"registered","createdAt":created,"nextStep":"A trusted backup worker must read the stored object and submit its observed hash and byte count to the verification endpoint."})
	default: methodNotAllowed(w)
	}
}

func (s *Server) hostingBackupActions(w http.ResponseWriter,r *http.Request) {
	if !s.internalOnly(w,r) { return }
	if r.Method!=http.MethodPost { methodNotAllowed(w); return }
	path:=strings.Trim(strings.TrimPrefix(r.URL.Path,"/v1/hosting/backups/"),"/")
	parts:=strings.Split(path,"/")
	if len(parts)!=2 || parts[1]!="verify" || parts[0]=="" { reply(w,404,map[string]string{"error":"expected /v1/hosting/backups/{id}/verify"}); return }
	var in BackupVerifyRequest
	if err:=decodeJSON(w,r,&in); err!=nil { reply(w,400,map[string]string{"error":"invalid json"}); return }
	if !sha256Pattern.MatchString(in.ObservedSHA256) || in.ObservedBytes<0 { reply(w,400,map[string]string{"error":"valid observed hash and byte count are required"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
	var expectedHash string; var expectedBytes int64
	err:=s.db.QueryRow(ctx,"select sha256,size_bytes from testagram_hosting.backup_manifests where id=$1::uuid",parts[0]).Scan(&expectedHash,&expectedBytes)
	if err!=nil { reply(w,404,map[string]string{"error":"backup manifest not found"}); return }
	verified:=in.ObservedSHA256==expectedHash && in.ObservedBytes==expectedBytes
	status:="failed"; if verified { status="verified" }
	_,err=s.db.Exec(ctx,"update testagram_hosting.backup_manifests set status=$2,verified_at=case when $2='verified' then now() else null end where id=$1::uuid",parts[0],status)
	if err!=nil { reply(w,500,map[string]string{"error":"backup verification result could not be stored"}); return }
	code:=http.StatusOK; if !verified { code=http.StatusUnprocessableEntity }
	reply(w,code,map[string]any{"id":parts[0],"status":status,"hashMatched":in.ObservedSHA256==expectedHash,"sizeMatched":in.ObservedBytes==expectedBytes})
}

func (s *Server) hostingRecoveryDrills(w http.ResponseWriter,r *http.Request) {
	if !s.internalOnly(w,r) { return }
	switch r.Method {
	case http.MethodGet:
		ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
		rows,err:=s.db.Query(ctx,"select d.id::text,d.backup_id::text,d.target_id,d.restored_sha256,d.success,d.notes,d.started_at,d.completed_at from testagram_hosting.recovery_drills d order by d.completed_at desc limit 500")
		if err!=nil { reply(w,503,map[string]string{"error":"recovery drill inventory unavailable"}); return }
		defer rows.Close()
		items:=make([]map[string]any,0)
		for rows.Next() {
			var id,backup,target,hash,notes string; var success bool; var started,completed time.Time
			if err:=rows.Scan(&id,&backup,&target,&hash,&success,&notes,&started,&completed); err!=nil { reply(w,500,map[string]string{"error":"recovery drill decode failed"}); return }
			items=append(items,map[string]any{"id":id,"backupId":backup,"targetId":target,"restoredSha256":hash,"success":success,"notes":notes,"startedAt":started,"completedAt":completed})
		}
		if err:=rows.Err(); err!=nil { reply(w,503,map[string]string{"error":"recovery drill inventory unavailable"}); return }
		reply(w,200,map[string]any{"drills":items,"count":len(items)})
	case http.MethodPost:
		var in RecoveryDrillRequest
		if err:=decodeJSON(w,r,&in); err!=nil { reply(w,400,map[string]string{"error":"invalid json"}); return }
		in.TargetID=strings.TrimSpace(in.TargetID); in.Notes=strings.TrimSpace(in.Notes)
		if in.BackupID=="" || in.TargetID=="" || len(in.TargetID)>160 || len(in.Notes)>2000 || !sha256Pattern.MatchString(in.RestoredSHA256) || in.StartedAt.IsZero() || in.CompletedAt.IsZero() || in.CompletedAt.Before(in.StartedAt) || in.CompletedAt.After(time.Now().Add(5*time.Minute)) {
			reply(w,400,map[string]string{"error":"backupId, targetId, restored SHA-256 and valid start/completion times are required"}); return
		}
		ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
		var expectedHash,status string
		err:=s.db.QueryRow(ctx,"select sha256,status from testagram_hosting.backup_manifests where id=$1::uuid",in.BackupID).Scan(&expectedHash,&status)
		if err!=nil { reply(w,404,map[string]string{"error":"backup manifest not found"}); return }
		if status!="verified" { reply(w,409,map[string]string{"error":"recovery drills require a verified backup manifest"}); return }
		success:=in.Success && in.RestoredSHA256==expectedHash
		var id string
		err=s.db.QueryRow(ctx,"insert into testagram_hosting.recovery_drills(backup_id,target_id,restored_sha256,success,notes,started_at,completed_at) values($1::uuid,$2,$3,$4,$5,$6,$7) returning id::text",in.BackupID,in.TargetID,in.RestoredSHA256,success,in.Notes,in.StartedAt,in.CompletedAt).Scan(&id)
		if err!=nil { reply(w,500,map[string]string{"error":"recovery drill result could not be stored"}); return }
		code:=http.StatusCreated; if !success { code=http.StatusUnprocessableEntity }
		reply(w,code,map[string]any{"id":id,"success":success,"hashMatched":in.RestoredSHA256==expectedHash,"note":"This records a drill report; the reporting worker must perform an isolated restore before submitting it."})
	default: methodNotAllowed(w)
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}
