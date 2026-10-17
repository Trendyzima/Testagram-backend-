package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxControlResponse = 64 << 10

type Agent struct {
	control, bootstrap, nodeToken, nodeID, name, endpoint, volumeRoot, stateFile string
	client *http.Client
	mu sync.RWMutex
}

type Registration struct {
	Name string `json:"name"`
	Platform string `json:"platform"`
	Arch string `json:"arch"`
	CPUCores int `json:"cpuCores"`
	MemoryBytes int64 `json:"memoryBytes"`
	StorageBytes int64 `json:"storageBytes"`
	FreeBytes int64 `json:"freeBytes"`
	Endpoint string `json:"endpoint"`
	Capabilities map[string]any `json:"capabilities"`
}

type Workload struct {
	Image string `json:"image"`
	ImageDigest string `json:"imageDigest"`
	Command []string `json:"command"`
	Env map[string]string `json:"env"`
	CPUMillis int `json:"cpuMillis"`
	MemoryBytes int64 `json:"memoryBytes"`
	StorageBytes int64 `json:"storageBytes"`
	Volume string `json:"volume"`
	Arch string `json:"arch"`
}

type pollResponse struct {
	ID string `json:"id"`
	Workload Workload `json:"workload"`
}

func equalSecret(a, b string) bool {
	if a == "" || b == "" { return false }
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func validImageDigest(v string) bool {
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") { return false }
	for _, c := range v[7:] { if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) { return false } }
	return true
}
func (a *Agent) token() string { a.mu.RLock(); defer a.mu.RUnlock(); return a.nodeToken }
func (a *Agent) authHeader() string {
	if token := a.token(); token != "" { return "Bearer " + token }
	return "Bearer " + a.bootstrap
}
func runtimeBin() string {
	for _, name := range []string{"docker", "podman"} { if path, err := exec.LookPath(name); err == nil { return path } }
	return ""
}
func (a *Agent) disk() (int64, int64) {
	if total, err := strconv.ParseInt(os.Getenv("VPS_STORAGE_BYTES"), 10, 64); err == nil && total > 0 {
		free, _ := strconv.ParseInt(os.Getenv("VPS_FREE_BYTES"), 10, 64)
		return total, free
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(a.volumeRoot, &stat); err != nil { return 0, 0 }
	return int64(stat.Blocks) * int64(stat.Bsize), int64(stat.Bavail) * int64(stat.Bsize)
}
func (a *Agent) memory() int64 {
	if value, err := strconv.ParseInt(os.Getenv("VPS_MEMORY_BYTES"), 10, 64); err == nil && value > 0 { return value }
	data, err := os.ReadFile("/proc/meminfo"); if err != nil { return 0 }
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" { kb, _ := strconv.ParseInt(fields[1], 10, 64); return kb * 1024 }
	}
	return 0
}
func (a *Agent) post(path string, body any, out any) (int, error) {
	raw, err := json.Marshal(body); if err != nil { return 0, err }
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(a.control, "/")+path, bytes.NewReader(raw)); if err != nil { return 0, err }
	req.Header.Set("Authorization", a.authHeader()); req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req); if err != nil { return 0, err }; defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNoContent {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("control plane returned HTTP %d", resp.StatusCode)
		_ = data
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxControlResponse)).Decode(out); err != nil { return resp.StatusCode, errors.New("invalid control-plane response") }
	}
	return resp.StatusCode, nil
}
func (a *Agent) loadState() error {
	data, err := os.ReadFile(a.stateFile)
	if os.IsNotExist(err) { return nil }; if err != nil { return err }
	info, err := os.Stat(a.stateFile); if err != nil { return err }
	if info.Mode().Perm()&0077 != 0 { return errors.New("node state file permissions must be 0600 or stricter") }
	var state struct { ID string `json:"id"`; Token string `json:"token"` }
	if err := json.Unmarshal(data, &state); err != nil { return err }
	if state.ID == "" || state.Token == "" { return errors.New("invalid node state") }
	a.mu.Lock(); a.nodeID, a.nodeToken = state.ID, state.Token; a.mu.Unlock()
	return nil
}
func (a *Agent) saveState(id, token string) error {
	if err := os.MkdirAll(filepath.Dir(a.stateFile), 0700); err != nil { return err }
	data, err := json.Marshal(struct { ID string `json:"id"`; Token string `json:"token"` }{id, token}); if err != nil { return err }
	tmp := a.stateFile + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600); if err != nil { return err }
	if _, err = f.Write(data); err != nil { _ = f.Close(); return err }
	if err = f.Sync(); err != nil { _ = f.Close(); return err }
	if err = f.Close(); err != nil { return err }
	if err = os.Rename(tmp, a.stateFile); err != nil { return err }
	return nil
}
func (a *Agent) register() error {
	total, free := a.disk()
	body := Registration{Name:a.name, Platform:runtime.GOOS, Arch:runtime.GOARCH, CPUCores:runtime.NumCPU(),
		MemoryBytes:a.memory(), StorageBytes:total, FreeBytes:free, Endpoint:a.endpoint,
		Capabilities:map[string]any{"containers":runtimeBin()!="", "localVolumes":true, "workloadTimeout":true}}
	var out struct { ID string `json:"id"`; Token string `json:"token"` }
	_, err := a.post("/v1/nodes/register", body, &out); if err != nil { return err }
	if out.ID == "" || out.Token == "" { return errors.New("control plane returned incomplete node credentials") }
	if err := a.saveState(out.ID, out.Token); err != nil { return err }
	a.mu.Lock(); a.nodeID, a.nodeToken = out.ID, out.Token; a.mu.Unlock()
	return nil
}
func (a *Agent) heartbeat() error {
	total, free := a.disk()
	body := Registration{CPUCores:runtime.NumCPU(), MemoryBytes:a.memory(), StorageBytes:total, FreeBytes:free,
		Capabilities:map[string]any{"containers":runtimeBin()!="", "workloadTimeout":true}}
	_, err := a.post("/v1/nodes/heartbeat", body, nil); return err
}
func (a *Agent) poll() (*pollResponse, int, error) {
	var out pollResponse
	code, err := a.post("/v1/nodes/poll", map[string]any{}, &out)
	if code == http.StatusNoContent { return nil, code, nil }
	if err != nil { return nil, code, err }
	if out.ID == "" || out.Workload.Image == "" { return nil, code, errors.New("control plane returned incomplete workload") }
	return &out, code, nil
}
func (a *Agent) report(id string, success bool) error {
	_, err := a.post("/v1/workloads/"+id+"/result", map[string]bool{"success":success}, nil); return err
}
func workloadTimeout() time.Duration {
	const defaultTimeout = 30 * time.Minute
	const maximumTimeout = 2 * time.Hour
	raw := strings.TrimSpace(os.Getenv("VPS_WORKLOAD_TIMEOUT"))
	if raw == "" { return defaultTimeout }
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Second || d > maximumTimeout { log.Printf("invalid VPS_WORKLOAD_TIMEOUT; using %s", defaultTimeout); return defaultTimeout }
	return d
}
func safeVolumePath(root, volume string) (string, error) {
	if volume == "" { return "", nil }
	if filepath.IsAbs(volume) || volume == "." || volume == ".." || strings.ContainsAny(volume, "\x00") {
		return "", errors.New("invalid workload volume")
	}
	rootAbs, err := filepath.Abs(root); if err != nil { return "", err }
	candidate := filepath.Join(rootAbs, filepath.Clean(volume))
	if candidate == rootAbs || !strings.HasPrefix(candidate, rootAbs+string(os.PathSeparator)) { return "", errors.New("volume escapes storage root") }
	if err := os.MkdirAll(rootAbs, 0700); err != nil { return "", err }
	realRoot, err := filepath.EvalSymlinks(rootAbs); if err != nil { return "", err }
	if err := os.MkdirAll(candidate, 0700); err != nil { return "", err }
	realCandidate, err := filepath.EvalSymlinks(candidate); if err != nil { return "", err }
	if realCandidate != realRoot && !strings.HasPrefix(realCandidate, realRoot+string(os.PathSeparator)) { return "", errors.New("volume symlink escapes storage root") }
	return realCandidate, nil
}
func validateWorkload(w Workload) error {
	if w.Image == "" || strings.ContainsAny(w.Image, " \t\r\n\x00") || strings.Contains(w.Image, "@") || !validImageDigest(w.ImageDigest) { return errors.New("immutable image and lowercase sha256 digest required") }
	if len(w.Command) > 128 { return errors.New("too many command arguments") }
	for _, arg := range w.Command { if strings.ContainsRune(arg, 0) { return errors.New("invalid command argument") } }
	if len(w.Env) > 128 { return errors.New("too many environment variables") }
	total := 0
	for key, value := range w.Env {
		if key == "" || strings.ContainsAny(key, "=\x00\r\n") || strings.HasPrefix(key, "LD_") || key == "PATH" { return errors.New("invalid or restricted environment key") }
		total += len(key)+len(value)
		if strings.ContainsRune(value, 0) { return errors.New("invalid environment value") }
	}
	if total > 32<<10 { return errors.New("environment exceeds 32 KiB") }
	if w.CPUMillis < 0 || w.CPUMillis > 64000 || w.MemoryBytes < 0 || w.MemoryBytes > 68719476736 || w.StorageBytes < 0 || w.StorageBytes > 1099511627776 { return errors.New("resource request outside supported bounds") }
	return nil
}
func (a *Agent) execute(w Workload) error {
	if err := validateWorkload(w); err != nil { return err }
	if w.StorageBytes > 0 { _, free := a.disk(); if free < w.StorageBytes { return errors.New("insufficient free disk space") } }
	rt := runtimeBin(); if rt == "" { return errors.New("docker or podman is required on this worker") }
	image := w.Image+"@"+w.ImageDigest
	args := []string{"run","--rm","--init","--read-only","--cap-drop=ALL","--security-opt=no-new-privileges",
		"--pids-limit=256","--network=bridge","--stop-timeout=10","--tmpfs","/tmp:rw,noexec,nosuid,size=64m"}
	if w.CPUMillis > 0 { args=append(args,"--cpus",strconv.FormatFloat(float64(w.CPUMillis)/1000,'f',3,64)) }
	if w.MemoryBytes > 0 { args=append(args,"--memory",strconv.FormatInt(w.MemoryBytes,10),"--memory-swap",strconv.FormatInt(w.MemoryBytes,10)) }
	for key,value := range w.Env { args=append(args,"--env",key+"="+value) }
	if w.Volume != "" {
		path, err := safeVolumePath(a.volumeRoot,w.Volume); if err != nil { return err }
		args=append(args,"--volume",path+":/data:rw")
	}
	args=append(args,image); args=append(args,w.Command...)
	ctx,cancel:=context.WithTimeout(context.Background(),workloadTimeout()); defer cancel()
	cmd:=exec.CommandContext(ctx,rt,args...)
	cmd.Stdout,cmd.Stderr=os.Stdout,os.Stderr
	err:=cmd.Run()
	if ctx.Err()==context.DeadlineExceeded { return errors.New("workload exceeded configured execution timeout") }
	return err
}
func controlURLValid(raw string) error {
	if raw == "" { return errors.New("VPS_CONTROL_PLANE_URL is required") }
	u,err:=net/url.Parse(raw); if err!=nil || u.Host=="" { return errors.New("invalid control-plane URL") }
	if u.Scheme!="https" {
		host:=u.Hostname()
		if u.Scheme!="http" || (host!="localhost" && net.ParseIP(host)==nil) { return errors.New("control-plane URL must use HTTPS except for loopback development") }
		if ip:=net.ParseIP(host); ip!=nil && !ip.IsLoopback() { return errors.New("HTTP control-plane URL must use loopback") }
	}
	if u.User!=nil || u.RawQuery!="" || u.Fragment!="" { return errors.New("control-plane URL must not contain credentials, query, or fragment") }
	return nil
}
func main() {
	control:=strings.TrimSpace(os.Getenv("VPS_CONTROL_PLANE_URL"))
	bootstrap:=os.Getenv("VPS_NODE_BOOTSTRAP_TOKEN")
	if err:=controlURLValid(control); err!=nil { log.Fatal(err) }
	if bootstrap=="" { log.Fatal("VPS_NODE_BOOTSTRAP_TOKEN is required") }
	a:=&Agent{control:control,bootstrap:bootstrap,name:os.Getenv("VPS_NODE_NAME"),endpoint:os.Getenv("VPS_NODE_ENDPOINT"),
		volumeRoot:os.Getenv("VPS_VOLUME_ROOT"),stateFile:os.Getenv("VPS_NODE_STATE_FILE"),
		client:&http.Client{Timeout:12*time.Second}}
	if a.name=="" { a.name="testagram-node" }; if a.endpoint=="" { a.endpoint="outbound-only" }
	if a.volumeRoot=="" { a.volumeRoot="/var/lib/testagram/storage/nodes" }
	if a.stateFile=="" { a.stateFile="/var/lib/testagram/node-agent/node-state.json" }
	if err:=os.MkdirAll(a.volumeRoot,0700); err!=nil { log.Fatal(err) }
	if err:=a.loadState(); err!=nil { log.Fatalf("refusing to start with invalid node state: %v",err) }
	backoff:=2*time.Second
	for {
		if a.token()=="" {
			if err:=a.register(); err!=nil { log.Printf("registration failed: %v",err); time.Sleep(backoff); if backoff<30*time.Second { backoff*=2 }; continue }
		}
		if err:=a.heartbeat(); err!=nil {
			log.Printf("heartbeat failed: %v",err)
			// Do not discard a valid persisted node credential on transient network/database errors.
			// Only a confirmed 401 during poll/result should trigger explicit re-enrollment.
			time.Sleep(backoff); if backoff<30*time.Second { backoff*=2 }; continue
		}
		work,code,err:=a.poll()
		if code==http.StatusUnauthorized {
			a.mu.Lock(); a.nodeToken=""; a.mu.Unlock()
			log.Printf("node credential rejected; registration will be retried")
		} else if err!=nil { log.Printf("poll failed: %v",err) } else if work!=nil {
			runErr:=a.execute(work.Workload)
			if reportErr:=a.report(work.ID,runErr==nil); reportErr!=nil { log.Printf("result report failed: %v",reportErr) }
			if runErr!=nil { log.Printf("workload %s failed: %v",work.ID,runErr) }
		}
		backoff=2*time.Second
		time.Sleep(5*time.Second)
	}
}
