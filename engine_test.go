package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetEngine(t *testing.T) {
	t.Helper()
	origPatch := patchProxyURLFn
	origAPIKeyPatch := patchAPIKeyTargetsFn
	t.Cleanup(func() {
		patchProxyURLFn = origPatch
		patchAPIKeyTargetsFn = origAPIKeyPatch
	})
	engine.mu.Lock()
	engine.running = false
	engine.stopped = false
	engine.total = 0
	engine.done = 0
	engine.ok = 0
	engine.failures = nil
	engine.stopCh = nil
	engine.mu.Unlock()
}

func TestStartRejectsEmptyTargets(t *testing.T) {
	resetEngine(t)
	if err := engine.Start("set", nil, 4, nil); !errors.Is(err, ErrEmptyTargets) {
		t.Fatalf("Start(nil) err = %v, want ErrEmptyTargets", err)
	}
}

func TestStartRejectsWhenBusy(t *testing.T) {
	resetEngine(t)
	release := make(chan struct{})
	patchProxyURLFn = func(ctx context.Context, file, value string, headers http.Header) error {
		<-release
		return nil
	}
	if err := engine.Start("set", []EngineTarget{{FileName: "a.json", Value: "http://gw:2260"}}, 1, nil); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := engine.Start("set", []EngineTarget{{FileName: "b.json", Value: "http://gw:2260"}}, 1, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start err = %v, want ErrBusy", err)
	}
	close(release)
	waitEngineIdle(t, 2*time.Second)
}

func TestBatchRecordsOKAndFailures(t *testing.T) {
	resetEngine(t)
	patchProxyURLFn = func(ctx context.Context, file, value string, headers http.Header) error {
		if value == "" {
			return errors.New("409 账号已被删除")
		}
		return nil
	}
	targets := []EngineTarget{
		{FileName: "a.json", Value: "http://a:2260", Label: "a"},
		{FileName: "b.json", Value: "", Label: "b"},
		{FileName: "c.json", Value: "http://c:2260", Label: "c"},
	}
	if err := engine.Start("set", targets, 3, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitEngineIdle(t, 2*time.Second)

	snap := engine.Snapshot()
	if snap.Running || !snap.Finished {
		t.Fatalf("snapshot should be finished: %+v", snap)
	}
	if snap.Total != 3 || snap.OK != 2 || snap.Failed != 1 {
		t.Fatalf("counts = total %d ok %d failed %d", snap.Total, snap.OK, snap.Failed)
	}
	if len(snap.Failures) != 1 || snap.Failures[0].Name != "b" {
		t.Fatalf("failures = %+v", snap.Failures)
	}
}

func TestStopIsCooperative(t *testing.T) {
	resetEngine(t)
	var dispatched atomic.Int32
	var mu sync.Mutex
	seen := make(map[string]bool)
	patchProxyURLFn = func(ctx context.Context, file, value string, headers http.Header) error {
		dispatched.Add(1)
		mu.Lock()
		seen[file] = true
		mu.Unlock()
		return nil
	}
	targets := make([]EngineTarget, 0, 40)
	for i := 0; i < 40; i++ {
		targets = append(targets, EngineTarget{FileName: "f.json", Value: "http://x:2260", Label: "f"})
	}
	if err := engine.Start("set", targets, 1, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	engine.Stop()
	waitEngineIdle(t, 2*time.Second)
	if dispatched := dispatched.Load(); dispatched > int32(5) {
		t.Fatalf("stop dispatched %d writes, expected at most a few in-flight ones", dispatched)
	}
	snap := engine.Snapshot()
	if !snap.Stopped {
		t.Error("snapshot should report stopped")
	}
}

func waitEngineIdle(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !engine.Snapshot().Running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("engine did not finish in time")
}

func TestAPIKeyTargetsForSameProviderUseOneWrite(t *testing.T) {
	resetEngine(t)
	var calls atomic.Int32
	var groupedKeys atomic.Int32
	patchAPIKeyTargetsFn = func(ctx context.Context, targets []EngineTarget, headers http.Header) error {
		calls.Add(1)
		groupedKeys.Add(int32(len(targets)))
		return nil
	}
	targets := []EngineTarget{
		{Kind: targetKindAPIKeyConfig, Label: "key-one", Value: "http://one:8080", APIKey: &APIKeyTarget{ProviderFamily: "claude", FullHash: "a", Occurrence: 0}},
		{Kind: targetKindAPIKeyConfig, Label: "key-two", Value: "http://two:8080", APIKey: &APIKeyTarget{ProviderFamily: "claude", FullHash: "b", Occurrence: 0}},
	}
	if err := engine.Start("set", targets, 4, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitEngineIdle(t, 2*time.Second)
	if calls.Load() != 1 || groupedKeys.Load() != 2 {
		t.Fatalf("calls=%d keys=%d, want one write with two keys", calls.Load(), groupedKeys.Load())
	}
	if snap := engine.Snapshot(); snap.OK != 2 || snap.Failed != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
}
