package execution

import (
	"context"
	"errors"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"sync"
	"testing"
	"time"
)

type fake struct {
	mu                                      sync.Mutex
	messages                                []opencode.Message
	status                                  string
	missing, disconnected, fail, permission bool
	sends, aborts                           int
	mid                                     string
	entered, release                        chan struct{}
}

func (f *fake) History(context.Context, string) ([]opencode.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disconnected {
		return nil, errors.New("offline")
	}
	return append([]opencode.Message(nil), f.messages...), nil
}
func (f *fake) Status(context.Context) (map[string]opencode.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disconnected {
		return nil, errors.New("offline")
	}
	if f.missing {
		return map[string]opencode.Status{}, nil
	}
	return map[string]opencode.Status{"ses_test": {Type: f.status}}, nil
}
func (f *fake) Permissions(context.Context) ([]opencode.Permission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.permission {
		return []opencode.Permission{{SessionID: "ses_test"}}, nil
	}
	return nil, nil
}
func (f *fake) Prompt(ctx context.Context, _, _, mid string) error {
	f.mu.Lock()
	f.sends++
	f.mid = mid
	fail := f.fail
	f.mu.Unlock()
	if f.entered != nil {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return errors.New("lost reply")
	}
	return nil
}
func (f *fake) Abort(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborts++
	return nil
}
func setup(t *testing.T, f *fake) *Manager {
	t.Helper()
	m := NewWithTiming(f, time.Second, 2*time.Millisecond)
	t.Cleanup(m.Close)
	return m
}
func waitState(t *testing.T, m *Manager, id, state string) Execution {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		x, e := m.Get(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		if x.State == state {
			return x
		}
		time.Sleep(time.Millisecond)
	}
	x, _ := m.Get(context.Background(), id)
	t.Fatalf("want %s got %+v", state, x)
	return x
}
func complete(f *fake, mid, finish, errorJSON string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msg := opencode.Message{}
	msg.Info.ID = "msg_assistant" + mid
	msg.Info.Role = "assistant"
	msg.Info.SessionID = "ses_test"
	msg.Info.ParentID = mid
	msg.Info.Finish = finish
	msg.Info.Time.Completed = 1
	msg.Info.Error = []byte(errorJSON)
	f.messages = append(f.messages, msg)
}
func TestTwoTurnsCorrelationAndResults(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	ctx := context.Background()
	for _, key := range []string{"first-key", "second-key"} {
		x, e := m.Send(ctx, "ses_test", "test", key)
		if e != nil {
			t.Fatal(e)
		}
		complete(f, "msg_unrelated", "stop", "")
		complete(f, x.MessageID, "tool-calls", "")
		waitState(t, m, x.ID, "running")
		complete(f, x.MessageID, "stop", "")
		done := waitState(t, m, x.ID, "completed")
		if done.SessionID != "opencode:ses_test" || done.Ended == nil {
			t.Fatal(done)
		}
		_, msgs, e := m.Result(ctx, x.ID)
		if e != nil || len(msgs) != 2 {
			t.Fatalf("result: %d %v", len(msgs), e)
		}
		for _, v := range msgs {
			if v.Info.ParentID != x.MessageID {
				t.Fatal("unrelated result")
			}
		}
	}
}
func TestDuplicateConflictBusyConcurrent(t *testing.T) {
	f := &fake{status: "idle", entered: make(chan struct{}), release: make(chan struct{})}
	m := setup(t, f)
	x, e := m.Send(context.Background(), "ses_test", "test", "same-key")
	if e != nil {
		t.Fatal(e)
	}
	<-f.entered
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			y, e := m.Send(context.Background(), "ses_test", "test", "same-key")
			if e != nil || y.ID != x.ID {
				t.Error("duplicate", e)
			}
		}()
	}
	wg.Wait()
	if _, e = m.Send(context.Background(), "ses_test", "changed", "same-key"); e == nil {
		t.Fatal("conflict accepted")
	}
	if _, e = m.Send(context.Background(), "ses_test", "test", "other-key"); e == nil {
		t.Fatal("busy accepted")
	}
	close(f.release)
	waitState(t, m, x.ID, "running")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends != 1 {
		t.Fatal(f.sends)
	}
}
func TestUnknownSubmissionNeverRetried(t *testing.T) {
	f := &fake{status: "idle", fail: true}
	m := setup(t, f)
	x, e := m.Send(context.Background(), "ses_test", "test", "same-key")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, m, x.ID, "unknown")
	_, _ = m.Send(context.Background(), "ses_test", "test", "same-key")
	if _, e = m.Send(context.Background(), "ses_test", "test", "new--key"); e == nil {
		t.Fatal("uncertain guard released")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends != 1 {
		t.Fatal(f.sends)
	}
}
func TestDisconnectAndMissingStatusRecoverFromProof(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "disconnect"}[missing], func(t *testing.T) {
			f := &fake{status: "idle"}
			m := setup(t, f)
			x, _ := m.Send(context.Background(), "ses_test", "test", "same-key")
			waitState(t, m, x.ID, "running")
			f.mu.Lock()
			f.missing = missing
			f.disconnected = !missing
			f.mu.Unlock()
			waitState(t, m, x.ID, "unknown")
			complete(f, x.MessageID, "stop", "")
			f.mu.Lock()
			f.disconnected = false
			f.mu.Unlock()
			waitState(t, m, x.ID, "completed")
		})
	}
}
func TestTimeoutAndShutdownRetainGuard(t *testing.T) {
	for _, shutdown := range []bool{true, false} {
		f := &fake{status: "idle"}
		m := NewWithTiming(f, 30*time.Millisecond, time.Millisecond)
		x, _ := m.Send(context.Background(), "ses_test", "test", "same-key")
		if shutdown {
			m.Close()
		} else {
			waitState(t, m, x.ID, "unknown")
			m.Close()
		}
		x, _ = m.Get(context.Background(), x.ID)
		if x.State != "unknown" {
			t.Fatal(x)
		}
	}
}
func TestCancellationBeforeAndAfterAcceptance(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := m.Send(ctx, "ses_test", "test", "same-key"); e == nil {
		t.Fatal("canceled request accepted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	x, e := m.Send(ctx, "ses_test", "test", "same-key")
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	complete(f, x.MessageID, "stop", "")
	waitState(t, m, x.ID, "completed")
}
func TestPermissionErrorAndExplicitInterrupt(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	x, _ := m.Send(context.Background(), "ses_test", "test", "same-key")
	waitState(t, m, x.ID, "running")
	f.mu.Lock()
	f.permission = true
	f.mu.Unlock()
	waitState(t, m, x.ID, "waiting_for_permission")
	if _, e := m.Interrupt(context.Background(), x.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Send(context.Background(), "ses_test", "test", "other-key"); e == nil {
		t.Fatal("abort ack released guard")
	}
	f.mu.Lock()
	f.permission = false
	f.mu.Unlock()
	complete(f, x.MessageID, "", `{"name":"MessageAbortedError"}`)
	waitState(t, m, x.ID, "interrupted")
	y, e := m.Send(context.Background(), "ses_test", "test", "other-key")
	if e != nil {
		t.Fatal(e)
	}
	complete(f, y.MessageID, "", `{"name":"APIError"}`)
	waitState(t, m, y.ID, "failed")
}
func TestNativeBusyAndValidation(t *testing.T) {
	f := &fake{status: "busy"}
	m := setup(t, f)
	if _, e := m.Send(context.Background(), "ses_test", "test", "same-key"); e == nil {
		t.Fatal("busy accepted")
	}
	for _, id := range []string{"", "s", "ses_../x", "opencode:ses_test"} {
		if _, e := m.Send(context.Background(), id, "test", "same-key"); e == nil {
			t.Fatal(id)
		}
	}
	if _, e := m.Get(context.Background(), "missing"); e == nil {
		t.Fatal("missing accepted")
	}
}

func TestMessageIDChronological(t *testing.T) {
	previous := ""
	for i := 0; i < 100; i++ {
		id := messageID()
		if len(id) != 30 || id <= previous {
			t.Fatalf("invalid native ascending ID %q", id)
		}
		previous = id
	}
}
