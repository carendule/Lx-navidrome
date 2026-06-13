package nativeapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseRecorder is a minimal http.ResponseWriter + http.Flusher with a
// mutex-protected buffer, avoiding the data race of httptest.ResponseRecorder.
type sseRecorder struct {
	mu  sync.Mutex
	hdr http.Header
	buf bytes.Buffer
}

func newSSERecorder() *sseRecorder { return &sseRecorder{hdr: make(http.Header)} }

func (r *sseRecorder) Header() http.Header { return r.hdr }
func (r *sseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}
func (r *sseRecorder) WriteHeader(int) {}
func (r *sseRecorder) Flush()          {}
func (r *sseRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func waitFor(t *testing.T, dur time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestDownloadTaskBrokerPushesOnMutation: 验证 SSE 端点能在 task update 时推送。
func TestDownloadTaskBrokerPushesOnMutation(t *testing.T) {
	id := "sse-test-task"
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{ID: id, Mode: "server", Status: "queued"}
	onlineDownloadTasks.Unlock()
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, id)
		onlineDownloadTasks.Unlock()
	}()

	api := &Router{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	rec := newSSERecorder()
	done := make(chan struct{})
	go func() {
		api.onlineServerDownloadTasksStream(rec, req)
		close(done)
	}()

	if !waitFor(t, 2*time.Second, func() bool { return strings.Contains(rec.String(), "stream-open") }) {
		t.Fatalf("expected stream-open preamble, body=%q", rec.String())
	}

	updateOnlineDownloadTask(id, func(t *onlineDownloadTask) { t.Status = "downloading"; t.Progress = 42 })

	if !waitFor(t, 2*time.Second, func() bool { return strings.Contains(rec.String(), "event: tasks-changed") }) {
		t.Errorf("expected event: tasks-changed, body=%q", rec.String())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("handler did not exit after context cancel")
	}
}

// TestBrokerPushesOnTaskCreate: 验证创建新任务时也 broadcast。
func TestBrokerPushesOnTaskCreate(t *testing.T) {
	id := "sse-create-task"
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, id)
		onlineDownloadTasks.Unlock()
	}()

	api := &Router{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	rec := newSSERecorder()
	done := make(chan struct{})
	go func() {
		api.onlineServerDownloadTasksStream(rec, req)
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return strings.Contains(rec.String(), "stream-open") })

	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{ID: id, Mode: "server", Status: "queued"}
	onlineDownloadTasks.Unlock()
	broadcastDownloadTaskChange()

	if !waitFor(t, 2*time.Second, func() bool { return strings.Contains(rec.String(), "event: tasks-changed") }) {
		t.Fatalf("expected tasks-changed on create, body=%q", rec.String())
	}

	cancel()
	<-done
}

// TestBrokerHighFrequencyNoMissedWakeup: 在高频 broadcast 下 SSE handler
// 至少能收到一次 tasks-changed（演示新模型不丢事件）。
func TestBrokerHighFrequencyNoMissedWakeup(t *testing.T) {
	id := "sse-hf-task"
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{ID: id, Mode: "server", Status: "queued"}
	onlineDownloadTasks.Unlock()
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, id)
		onlineDownloadTasks.Unlock()
	}()

	api := &Router{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	rec := newSSERecorder()
	done := make(chan struct{})
	go func() {
		api.onlineServerDownloadTasksStream(rec, req)
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return strings.Contains(rec.String(), "stream-open") })

	// Fire 500 rapid updates simulating per-chunk progress ticks.
	for i := 0; i < 500; i++ {
		updateOnlineDownloadTask(id, func(t *onlineDownloadTask) { t.Progress = i % 100 })
	}

	// SSE handler must receive at least one tasks-changed within 2s.
	if !waitFor(t, 2*time.Second, func() bool { return strings.Contains(rec.String(), "event: tasks-changed") }) {
		t.Fatalf("SSE handler missed all 500 broadcast signals, body=%q", rec.String())
	}

	cancel()
	<-done
}

// TestSubscribeUnsubscribeNoLeak: 验证 unsubscribe 之后不再收到通知。
func TestSubscribeUnsubscribeNoLeak(t *testing.T) {
	id := "sse-leak-task"
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{ID: id, Mode: "server", Status: "queued"}
	onlineDownloadTasks.Unlock()
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, id)
		onlineDownloadTasks.Unlock()
	}()

	ch, unsub := subscribeDownloadTaskNotifications()
	unsub()
	updateOnlineDownloadTask(id, func(t *onlineDownloadTask) { t.Status = "downloading" })

	select {
	case <-ch:
		t.Error("expected no signal after unsubscribe")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestBrokerRapidSubscribersUnderRace: 并发压力测试，断言 subs map 终态为空。
func TestBrokerRapidSubscribersUnderRace(t *testing.T) {
	id := "sse-race-task"
	onlineDownloadTasks.Lock()
	onlineDownloadTasks.items[id] = &onlineDownloadTask{ID: id, Mode: "server", Status: "queued"}
	onlineDownloadTasks.Unlock()
	defer func() {
		onlineDownloadTasks.Lock()
		delete(onlineDownloadTasks.items, id)
		onlineDownloadTasks.Unlock()
	}()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < 50; r++ {
				ch, unsub := subscribeDownloadTaskNotifications()
				select {
				case <-ch:
				default:
				}
				unsub()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 4000; i++ {
			updateOnlineDownloadTask(id, func(t *onlineDownloadTask) { t.Progress = i % 100 })
		}
	}()

	wg.Wait()
	<-done

	downloadTaskBroker.Lock()
	leftover := len(downloadTaskBroker.subs)
	downloadTaskBroker.Unlock()
	if leftover != 0 {
		t.Errorf("expected 0 leftover subscribers, got %d", leftover)
	}

	ch, unsub := subscribeDownloadTaskNotifications()
	_ = fmt.Sprintf("%v", ch)
	unsub()
}
