package main

// engine.go runs one batch at a time. Targets are grouped into write units:
// each auth file is its own unit, while API keys sharing a provider config are
// updated in one ETag-guarded config write.
import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

var ErrBusy = errors.New("已有批次在执行中")

var ErrEmptyTargets = errors.New("没有可执行的目标")

const (
	targetKindAuthFile     = "auth_file"
	targetKindAPIKeyConfig = "api_key_config"
)

// EngineTarget is one logical credential write. APIKey is non-nil only for
// config-sourced API keys; file-backed API keys continue through FileName.
type EngineTarget struct {
	Kind     string
	TargetID string
	FileName string
	Value    string
	Label    string
	APIKey   *APIKeyTarget
}

// FailEntry describes one failed write.
type FailEntry struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Status is the poll payload served by GET /status.
type Status struct {
	Running   bool        `json:"running"`
	Mode      string      `json:"mode,omitempty"`
	Total     int         `json:"total"`
	Done      int         `json:"done"`
	OK        int         `json:"ok"`
	Failed    int         `json:"failed"`
	Stopped   bool        `json:"stopped,omitempty"`
	Finished  bool        `json:"finished"`
	Current   string      `json:"current,omitempty"`
	StartedAt string      `json:"started_at,omitempty"`
	EndedAt   string      `json:"ended_at,omitempty"`
	Failures  []FailEntry `json:"failures"`
}

type engineState struct {
	mu        sync.Mutex
	running   bool
	stopped   bool
	mode      string
	total     int
	done      int
	ok        int
	failures  []FailEntry
	current   string
	startedAt time.Time
	endedAt   time.Time
	stopCh    chan struct{}
}

// engine is the package-level singleton wired to the management routes.
var engine = &engineState{}

// Swappable for tests.
var patchProxyURLFn = PatchProxyURL
var patchAPIKeyTargetsFn = applyAPIKeyTargets

func (e *engineState) Start(mode string, targets []EngineTarget, concurrency int, headers http.Header) error {
	if concurrency < minWriteConcurrency {
		concurrency = minWriteConcurrency
	}
	if concurrency > maxWriteConcurrency {
		concurrency = maxWriteConcurrency
	}
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return ErrBusy
	}
	if len(targets) == 0 {
		e.mu.Unlock()
		return ErrEmptyTargets
	}
	e.running = true
	e.stopped = false
	e.mode = mode
	e.total = len(targets)
	e.done = 0
	e.ok = 0
	e.failures = nil
	e.current = ""
	e.startedAt = time.Now()
	e.stopCh = make(chan struct{})
	stopCh := e.stopCh
	e.mu.Unlock()

	go e.run(groupEngineTargets(targets), concurrency, headers, stopCh)
	return nil
}

func groupEngineTargets(targets []EngineTarget) [][]EngineTarget {
	units := make([][]EngineTarget, 0, len(targets))
	apiKeyUnits := map[string]int{}
	for _, target := range targets {
		kind := target.Kind
		if kind == "" {
			kind = targetKindAuthFile
		}
		if kind != targetKindAPIKeyConfig || target.APIKey == nil {
			units = append(units, []EngineTarget{target})
			continue
		}
		family := target.APIKey.ProviderFamily
		if idx, ok := apiKeyUnits[family]; ok {
			units[idx] = append(units[idx], target)
			continue
		}
		apiKeyUnits[family] = len(units)
		units = append(units, []EngineTarget{target})
	}
	return units
}

func (e *engineState) run(units [][]EngineTarget, concurrency int, headers http.Header, stopCh <-chan struct{}) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, unit := range units {
		select {
		case <-stopCh:
			wg.Wait()
			e.finish()
			return
		default:
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(unit []EngineTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			select {
			case <-stopCh:
				return
			default:
			}
			e.markCurrent(unitLabel(unit))
			ctx, cancel := context.WithTimeout(context.Background(), mgmtRequestTimeout)
			defer cancel()
			e.recordUnit(unit, executeEngineUnit(ctx, unit, headers))
		}(unit)
	}
	wg.Wait()
	e.finish()
}

func unitLabel(unit []EngineTarget) string {
	if len(unit) == 0 {
		return ""
	}
	if len(unit) == 1 {
		return unit[0].Label
	}
	return fmt.Sprintf("%s 等 %d 个 Key", unit[0].Label, len(unit))
}

func executeEngineUnit(ctx context.Context, unit []EngineTarget, headers http.Header) error {
	if len(unit) == 0 {
		return nil
	}
	kind := unit[0].Kind
	if kind == "" {
		kind = targetKindAuthFile
	}
	switch kind {
	case targetKindAuthFile:
		if len(unit) != 1 {
			return fmt.Errorf("auth file unit contains multiple targets")
		}
		return patchProxyURLFn(ctx, unit[0].FileName, unit[0].Value, headers)
	case targetKindAPIKeyConfig:
		return patchAPIKeyTargetsFn(ctx, unit, headers)
	default:
		return fmt.Errorf("unknown target kind %q", kind)
	}
}

func (e *engineState) finish() {
	e.mu.Lock()
	e.running = false
	e.endedAt = time.Now()
	e.current = ""
	e.mu.Unlock()
}

func (e *engineState) Stop() {
	e.mu.Lock()
	if e.stopCh != nil && e.running {
		e.stopped = true
		close(e.stopCh)
	}
	e.mu.Unlock()
}

func (e *engineState) markCurrent(label string) {
	e.mu.Lock()
	e.current = label
	e.mu.Unlock()
}

func (e *engineState) recordUnit(unit []EngineTarget, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		e.done += len(unit)
		e.ok += len(unit)
		return
	}
	reason := err.Error()
	if reason == "" {
		reason = "写入失败"
	}
	for _, target := range unit {
		e.done++
		e.failures = append(e.failures, FailEntry{Name: target.Label, Reason: reason})
	}
}

func (e *engineState) Snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	running := e.running
	failures := append([]FailEntry(nil), e.failures...)
	return Status{
		Running:   running,
		Mode:      e.mode,
		Total:     e.total,
		Done:      e.done,
		OK:        e.ok,
		Failed:    len(failures),
		Stopped:   e.stopped && !running,
		Finished:  !running,
		Current:   e.current,
		StartedAt: formatEngineTime(e.startedAt),
		EndedAt:   formatEngineTime(e.endedAt),
		Failures:  failures,
	}
}

func formatEngineTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("15:04:05")
}
