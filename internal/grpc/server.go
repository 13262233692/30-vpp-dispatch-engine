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
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type DispatchServiceHandler interface {
	LoadShedding(ctx context.Context, req *proto.LoadSheddingRequest) (*proto.LoadSheddingResponse, error)
	GetFleetStatus(ctx context.Context, req *proto.FleetStatusRequest) (*proto.FleetStatusResponse, error)
	EmergencyShutdown(ctx context.Context, req *proto.EmergencyShutdownRequest) (*proto.EmergencyShutdownResponse, error)
}

type DispatchServer struct {
	engine    *dispatcher.Engine
	server    *grpc.Server
	addr      string
	reqCount  atomic.Int64
	errCount  atomic.Int64
	mu        sync.Mutex
	startTime time.Time
}

type ServerConfig struct {
	ListenAddr string
	Engine     *dispatcher.Engine
}

func NewDispatchServer(cfg ServerConfig) *DispatchServer {
	return &DispatchServer{
		addr:   cfg.ListenAddr,
		engine: cfg.Engine,
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
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 10 * time.Second,
			Time:                  30 * time.Second,
			Timeout:               10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)

	RegisterDispatchService(s.server, s)

	s.startTime = time.Now()

	go func() {
		log.Printf("[grpc] server listening on %s", s.addr)
		if err := s.server.Serve(ln); err != nil {
			log.Printf("[grpc] server error: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		s.server.GracefulStop()
	}()

	return nil
}

func (s *DispatchServer) LoadShedding(ctx context.Context, req *proto.LoadSheddingRequest) (*proto.LoadSheddingResponse, error) {
	s.reqCount.Add(1)
	log.Printf("[grpc] LoadShedding request: id=%s target=%.2fMW strategy=%s",
		req.RequestID, req.TargetReductionMW, req.Strategy)

	resp := s.engine.LoadShedding(req)

	log.Printf("[grpc] LoadShedding response: id=%s success=%v reduction=%.2fMW devices=%d compute=%dus",
		resp.RequestID, resp.Success, resp.ActualReductionMW, resp.DeviceCount, resp.ComputeTimeUS)

	return resp, nil
}

func (s *DispatchServer) GetFleetStatus(ctx context.Context, req *proto.FleetStatusRequest) (*proto.FleetStatusResponse, error) {
	s.reqCount.Add(1)
	resp := s.engine.FleetStatus(req)

	log.Printf("[grpc] FleetStatus: %d devices, capacity=%.2fMW, available=%.2fMW",
		resp.DeviceCount, resp.TotalCapacityMW, resp.TotalAvailableMW)

	return resp, nil
}

func (s *DispatchServer) EmergencyShutdown(ctx context.Context, req *proto.EmergencyShutdownRequest) (*proto.EmergencyShutdownResponse, error) {
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
		s.server.GracefulStop()
	}
}

func (s *DispatchServer) Stats() (requests int64, errors int64, uptime time.Duration) {
	return s.reqCount.Load(), s.errCount.Load(), time.Since(s.startTime)
}
