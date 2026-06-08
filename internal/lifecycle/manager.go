package lifecycle

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type GoroutineManager struct {
	rootCtx    context.Context
	rootCancel context.CancelFunc
	wg         sync.WaitGroup
	tracker    map[string]*goroutineInfo
	mu         sync.Mutex
	spawnCount atomic.Int64
	activeCount atomic.Int64
	panicCount atomic.Int64
}

type goroutineInfo struct {
	Name      string
	StartTime time.Time
	Cancel    context.CancelFunc
}

type GoConfig struct {
	Name     string
	Parent   context.Context
	NoPanic  bool
}

func NewGoroutineManager(parent context.Context) *GoroutineManager {
	ctx, cancel := context.WithCancel(parent)
	return &GoroutineManager{
		rootCtx:    ctx,
		rootCancel: cancel,
		tracker:    make(map[string]*goroutineInfo),
	}
}

func (gm *GoroutineManager) RootContext() context.Context {
	return gm.rootCtx
}

func (gm *GoroutineManager) Go(fn func(ctx context.Context) error, opts ...func(*GoConfig)) {
	cfg := &GoConfig{Name: "anon"}
	for _, o := range opts {
		o(cfg)
	}

	parentCtx := gm.rootCtx
	if cfg.Parent != nil {
		parentCtx = cfg.Parent
	}

	childCtx, cancel := context.WithCancel(parentCtx)
	name := cfg.Name

	gm.mu.Lock()
	seq := gm.spawnCount.Add(1)
	fullName := fmt.Sprintf("%s-%d", name, seq)
	gm.tracker[fullName] = &goroutineInfo{
		Name:      fullName,
		StartTime: time.Now(),
		Cancel:    cancel,
	}
	gm.mu.Unlock()

	gm.activeCount.Add(1)
	gm.wg.Add(1)

	go func() {
		defer func() {
			cancel()

			gm.mu.Lock()
			delete(gm.tracker, fullName)
			gm.mu.Unlock()

			gm.activeCount.Add(-1)
			gm.wg.Done()

			if !cfg.NoPanic {
				if r := recover(); r != nil {
					gm.panicCount.Add(1)
					buf := make([]byte, 4096)
					n := runtime.Stack(buf, false)
					log.Printf("[lifecycle] PANIC in %s: %v\n%s", fullName, r, buf[:n])
				}
			}
		}()

		if err := fn(childCtx); err != nil {
			if childCtx.Err() == nil {
				log.Printf("[lifecycle] goroutine %s exited with error: %v", fullName, err)
			}
		}
	}()
}

func WithName(name string) func(*GoConfig) {
	return func(cfg *GoConfig) { cfg.Name = name }
}

func WithParent(ctx context.Context) func(*GoConfig) {
	return func(cfg *GoConfig) { cfg.Parent = ctx }
}

func (gm *GoroutineManager) CancelGoroutine(name string) bool {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	for key, info := range gm.tracker {
		if key == name || info.Name == name {
			info.Cancel()
			return true
		}
	}
	return false
}

func (gm *GoroutineManager) CancelAll() {
	gm.rootCancel()

	gm.mu.Lock()
	for _, info := range gm.tracker {
		info.Cancel()
	}
	gm.mu.Unlock()
}

func (gm *GoroutineManager) WaitTimeout(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		gm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (gm *GoroutineManager) ActiveCount() int64 {
	return gm.activeCount.Load()
}

func (gm *GoroutineManager) Stats() (spawned int64, active int64, panics int64) {
	return gm.spawnCount.Load(), gm.activeCount.Load(), gm.panicCount.Load()
}

func (gm *GoroutineManager) DumpActive() []string {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	result := make([]string, 0, len(gm.tracker))
	for _, info := range gm.tracker {
		age := time.Since(info.StartTime).Truncate(time.Second)
		result = append(result, fmt.Sprintf("%s (age=%s)", info.Name, age))
	}
	return result
}

type ConnLimiter struct {
	maxConns   int64
	active     atomic.Int64
	notifyChan chan struct{}
}

func NewConnLimiter(maxConns int) *ConnLimiter {
	return &ConnLimiter{
		maxConns:   int64(maxConns),
		notifyChan: make(chan struct{}, maxConns),
	}
}

func (cl *ConnLimiter) Acquire() bool {
	current := cl.active.Add(1)
	if current > cl.maxConns {
		cl.active.Add(-1)
		return false
	}
	cl.notifyChan <- struct{}{}
	return true
}

func (cl *ConnLimiter) Release() {
	cl.active.Add(-1)
	select {
	case <-cl.notifyChan:
	default:
	}
}

func (cl *ConnLimiter) Active() int64 {
	return cl.active.Load()
}

func (cl *ConnLimiter) Max() int64 {
	return cl.maxConns
}

type RateLimiter struct {
	tokens   chan struct{}
	stopOnce sync.Once
	stopChan chan struct{}
}

func NewRateLimiter(rate int, burst int) *RateLimiter {
	rl := &RateLimiter{
		tokens:   make(chan struct{}, burst),
		stopChan: make(chan struct{}),
	}

	for i := 0; i < burst; i++ {
		rl.tokens <- struct{}{}
	}

	go rl.refill(rate, burst)

	return rl
}

func (rl *RateLimiter) refill(rate, burst int) {
	interval := time.Second / time.Duration(rate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stopChan:
			return
		case <-ticker.C:
			select {
			case rl.tokens <- struct{}{}:
			default:
			}
		}
	}
}

func (rl *RateLimiter) Allow() bool {
	select {
	case <-rl.tokens:
		return true
	default:
		return false
	}
}

func (rl *RateLimiter) Wait(ctx context.Context) error {
	select {
	case <-rl.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-rl.stopChan:
		return fmt.Errorf("rate limiter stopped")
	}
}

func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.stopChan)
	})
}

type HealthMonitor struct {
	gm       *GoroutineManager
	interval time.Duration
	cancel   context.CancelFunc
}

func NewHealthMonitor(gm *GoroutineManager, interval time.Duration) *HealthMonitor {
	return &HealthMonitor{
		gm:       gm,
		interval: interval,
	}
}

func (hm *HealthMonitor) Start(parentCtx context.Context) {
	ctx, cancel := context.WithCancel(parentCtx)
	hm.cancel = cancel

	go func() {
		ticker := time.NewTicker(hm.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, active, panics := hm.gm.Stats()
				if active > 500 {
					activeList := hm.gm.DumpActive()
					log.Printf("[health] WARNING: %d active goroutines, panics=%d", active, panics)
					if len(activeList) > 10 {
						for i := 0; i < 10; i++ {
							log.Printf("[health]   - %s", activeList[i])
						}
						log.Printf("[health]   ... and %d more", len(activeList)-10)
					}
				}
			}
		}
	}()
}

func (hm *HealthMonitor) Stop() {
	if hm.cancel != nil {
		hm.cancel()
	}
}
