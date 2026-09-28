package installer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resetRestartGate() {
	restartMu.Lock()
	restartBusy = false
	restartMu.Unlock()
}

func TestScheduleRestartMissing(t *testing.T) {
	resetRestartGate()
	err := scheduleRestart(filepath.Join(t.TempDir(), "missing"), 0, nil)
	if !errors.Is(err, ErrNotService) {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduleRestartRunsInitScript(t *testing.T) {
	resetRestartGate()
	dir := t.TempDir()
	script := filepath.Join(dir, "leosentry")
	marker := filepath.Join(dir, "marker")
	body := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> '" + marker + "'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := scheduleRestart(script, 20*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if err := scheduleRestart(script, 20*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(marker)
		if string(got) == "restart\n" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("marker = %q", got)
}
