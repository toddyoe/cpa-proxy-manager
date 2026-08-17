package main

// engine.go runs one batch at a time. Start receives fully resolved targets
// (file name plus the value to write), executes them with bounded concurrency
// through the loopback management client, and keeps a failure list in memory
// only. Stop is cooperative: no new writes are dispatched, finished ones stay.
import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

var ErrBusy = errors.New("已有批次在执行中")

var ErrEmptyTargets = errors.New("没有可执行的目标")

// EngineTarget is one resolved write: FileName to patch and Value to store
// ("" clears the proxy).
type EngineTarget struct {
	FileName string
	Value    string
	Label    string
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

// patchProxyURLFn is swappable for tests.
var patchProxyURLFn = PatchProxyURL

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

	go e.run(targets, concurrency, headers, stopCh)
	return nil
}

func (e *engineState) run(targets []EngineTarget, concurrency int, headers http.Header, stopCh <-chan struct{}) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range targets {
		select {
		case <-stopCh:
			// Stop dispatching; remaining targets are simply not scheduled.
			wg.Wait()
			e.finish()
			return
		default:
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(target EngineTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			select {
			case <-stopCh:
				return
			default:
			}
			e.markCurrent(target.Label)
			ctx, cancel := context.WithTimeout(context.Background(), mgmtRequestTimeout)
			defer cancel()
			if err := patchProxyURLFn(ctx, target.FileName, target.Value, headers); err != nil {
				e.recordFailure(target.Label, err)
			} else {
				e.recordOK()
			}
		}(targets[i])
	}
	wg.Wait()
	e.finish()
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

func (e *engineState) recordOK() {
	e.mu.Lock()
	e.done++
	e.ok++
	e.mu.Unlock()
}

func (e *engineState) recordFailure(label string, err error) {
	reason := "写入失败"
	if err != nil {
		reason = err.Error()
	}
	e.mu.Lock()
	e.done++
	e.failures = append(e.failures, FailEntry{Name: label, Reason: reason})
	e.mu.Unlock()
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
