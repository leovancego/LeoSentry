package sysconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisableConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dnsmasq.conf")
	content := "# comment\n#log-facility=/var/log/x\nlog-facility=/dev/null\n  log-facility /tmp/y\nlog-facility-other=1\nlog-queries\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	original, changed, err := disableConflicts(path)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if string(original) != content {
		t.Fatal("original content not returned for rollback")
	}
	got, _ := os.ReadFile(path)
	lines := strings.Split(string(got), "\n")
	if !strings.HasPrefix(lines[2], "# log-facility=/dev/null") || !strings.HasPrefix(lines[3], "#   log-facility /tmp/y") {
		t.Errorf("conflicts not commented out:\n%s", got)
	}
	if lines[1] != "#log-facility=/var/log/x" || lines[4] != "log-facility-other=1" || lines[5] != "log-queries" {
		t.Errorf("unrelated lines modified:\n%s", got)
	}

	if _, changed, _ := disableConflicts(path); changed {
		t.Error("second run should be a no-op")
	}
	if _, changed, err := disableConflicts(filepath.Join(t.TempDir(), "missing")); changed || err != nil {
		t.Errorf("missing file: changed=%v err=%v", changed, err)
	}
}

func TestParseServicePIDs(t *testing.T) {
	running := []byte(`{"dnsmasq":{"instances":{"cfg01411c":{"running":true,"pid":11401,"command":["/usr/sbin/dnsmasq"]}}}}`)
	pids, err := parseServicePIDs(running, "dnsmasq")
	if err != nil || pids["cfg01411c"] != 11401 {
		t.Fatalf("pids=%v err=%v", pids, err)
	}

	crashed := []byte(`{"dnsmasq":{"instances":{"cfg01411c":{"running":false,"exit_code":1}}}}`)
	if _, err := parseServicePIDs(crashed, "dnsmasq"); err == nil || !strings.Contains(err.Error(), "exit code 1") {
		t.Fatalf("crashed instance: err=%v", err)
	}
	if _, err := parseServicePIDs([]byte(`{}`), "dnsmasq"); err == nil {
		t.Fatal("missing service should fail")
	}
}
