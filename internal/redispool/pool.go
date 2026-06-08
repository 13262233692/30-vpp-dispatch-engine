package redispool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/vpp/dispatch-engine/internal/mms"
)

type Pool struct {
	client       *redis.Client
	ctx          context.Context
	cancel       context.CancelFunc
	batchSize    int
	flushInterval time.Duration
	mu           sync.Mutex
	buffer       []*mms.SubstationData
	writeCount   int64
	errorCount   int64
}

type Config struct {
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	BatchSize    int
	FlushInterval time.Duration
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

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	})

	ctx, cancel := context.WithCancel(context.Background())

	if err := client.Ping(ctx).Err(); err != nil {
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
	}

	go p.flushLoop()

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
		go p.flush()
	}
	return nil
}

func (p *Pool) flushLoop() {
	ticker := time.NewTicker(p.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
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

	pipe := p.client.Pipeline()
	now := time.Now()

	for _, data := range batch {
		for nodeID, meas := range data.Measurements {
			key := fmt.Sprintf("vpp:node:%s", nodeID)

			measJSON, err := json.Marshal(map[string]interface{}{
				"node_id":       meas.NodeID,
				"active_power":  meas.ActivePower,
				"reactive_power": meas.ReactivePower,
				"soc":           meas.SOC,
				"voltage":       meas.Voltage,
				"current":       meas.Current,
				"frequency":     meas.Frequency,
				"timestamp":     meas.Timestamp.UnixMilli(),
				"received_at":   now.UnixMilli(),
			})
			if err != nil {
				p.errorCount++
				continue
			}

			pipe.Set(p.ctx, key, measJSON, 30*time.Second)
			pipe.ZAdd(p.ctx, "vpp:nodes:timeline", redis.Z{
				Score:  float64(now.UnixMilli()),
				Member: nodeID,
			})
		}

		if data.IEDName != "" {
			iedKey := fmt.Sprintf("vpp:ied:%s", data.IEDName)
			pipe.SAdd(p.ctx, "vpp:ieds", data.IEDName)
			pipe.Set(p.ctx, iedKey+":last_seen", now.UnixMilli(), 30*time.Second)
		}
	}

	_, err := pipe.Exec(p.ctx)
	if err != nil {
		log.Printf("[redispool] batch write error: %v", err)
		p.errorCount++
	} else {
		p.writeCount += int64(len(batch))
	}
}

func (p *Pool) GetAllNodes(ctx context.Context) (map[string]*NodeState, error) {
	keys, err := p.client.Keys(ctx, "vpp:node:*").Result()
	if err != nil {
		return nil, fmt.Errorf("redispool: failed to list nodes: %w", err)
	}

	result := make(map[string]*NodeState, len(keys))
	for _, key := range keys {
		val, err := p.client.Get(ctx, key).Result()
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
	key := fmt.Sprintf("vpp:node:%s", nodeID)
	val, err := p.client.Get(ctx, key).Result()
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

func (p *Pool) Stats() (writes int64, errors int64) {
	return p.writeCount, p.errorCount
}

type NodeState struct {
	NodeID       string
	ActivePower  float64
	ReactivePower float64
	SOC          float64
	Voltage      float64
	Current      float64
}
