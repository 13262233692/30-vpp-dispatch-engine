package grpc

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vpp/dispatch-engine/internal/lifecycle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type InterceptorConfig struct {
	GM             *lifecycle.GoroutineManager
	ConnLimiter    *lifecycle.ConnLimiter
	RateLimiter    *lifecycle.RateLimiter
	RequestTimeout time.Duration
}

type streamState struct {
	method    string
	startTime time.Time
	peerAddr  string
	ctx       context.Context
	cancel    context.CancelFunc
}

type ServerInterceptor struct {
	gm            *lifecycle.GoroutineManager
	connLimiter   *lifecycle.ConnLimiter
	rateLimiter   *lifecycle.RateLimiter
	reqTimeout    time.Duration
	activeStreams sync.Map
	streamCount   atomic.Int64
	reqCount      atomic.Int64
	panicCount    atomic.Int64
	rejectCount   atomic.Int64
	timeoutCount  atomic.Int64
}

func NewServerInterceptor(cfg InterceptorConfig) *ServerInterceptor {
	return &ServerInterceptor{
		gm:          cfg.GM,
		connLimiter: cfg.ConnLimiter,
		rateLimiter: cfg.RateLimiter,
		reqTimeout:  cfg.RequestTimeout,
	}
}

func (si *ServerInterceptor) UnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	si.reqCount.Add(1)

	if si.rateLimiter != nil && !si.rateLimiter.Allow() {
		si.rejectCount.Add(1)
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	var clientAddr string
	if p, ok := peer.FromContext(ctx); ok {
		clientAddr = p.Addr.String()
	}

	var handlerCtx context.Context
	var cancel context.CancelFunc

	if si.reqTimeout > 0 {
		handlerCtx, cancel = context.WithTimeout(ctx, si.reqTimeout)
	} else {
		handlerCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	done := make(chan result, 1)

	si.gm.Go(func(gmCtx context.Context) error {
		defer func() {
			if r := recover(); r != nil {
				si.panicCount.Add(1)
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				log.Printf("[grpc-interceptor] PANIC in %s from %s: %v\n%s", info.FullMethod, clientAddr, r, buf[:n])
				done <- result{err: status.Error(codes.Internal, "internal server error")}
			}
		}()

		resp, err := handler(handlerCtx, req)
		done <- result{resp: resp, err: err}
		return nil
	}, lifecycle.WithName("grpc-unary:"+info.FullMethod))

	select {
	case <-ctx.Done():
		si.timeoutCount.Add(1)
		cancel()
		return nil, status.FromContextError(ctx.Err()).Err()
	case r := <-done:
		return r.resp, r.err
	}
}

type result struct {
	resp interface{}
	err  error
}

func (si *ServerInterceptor) StreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	si.streamCount.Add(1)

	if si.connLimiter != nil && !si.connLimiter.Acquire() {
		si.rejectCount.Add(1)
		si.streamCount.Add(-1)
		return status.Error(codes.ResourceExhausted, "connection limit exceeded")
	}

	if si.rateLimiter != nil && !si.rateLimiter.Allow() {
		si.rejectCount.Add(1)
		si.streamCount.Add(-1)
		if si.connLimiter != nil {
			si.connLimiter.Release()
		}
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	var clientAddr string
	if p, ok := peer.FromContext(ss.Context()); ok {
		clientAddr = p.Addr.String()
	}

	streamCtx, streamCancel := context.WithCancel(ss.Context())
	defer streamCancel()

	state := &streamState{
		method:    info.FullMethod,
		startTime: time.Now(),
		peerAddr:  clientAddr,
		ctx:       streamCtx,
		cancel:    streamCancel,
	}

	streamID := time.Now().UnixNano()
	si.activeStreams.Store(streamID, state)
	defer func() {
		si.activeStreams.Delete(streamID)
		si.streamCount.Add(-1)
		if si.connLimiter != nil {
			si.connLimiter.Release()
		}
	}()

	wrappedStream := &contextStream{ServerStream: ss, ctx: streamCtx}

	done := make(chan error, 1)

	si.gm.Go(func(gmCtx context.Context) error {
		defer func() {
			if r := recover(); r != nil {
				si.panicCount.Add(1)
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				log.Printf("[grpc-interceptor] PANIC in stream %s from %s: %v\n%s", info.FullMethod, clientAddr, r, buf[:n])
				done <- status.Error(codes.Internal, "internal server error")
			}
		}()

		err := handler(srv, wrappedStream)
		done <- err
		return nil
	}, lifecycle.WithName("grpc-stream:"+info.FullMethod))

	select {
	case <-streamCtx.Done():
		streamCancel()
		log.Printf("[grpc-interceptor] stream %s from %s context cancelled (age=%v)",
			info.FullMethod, clientAddr, time.Since(state.startTime))
		return status.FromContextError(streamCtx.Err()).Err()
	case err := <-done:
		return err
	}
}

type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (cs *contextStream) Context() context.Context {
	return cs.ctx
}

func (si *ServerInterceptor) Stats() (requests int64, activeStreams int64, panics int64, rejects int64, timeouts int64) {
	return si.reqCount.Load(), si.streamCount.Load(), si.panicCount.Load(), si.rejectCount.Load(), si.timeoutCount.Load()
}

func (si *ServerInterceptor) ActiveStreamCount() int64 {
	return si.streamCount.Load()
}

func (si *ServerInterceptor) DumpActiveStreams() []string {
	var result []string
	si.activeStreams.Range(func(key, value interface{}) bool {
		if state, ok := value.(*streamState); ok {
			age := time.Since(state.startTime).Truncate(time.Millisecond)
			result = append(result, fmt.Sprintf("%s from %s (age=%v)", state.method, state.peerAddr, age))
		}
		return true
	})
	return result
}
