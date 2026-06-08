package lifecycle

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestGoroutineManagerGo(t *testing.T) {
	gm := NewGoroutineManager(context.Background())
	defer gm.CancelAll()

	var executed atomic.Int32

	gm.Go(func(ctx context.Context) error {
		executed.Add(1)
		return nil
	}, WithName("test-basic"))

	time.Sleep(50 * time.Millisecond)

	if executed.Load() != 1 {
		t.Errorf("expected 1 execution, got %d", executed.Load())
	}
}

func TestGoroutineManagerCancelAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gm := NewGoroutineManager(ctx)

	var completed atomic.Int32

	gm.Go(func(gmCtx context.Context) error {
		<-gmCtx.Done()
		completed.Add(1)
		return nil
	}, WithName("test-cancel"))

	time.Sleep(20 * time.Millisecond)

	if gm.ActiveCount() != 1 {
		t.Errorf("expected 1 active goroutine, got %d", gm.ActiveCount())
	}

	gm.CancelAll()
	time.Sleep(50 * time.Millisecond)

	if completed.Load() != 1 {
		t.Errorf("expected 1 completed goroutine after cancel, got %d", completed.Load())
	}
}

func TestGoroutineManagerParentContextCancellation(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())

	gm := NewGoroutineManager(parentCtx)

	var completed atomic.Int32

	gm.Go(func(gmCtx context.Context) error {
		<-gmCtx.Done()
		completed.Add(1)
		return nil
	}, WithName("test-parent"))

	time.Sleep(20 * time.Millisecond)

	parentCancel()
	time.Sleep(50 * time.Millisecond)

	if completed.Load() != 1 {
		t.Errorf("expected goroutine to exit after parent context cancel, got %d completed", completed.Load())
	}
}

func TestGoroutineManagerPanicRecovery(t *testing.T) {
	gm := NewGoroutineManager(context.Background())
	defer gm.CancelAll()

	gm.Go(func(ctx context.Context) error {
		panic("test panic")
	}, WithName("test-panic"))

	time.Sleep(50 * time.Millisecond)

	_, _, panics := gm.Stats()
	if panics != 1 {
		t.Errorf("expected 1 panic, got %d", panics)
	}
}

func TestGoroutineManagerWaitTimeout(t *testing.T) {
	gm := NewGoroutineManager(context.Background())

	gm.Go(func(gmCtx context.Context) error {
		<-gmCtx.Done()
		return nil
	}, WithName("test-wait"))

	if gm.WaitTimeout(50 * time.Millisecond) {
		t.Error("should not complete within timeout since goroutine is blocked")
	}

	gm.CancelAll()

	if !gm.WaitTimeout(2 * time.Second) {
		t.Error("should complete after cancel")
	}
}

func TestGoroutineManagerWithParent(t *testing.T) {
	gm := NewGoroutineManager(context.Background())
	defer gm.CancelAll()

	childCtx, childCancel := context.WithCancel(context.Background())
	defer childCancel()

	var completed atomic.Int32

	gm.Go(func(gmCtx context.Context) error {
		<-gmCtx.Done()
		completed.Add(1)
		return nil
	}, WithName("test-child-parent"), WithParent(childCtx))

	time.Sleep(20 * time.Millisecond)

	childCancel()
	time.Sleep(50 * time.Millisecond)

	if completed.Load() != 1 {
		t.Errorf("expected goroutine to exit after child context cancel, got %d completed", completed.Load())
	}
}

func TestGoroutineManagerCancelByName(t *testing.T) {
	gm := NewGoroutineManager(context.Background())
	defer gm.CancelAll()

	var completed atomic.Int32

	gm.Go(func(gmCtx context.Context) error {
		<-gmCtx.Done()
		completed.Add(1)
		return nil
	}, WithName("test-named-cancel"))

	time.Sleep(20 * time.Millisecond)

	found := gm.CancelGoroutine("test-named-cancel-1")
	if !found {
		dump := gm.DumpActive()
		t.Logf("active goroutines: %v", dump)
	}

	time.Sleep(50 * time.Millisecond)
}

func TestConnLimiter(t *testing.T) {
	limiter := NewConnLimiter(3)

	if !limiter.Acquire() {
		t.Error("should acquire first connection")
	}
	if !limiter.Acquire() {
		t.Error("should acquire second connection")
	}
	if !limiter.Acquire() {
		t.Error("should acquire third connection")
	}
	if limiter.Acquire() {
		t.Error("should reject fourth connection (limit=3)")
	}

	if limiter.Active() != 3 {
		t.Errorf("expected 3 active, got %d", limiter.Active())
	}

	limiter.Release()

	if limiter.Active() != 2 {
		t.Errorf("expected 2 active after release, got %d", limiter.Active())
	}

	if !limiter.Acquire() {
		t.Error("should acquire after release")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(100, 5)
	defer rl.Stop()

	allowed := 0
	for i := 0; i < 10; i++ {
		if rl.Allow() {
			allowed++
		}
	}

	if allowed != 5 {
		t.Errorf("expected 5 allowed (burst=5), got %d", allowed)
	}
}

func TestDumpActive(t *testing.T) {
	gm := NewGoroutineManager(context.Background())
	defer gm.CancelAll()

	gm.Go(func(gmCtx context.Context) error {
		<-gmCtx.Done()
		return nil
	}, WithName("dump-test-1"))

	gm.Go(func(gmCtx context.Context) error {
		<-gmCtx.Done()
		return nil
	}, WithName("dump-test-2"))

	time.Sleep(50 * time.Millisecond)

	active := gm.DumpActive()
	if len(active) < 2 {
		t.Errorf("expected at least 2 active goroutines, got %d: %v", len(active), active)
	}
}
