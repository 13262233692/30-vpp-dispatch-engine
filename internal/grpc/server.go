package grpc

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	proto "github.com/vpp/dispatch-engine/api/proto"
	"github.com/vpp/dispatch-engine/internal/dispatcher"
	"github.com/vpp/dispatch-engine/internal/lifecycle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type DispatchServiceHandler interface {
	LoadShedding(ctx context.Context, req *proto.LoadSheddingRequest) (*proto.LoadSheddingResponse, error)
	GetFleetStatus(ctx context.Context, req *proto.FleetStatusRequest) (*proto.FleetStatusResponse, error)
	EmergencyShutdown(ctx context.Context, req *proto.EmergencyShutdownRequest) (*proto.EmergencyShutdownResponse, error)
}

type DispatchServer struct {
	engine      *dispatcher.Engine
	server      *grpc.Server
	interceptor *ServerInterceptor
	addr        string
	reqCount    atomic.Int64
	errCount    atomic.Int64
	mu          sync.Mutex
	startTime   time.Time
	gm          *lifecycle.GoroutineManager
	connLimiter *lifecycle.ConnLimiter
}

type ServerConfig struct {
	ListenAddr    string
	Engine        *dispatcher.Engine
	GM            *lifecycle.GoroutineManager
	MaxConns      int
	MaxRate       int
	RateBurst     int
	RequestTimeout time.Duration
}

func NewDispatchServer(cfg ServerConfig) *DispatchServer {
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 1000
	}
	if cfg.MaxRate == 0 {
		cfg.MaxRate = 1000
	}
	if cfg.RateBurst == 0 {
		cfg.RateBurst = 2000
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 30 * time.Second
	}

	connLimiter := lifecycle.NewConnLimiter(cfg.MaxConns)

	gm := cfg.GM
	if gm == nil {
		gm = lifecycle.NewGoroutineManager(context.Background())
	}

	interceptor := NewServerInterceptor(InterceptorConfig{
		GM:             gm,
		ConnLimiter:    connLimiter,
		RateLimiter:    lifecycle.NewRateLimiter(cfg.MaxRate, cfg.RateBurst),
		RequestTimeout: cfg.RequestTimeout,
	})

	return &DispatchServer{
		addr:        cfg.ListenAddr,
		engine:      cfg.Engine,
		interceptor: interceptor,
		gm:          gm,
		connLimiter: connLimiter,
	}
}

func (s *DispatchServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("grpc: listen failed on %s: %w", s.addr, err)
	}

	s.server = grpc.NewServer(
		grpc.MaxRecvMsgSize(4*1024*1024),
		grpc.MaxSendMsgSize(4*1024*1024),
		grpc.MaxConcurrentStreams(100),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,
			MaxConnectionAge:      10 * time.Minute,
			MaxConnectionAgeGrace: 5 * time.Second,
			Time:                  30 * time.Second,
			Timeout:               10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.UnaryInterceptor(s.interceptor.UnaryInterceptor),
		grpc.StreamInterceptor(s.interceptor.StreamInterceptor),
	)

	RegisterDispatchService(s.server, s)

	s.startTime = time.Now()

	s.gm.Go(func(gmCtx context.Context) error {
		log.Printf("[grpc] server listening on %s (maxConns=%d, maxRate=%d/s, timeout=%v)",
			s.addr, s.connLimiter.Max(), 1000, 30*time.Second)
		if err := s.server.Serve(ln); err != nil {
			select {
			case <-gmCtx.Done():
				log.Printf("[grpc] server stopped by context cancellation")
			default:
				log.Printf("[grpc] server error: %v", err)
			}
		}
		return nil
	}, lifecycle.WithName("grpc-serve"))

	s.gm.Go(func(gmCtx context.Context) error {
		<-gmCtx.Done()
		log.Printf("[grpc] context cancelled, initiating graceful stop")
		stopped := make(chan struct{})
		go func() {
			s.server.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
			log.Printf("[grpc] graceful stop completed")
		case <-time.After(10 * time.Second):
			log.Printf("[grpc] graceful stop timeout, forcing stop")
			s.server.Stop()
		}
		return nil
	}, lifecycle.WithName("grpc-shutdown-watcher"))

	return nil
}

func (s *DispatchServer) LoadShedding(ctx context.Context, req *proto.LoadSheddingRequest) (*proto.LoadSheddingResponse, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("request cancelled: %w", ctx.Err())
	default:
	}

	s.reqCount.Add(1)
	log.Printf("[grpc] LoadShedding request: id=%s target=%.2fMW strategy=%s",
		req.RequestID, req.TargetReductionMW, req.Strategy)

	resp := s.engine.LoadShedding(req)

	log.Printf("[grpc] LoadShedding response: id=%s success=%v reduction=%.2fMW devices=%d compute=%dus",
		resp.RequestID, resp.Success, resp.ActualReductionMW, resp.DeviceCount, resp.ComputeTimeUS)

	return resp, nil
}

func (s *DispatchServer) GetFleetStatus(ctx context.Context, req *proto.FleetStatusRequest) (*proto.FleetStatusResponse, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("request cancelled: %w", ctx.Err())
	default:
	}

	s.reqCount.Add(1)
	resp := s.engine.FleetStatus(req)

	log.Printf("[grpc] FleetStatus: %d devices, capacity=%.2fMW, available=%.2fMW",
		resp.DeviceCount, resp.TotalCapacityMW, resp.TotalAvailableMW)

	return resp, nil
}

func (s *DispatchServer) EmergencyShutdown(ctx context.Context, req *proto.EmergencyShutdownRequest) (*proto.EmergencyShutdownResponse, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("request cancelled: %w", ctx.Err())
	default:
	}

	s.reqCount.Add(1)
	log.Printf("[grpc] EmergencyShutdown request: id=%s devices=%d target=%.2fMW",
		req.RequestID, len(req.DeviceIDs), req.TargetReductionMW)

	resp := s.engine.EmergencyShutdown(req)

	log.Printf("[grpc] EmergencyShutdown response: id=%s success=%v devices=%d reduction=%.2fMW",
		resp.RequestID, resp.Success, resp.DeviceCount, resp.ActualReductionMW)

	return resp, nil
}

func (s *DispatchServer) Stop() {
	if s.server != nil {
		stopped := make(chan struct{})
		go func() {
			s.server.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			s.server.Stop()
		}
	}
}

func (s *DispatchServer) Stats() (requests int64, errors int64, uptime time.Duration) {
	return s.reqCount.Load(), s.errCount.Load(), time.Since(s.startTime)
}

func (s *DispatchServer) InterceptorStats() (requests int64, activeStreams int64, panics int64, rejects int64, timeouts int64) {
	return s.interceptor.Stats()
}
