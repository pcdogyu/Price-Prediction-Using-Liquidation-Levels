package upgrade

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type instantRunner struct{}

func (instantRunner) Run(ctx context.Context) (Result, error) {
	return Result{Steps: []Step{{Name: "go_test", Command: "go test ./...", Output: "ok"}}}, nil
}

func TestManagerPersistsStatus(t *testing.T) {
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	manager := NewManager(instantRunner{}, WithStatusFile(statusFile))
	if _, started, err := manager.Start(context.Background()); err != nil || !started {
		t.Fatalf("start started=%v err=%v", started, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if manager.Status().Status == "succeeded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if manager.Status().Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", manager.Status().Status)
	}
	reloaded := NewManager(nil, WithStatusFile(statusFile))
	if got := reloaded.Status(); got.Status != "succeeded" || len(got.Steps) != 1 {
		t.Fatalf("reloaded status = %#v", got)
	}
}
