package auditlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWriterPermissions(t *testing.T) {
	if os.Getenv("AUDIT_PERMISSION_CHILD") == "1" {
		exercisePermissions(t)
		return
	}
	if os.Geteuid() != 0 {
		exercisePermissions(t)
		return
	}
	// A test binary below /root cannot be traversed by the child identity.
	dir, err := os.MkdirTemp("", "audit-nonroot-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "audit.test")
	if err := os.WriteFile(binary, data, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestWriterPermissions$", "-test.v")
	cmd.Env = append(os.Environ(), "AUDIT_PERMISSION_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("non-root test: %v\n%s", err, out)
	}
}
func exercisePermissions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("must exercise permissions without root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	if w, err := NewWriter(dir); err == nil {
		w.Close()
		t.Fatal("unwritable directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "platform-gateway-"+time.Now().In(Shanghai()).Format("2006-01-02")+".jsonl")
	original := []byte("existing-record\n")
	if err := os.WriteFile(file, original, 0400); err != nil {
		t.Fatal(err)
	}
	if w, err := NewWriter(dir); err == nil {
		w.Close()
		t.Fatal("unwritable current log accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	data, err := os.ReadFile(file)
	if err != nil || string(data) != string(original) {
		t.Fatal("existing log changed", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("probe left behind", err)
	}
	if w, err := NewWriter(""); err != nil || w != nil {
		t.Fatal("disabled writer")
	}
}
