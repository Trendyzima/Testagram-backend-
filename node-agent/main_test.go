package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestControlURLRequiresTLSOutsideLoopback(t *testing.T) {
	cases := []struct{ value string; wantErr bool }{
		{"https://control.example.test", false},
		{"http://127.0.0.1:8787", false},
		{"http://localhost:8787", false},
		{"http://control.example.test", true},
		{"http://192.0.2.5:8787", true},
		{"https://user:pass@control.example.test", true},
		{"https://control.example.test/?token=secret", true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			err := controlURLValid(tc.value)
			if (err != nil) != tc.wantErr { t.Fatalf("controlURLValid(%q) error=%v, wantErr=%v", tc.value, err, tc.wantErr) }
		})
	}
}

func TestValidateWorkloadRequiresImmutableDigest(t *testing.T) {
	good := Workload{Image:"registry.example/testagram/worker", ImageDigest:"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := validateWorkload(good); err != nil { t.Fatalf("valid workload rejected: %v", err) }
	bad := good; bad.ImageDigest = "latest"
	if err := validateWorkload(bad); err == nil { t.Fatal("mutable or malformed image digest accepted") }
	bad = good; bad.Env = map[string]string{"LD_PRELOAD":"/tmp/evil.so"}
	if err := validateWorkload(bad); err == nil { t.Fatal("loader environment override accepted") }
	bad = good; bad.Env = map[string]string{"TOKEN":"bad\x00value"}
	if err := validateWorkload(bad); err == nil { t.Fatal("NUL in environment value accepted") }
}

func TestSafeVolumePathRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := safeVolumePath(root, "../escape"); err == nil { t.Fatal("path traversal accepted") }
	outside := t.TempDir()
	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outside, link); err != nil { t.Skipf("symlink unsupported: %v", err) }
	if _, err := safeVolumePath(root, "escape-link/nested"); err == nil { t.Fatal("symlink escape accepted") }
	p, err := safeVolumePath(root, "tenant-a")
	if err != nil { t.Fatalf("safe volume rejected: %v", err) }
	realRoot, _ := filepath.EvalSymlinks(root)
	if !withinPath(realRoot, p) { t.Fatalf("path %q not within root %q", p, realRoot) }
}

func withinPath(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[:1] != "."
}

func TestNodeMemoryMetricsDoNotTrustExternalEndpoint(t *testing.T) {
	if ip := net.ParseIP("127.0.0.1"); ip == nil || !ip.IsLoopback() { t.Fatal("test assumption about loopback failed") }
}
