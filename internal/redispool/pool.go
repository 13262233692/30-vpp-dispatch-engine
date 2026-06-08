package redispool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/vpp/dispatch-engine/internal/lifecycle"
	"github.com/vpp/dispatch-engine/internal/mms"
)

type Pool struct {
	client        *redis.Client
	ctx           context.Context
	cancel        context.CancelFunc
	batchSize     int
	flushInterval time.Duration
	mu            sync.Mutex
	buffer        []*mms.SubstationData
	writeCount    atomic.Int64
	errorCount    atomic.Int64
	droppedCount  atomic.Int64
	gm            *lifecycle.GoroutineManager
	flushSem      chan struct{}
	healthy       atomic.Bool
	writeTimeout  time.Duration
	readTimeout   time.Duration
}

type Config struct {
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	BatchSize    int
	FlushInterval time.Duration
	GM           *lifecycle.GoroutineManager
	WriteTimeout  time.Duration
	ReadTimeout   time.Duration
}

func NewPool(cfg Config) (*Pool, error) {
	if cfg.PoolSize == 0 {
		cfg.PoolSize = 20
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 256
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = 100 * time.Millisecond
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 3 * time.Second
	}

	gm := cfg.GM
	if gm == nil {
		gm = lifecycle.NewGoroutineManager(context.Background())
	}

	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		MinIdleConns: 5,
		MaxRetries:   3,
	})

	ctx, cancel := context.WithCancel(context.Background())

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		cancel()
		return nil, fmt.Errorf("redispool: connect failed: %w", err)
	}

	p := &Pool{
		client:        client,
		ctx:           ctx,
		cancel:        cancel,
		batchSize:     cfg.BatchSize,
		flushInterval: cfg.FlushInterval,
		buffer:        make([]*mms.SubstationData, 0, cfg.BatchSize),
		gm:            gm,
		flushSem:      make(chan struct{}, 4),
		writeTimeout:  cfg.WriteTimeout,
		readTimeout:   cfg.ReadTimeout,
	}

	p.healthy.Store(true)

	gm.Go(func(gmCtx context.Context) error {
		p.flushLoop(gmCtx)
		return nil
	}, lifecycle.WithName("redis-flush"))

	gm.Go(func(gmCtx context.Context) error {
		p.healthCheckLoop(gmCtx)
		return nil
	}, lifecycle.WithName("redis-health"))

	log.Printf("[redispool] connected to %s (pool=%d, batch=%d, flush=%v)",
		cfg.Addr, cfg.PoolSize, cfg.BatchSize, cfg.FlushInterval)

	return p, nil
}

func (p *Pool) WriteSubstationData(data *mms.SubstationData) error {
	p.mu.Lock()
	p.buffer = append(p.buffer, data)
	shouldFlush := len(p.buffer) >= p.batchSize
	p.mu.Unlock()

	if shouldFlush {
		select {
		case p.flushSem <- struct{}{}:
			p.gm.Go(func(gmCtx context.Context) error {
				defer func() { <-p.flushSem }()
				p.flush()
				return nil
			}, lifecycle.WithName("redis-flush-urgent"))
		default:
			p.droppedCount.Add(1)
		}
	}
	return nil
}

func (p *Pool) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(p.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.flush()
			return
		case <-ticker.C:
			p.flush()
		}
	}
}

