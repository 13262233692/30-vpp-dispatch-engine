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

	"github.com/vpp/dispatch-engine/internal/mms"
	"github.com/vpp/dispatch-engine/internal/redispool"
)

type Server struct {
	addr         string
	listener     net.Listener
	redisPool    *redispool.Pool
	connCount    atomic.Int64
	msgCount     atomic.Int64
	errorCount   atomic.Int64
	mu           sync.Mutex
	connections  map[net.Conn]struct{}
	cancel       context.CancelFunc
	onMeasurement func(data *mms.SubstationData)
}

type Config struct {
	ListenAddr string
	RedisPool  *redispool.Pool
}

func NewServer(cfg Config) *Server {
	return &Server{
		addr:        cfg.ListenAddr,
		redisPool:   cfg.RedisPool,
		connections: make(map[net.Conn]struct{}),
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

	log.Printf("[ingestion] TCP listener started on %s", s.addr)

	go s.acceptLoop(childCtx)

	go func() {
		<-childCtx.Done()
		s.shutdown()
	}()

	return nil
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("[ingestion] accept error: %v", err)
				continue
			}
		}

		s.mu.Lock()
		s.connections[conn] = struct{}{}
		s.mu.Unlock()
		s.connCount.Add(1)

		log.Printf("[ingestion] new connection from %s (active: %d)", conn.RemoteAddr(), s.connCount.Load())

		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		s.connCount.Add(-1)
	}()

	buf := make([]byte, 0, 65536)
	tmp := make([]byte, 8192)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return
		}

		n, err := conn.Read(tmp)
		if err != nil {
			if err == io.EOF {
				log.Printf("[ingestion] connection %s closed by peer", conn.RemoteAddr())
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("[ingestion] read error from %s: %v", conn.RemoteAddr(), err)
			return
		}

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

			s.processFrame(payload)
			s.msgCount.Add(1)
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
	for conn := range s.connections {
		conn.Close()
	}
	s.connections = make(map[net.Conn]struct{})
	s.mu.Unlock()

	log.Printf("[ingestion] server shutdown complete (msg=%d, err=%d)", s.msgCount.Load(), s.errorCount.Load())
}

func (s *Server) Stats() (connections int64, messages int64, errors int64) {
	return s.connCount.Load(), s.msgCount.Load(), s.errorCount.Load()
}

func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}
