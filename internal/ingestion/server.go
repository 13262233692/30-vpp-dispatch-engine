package ingestion

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vpp/dispatch-engine/internal/lifecycle"
	"github.com/vpp/dispatch-engine/internal/mms"
	"github.com/vpp/dispatch-engine/internal/redispool"
)

type Server struct {
	addr          string
	listener      net.Listener
	redisPool     *redispool.Pool
	connCount     atomic.Int64
	msgCount      atomic.Int64
	errorCount    atomic.Int64
	rejectCount   atomic.Int64
	mu            sync.Mutex
	connections   map[net.Conn]context.CancelFunc
	cancel        context.CancelFunc
	onMeasurement func(data *mms.SubstationData)
	gm            *lifecycle.GoroutineManager
	connLimiter   *lifecycle.ConnLimiter
	idleTimeout   time.Duration
}

type Config struct {
	ListenAddr string
	RedisPool  *redispool.Pool
	GM         *lifecycle.GoroutineManager
	MaxConns   int
	IdleTimeout time.Duration
}

func NewServer(cfg Config) *Server {
	maxConns := cfg.MaxConns
	if maxConns == 0 {
		maxConns = 500
	}
	idleTimeout := cfg.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = 60 * time.Second
	}

	gm := cfg.GM
	if gm == nil {
		gm = lifecycle.NewGoroutineManager(context.Background())
	}

	return &Server{
		addr:        cfg.ListenAddr,
		redisPool:   cfg.RedisPool,
		connections: make(map[net.Conn]context.CancelFunc),
		gm:          gm,
		connLimiter: lifecycle.NewConnLimiter(maxConns),
		idleTimeout: idleTimeout,
	}
}

func (s *Server) OnMeasurement(fn func(data *mms.SubstationData)) {
	s.onMeasurement = fn
}

func (s *Server) Start(ctx context.Context) error {
	childCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	lc := net.ListenConfig{}
	ln, err := lc.Listen(childCtx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("ingestion: listen failed on %s: %w", s.addr, err)
	}
	s.listener = ln

	log.Printf("[ingestion] TCP listener started on %s (maxConns=%d, idleTimeout=%v)",
		s.addr, s.connLimiter.Max(), s.idleTimeout)

	s.gm.Go(func(gmCtx context.Context) error {
		s.acceptLoop(gmCtx)
		return nil
	}, lifecycle.WithName("ingestion-accept"))

	return nil
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !s.connLimiter.Acquire() {
			s.rejectCount.Add(1)
			log.Printf("[ingestion] connection limit reached (%d), rejecting", s.connLimiter.Active())

			tempConn, err := s.listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			tempConn.SetDeadline(time.Now().Add(5 * time.Second))
			tempConn.Write([]byte("ERROR: connection limit reached\r\n"))
			tempConn.Close()
			continue
		}

		conn, err := s.listener.Accept()
		if err != nil {
			s.connLimiter.Release()
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("[ingestion] accept error: %v", err)
				continue
			}
		}

		connCtx, connCancel := context.WithCancel(ctx)

		s.mu.Lock()
		s.connections[conn] = connCancel
		s.mu.Unlock()
		s.connCount.Add(1)

		log.Printf("[ingestion] new connection from %s (active: %d, limit: %d)",
			conn.RemoteAddr(), s.connLimiter.Active(), s.connLimiter.Max())

		s.gm.Go(func(gmCtx context.Context) error {
			s.handleConnection(connCtx, conn, connCancel)
			return nil
		}, lifecycle.WithName("ingestion-conn:"+conn.RemoteAddr().String()))
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn, connCancel context.CancelFunc) {
	defer func() {
		connCancel()
		conn.Close()
		s.connLimiter.Release()

		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		s.connCount.Add(-1)
	}()

	buf := make([]byte, 0, 65536)
	tmp := make([]byte, 8192)
	lastData := time.Now()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[ingestion] connection %s context cancelled, closing", conn.RemoteAddr())
			return
		default:
		}

		deadline := time.Now().Add(30 * time.Second)
		if err := conn.SetReadDeadline(deadline); err != nil {
			return
		}

		n, err := conn.Read(tmp)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if err == io.EOF {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				if time.Since(lastData) > s.idleTimeout {
					log.Printf("[ingestion] connection %s idle timeout (%v)", conn.RemoteAddr(), s.idleTimeout)
					return
				}
				continue
			}
			log.Printf("[ingestion] read error from %s: %v", conn.RemoteAddr(), err)
			return
		}

		lastData = time.Now()
		buf = append(buf, tmp[:n]...)

		for len(buf) >= 4 {
			tpkt, payload, err := mms.ParseTPKT(buf)
			if err != nil {
				buf = buf[1:]
				s.errorCount.Add(1)
				continue
			}

			frameLen := int(tpkt.Length)
			if len(buf) < frameLen {
				break
			}

			frame := make([]byte, frameLen)
			copy(frame, buf[:frameLen])
			buf = buf[frameLen:]

			select {
			case <-ctx.Done():
				return
			default:
				s.processFrame(payload)
				s.msgCount.Add(1)
			}
		}

		if len(buf) > 32768 {
			log.Printf("[ingestion] buffer overflow for %s, resetting", conn.RemoteAddr())
			buf = buf[:0]
			s.errorCount.Add(1)
		}
	}
}

func (s *Server) processFrame(payload []byte) {
	if len(payload) < 3 {
		return
	}

	_, cotpPayload, err := mms.ParseCOTP(payload)
	if err != nil {
		log.Printf("[ingestion] COTP parse error: %v", err)
		s.errorCount.Add(1)
		return
	}

	data, err := mms.ParseMMSPDU(cotpPayload)
	if err != nil {
		log.Printf("[ingestion] MMS parse error: %v", err)
		s.errorCount.Add(1)
		return
	}

	if s.redisPool != nil {
		if err := s.redisPool.WriteSubstationData(data); err != nil {
			log.Printf("[ingestion] Redis write error: %v", err)
		}
	}

	if s.onMeasurement != nil {
		s.onMeasurement(data)
	}
}

func (s *Server) shutdown() {
	if s.listener != nil {
		s.listener.Close()
	}

	s.mu.Lock()
	for conn, cancel := range s.connections {
		cancel()
		conn.SetDeadline(time.Now())
	}
	s.mu.Unlock()

	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			s.mu.Lock()
			for conn := range s.connections {
				conn.Close()
			}
			s.connections = make(map[net.Conn]context.CancelFunc)
			s.mu.Unlock()
			log.Printf("[ingestion] forced shutdown, remaining connections killed")
			return
		case <-ticker.C:
			if s.connCount.Load() == 0 {
				log.Printf("[ingestion] graceful shutdown complete (msg=%d, err=%d, reject=%d)",
					s.msgCount.Load(), s.errorCount.Load(), s.rejectCount.Load())
				return
			}
		}
	}
}

func (s *Server) Stats() (connections int64, messages int64, errors int64) {
	return s.connCount.Load(), s.msgCount.Load(), s.errorCount.Load()
}

func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.shutdown()
}