func (p *Pool) flush() {
	p.mu.Lock()
	if len(p.buffer) == 0 {
		p.mu.Unlock()
		return
	}
	batch := p.buffer
	p.buffer = make([]*mms.SubstationData, 0, p.batchSize)
	p.mu.Unlock()

	flushCtx, flushCancel := context.WithTimeout(p.ctx, p.writeTimeout)
	defer flushCancel()

	pipe := p.client.Pipeline()
	now := time.Now()

	for _, data := range batch {
		for nodeID, meas := range data.Measurements {
			key := fmt.Sprintf("vpp:node:%s", nodeID)

			measJSON, err := json.Marshal(map[string]interface{}{
				"node_id":        meas.NodeID,
				"active_power":   meas.ActivePower,
				"reactive_power": meas.ReactivePower,
				"soc":            meas.SOC,
				"voltage":        meas.Voltage,
				"current":        meas.Current,
				"frequency":      meas.Frequency,
				"timestamp":      meas.Timestamp.UnixMilli(),
				"received_at":    now.UnixMilli(),
			})
			if err != nil {
				p.errorCount.Add(1)
				continue
			}

			pipe.Set(flushCtx, key, measJSON, 30*time.Second)
			pipe.ZAdd(flushCtx, "vpp:nodes:timeline", redis.Z{
				Score:  float64(now.UnixMilli()),
				Member: nodeID,
			})
		}

		if data.IEDName != "" {
			iedKey := fmt.Sprintf("vpp:ied:%s", data.IEDName)
			pipe.SAdd(flushCtx, "vpp:ieds", data.IEDName)
			pipe.Set(flushCtx, iedKey+":last_seen", now.UnixMilli(), 30*time.Second)
		}
	}

	_, err := pipe.Exec(flushCtx)
	if err != nil {
		log.Printf("[redispool] batch write error: %v", err)
		p.errorCount.Add(1)
		p.healthy.Store(false)
	} else {
		p.writeCount.Add(int64(len(batch)))
	}
}

func (p *Pool) healthCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
			err := p.client.Ping(pingCtx).Err()
			pingCancel()

			if err != nil {
				if p.healthy.Load() {
					log.Printf("[redispool] health check FAILED: %v", err)
				}
				p.healthy.Store(false)
			} else {
				if !p.healthy.Load() {
					log.Printf("[redispool] health check recovered")
				}
				p.healthy.Store(true)
			}
		}
	}
}

func (p *Pool) IsHealthy() bool {
	return p.healthy.Load()
}

func (p *Pool) GetAllNodes(ctx context.Context) (map[string]*NodeState, error) {
	if !p.healthy.Load() {
		return nil, fmt.Errorf("redispool: unhealthy")
	}

	queryCtx, cancel := context.WithTimeout(ctx, p.readTimeout)
	defer cancel()

	keys, err := p.client.Keys(queryCtx, "vpp:node:*").Result()
	if err != nil {
		p.healthy.Store(false)
		return nil, fmt.Errorf("redispool: failed to list nodes: %w", err)
	}

	result := make(map[string]*NodeState, len(keys))
	for _, key := range keys {
		val, err := p.client.Get(queryCtx, key).Result()
		if err != nil {
			continue
		}

		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(val), &raw); err != nil {
			continue
		}

		state := &NodeState{}
		if v, ok := raw["node_id"].(string); ok {
			state.NodeID = v
		}
		if v, ok := raw["active_power"].(float64); ok {
			state.ActivePower = v
		}
		if v, ok := raw["reactive_power"].(float64); ok {
			state.ReactivePower = v
		}
		if v, ok := raw["soc"].(float64); ok {
			state.SOC = v
		}
		if v, ok := raw["voltage"].(float64); ok {
			state.Voltage = v
		}
		if v, ok := raw["current"].(float64); ok {
			state.Current = v
		}

		result[state.NodeID] = state
	}

	return result, nil
}

func (p *Pool) GetNode(ctx context.Context, nodeID string) (*NodeState, error) {
	if !p.healthy.Load() {
		return nil, fmt.Errorf("redispool: unhealthy")
	}

	queryCtx, cancel := context.WithTimeout(ctx, p.readTimeout)
	defer cancel()

	key := fmt.Sprintf("vpp:node:%s", nodeID)
	val, err := p.client.Get(queryCtx, key).Result()
	if err != nil {
		return nil, err
	}

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(val), &raw); err != nil {
		return nil, err
	}

	state := &NodeState{}
	if v, ok := raw["node_id"].(string); ok {
		state.NodeID = v
	}
	if v, ok := raw["active_power"].(float64); ok {
		state.ActivePower = v
	}
	if v, ok := raw["reactive_power"].(float64); ok {
		state.ReactivePower = v
	}
	if v, ok := raw["soc"].(float64); ok {
		state.SOC = v
	}

	return state, nil
}

func (p *Pool) Close() error {
	p.cancel()
	p.flush()
	return p.client.Close()
}

func (p *Pool) Stats() (writes int64, errors int64, dropped int64) {
	return p.writeCount.Load(), p.errorCount.Load(), p.droppedCount.Load()
}

type NodeState struct {
	NodeID        string
	ActivePower   float64
	ReactivePower float64
	SOC           float64
	Voltage       float64
	Current       float64
}
