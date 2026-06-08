package dispatcher

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	proto "github.com/vpp/dispatch-engine/api/proto"
	"github.com/vpp/dispatch-engine/internal/redispool"
)

type FlexibleLoad struct {
	DeviceID       string
	DeviceType     string
	CurrentLoadMW  float64
	MaxReductionMW float64
	ResponseTimeMS float64
	CostPerMW      float64
	SOC            float64
	Priority       int
	Online         bool
}

type DispatchResult struct {
	Commands          []*proto.DeviceControlCommand
	TotalReductionMW  float64
	DeviceCount       int
	ComputeTimeUS     int64
	Feasible          bool
	RemainingGapMW    float64
}

type Engine struct {
	redisPool *redispool.Pool
	mu        sync.RWMutex
	loads     map[string]*FlexibleLoad
}

func NewEngine(pool *redispool.Pool) *Engine {
	eng := &Engine{
		redisPool: pool,
		loads:     make(map[string]*FlexibleLoad),
	}

	go eng.syncLoop()

	return eng
}

func (e *Engine) RegisterLoad(load *FlexibleLoad) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loads[load.DeviceID] = load
}

func (e *Engine) RemoveLoad(deviceID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.loads, deviceID)
}

func (e *Engine) GetLoad(deviceID string) (*FlexibleLoad, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	l, ok := e.loads[deviceID]
	return l, ok
}

func (e *Engine) GetAllLoads() []*FlexibleLoad {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]*FlexibleLoad, 0, len(e.loads))
	for _, l := range e.loads {
		result = append(result, l)
	}
	return result
}

func (e *Engine) LoadShedding(req *proto.LoadSheddingRequest) *proto.LoadSheddingResponse {
	start := time.Now()

	result := e.greedyDispatch(req.TargetReductionMW, req.ExcludedDeviceIDs, req.MaxCostPerMW, req.Strategy)

	elapsed := time.Since(start)

	resp := &proto.LoadSheddingResponse{
		RequestID:         req.RequestID,
		Success:           result.Feasible,
		ActualReductionMW: result.TotalReductionMW,
		DeviceCount:       int32(result.DeviceCount),
		Commands:          result.Commands,
		ComputeTimeUS:     elapsed.Microseconds(),
		RemainingGapMW:    result.RemainingGapMW,
	}

	if result.Feasible {
		resp.Message = fmt.Sprintf("successfully shed %.2f MW across %d devices", result.TotalReductionMW, result.DeviceCount)
	} else {
		resp.Message = fmt.Sprintf("partial shed %.2f/%.2f MW, gap %.2f MW remains",
			result.TotalReductionMW, req.TargetReductionMW, result.RemainingGapMW)
	}

	return resp
}

func (e *Engine) greedyDispatch(targetMW float64, excludedIDs []string, maxCostPerMW float64, strategy proto.SheddingStrategy) *DispatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	excluded := make(map[string]bool)
	for _, id := range excludedIDs {
		excluded[id] = true
	}

	candidates := make([]*FlexibleLoad, 0, len(e.loads))
	for _, load := range e.loads {
		if !load.Online {
			continue
		}
		if excluded[load.DeviceID] {
			continue
		}
		if load.MaxReductionMW <= 0 {
			continue
		}
		if maxCostPerMW > 0 && load.CostPerMW > maxCostPerMW {
			continue
		}
		candidates = append(candidates, load)
	}

	switch strategy {
	case proto.StrategyCostOptimal:
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].CostPerMW < candidates[j].CostPerMW
		})
	case proto.StrategySpeedPriority:
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].ResponseTimeMS < candidates[j].ResponseTimeMS
		})
	default:
		sort.Slice(candidates, func(i, j int) bool {
			return greedyScore(candidates[i]) > greedyScore(candidates[j])
		})
	}

	var commands []*proto.DeviceControlCommand
	accumulated := 0.0

	for _, load := range candidates {
		if accumulated >= targetMW {
			break
		}

		remaining := targetMW - accumulated
		reduction := math.Min(load.MaxReductionMW, remaining)

		if reduction <= 0 {
			continue
		}

		cmd := &proto.DeviceControlCommand{
			DeviceID:       load.DeviceID,
			DeviceType:     load.DeviceType,
			CurrentLoadMW:  load.CurrentLoadMW,
			TargetLoadMW:   load.CurrentLoadMW - reduction,
			ReductionMW:    reduction,
			ResponseTimeMS: load.ResponseTimeMS,
			CostPerMW:      load.CostPerMW,
			Priority:       int32(load.Priority),
			ControlAction:  determineControlAction(load, reduction),
		}

		commands = append(commands, cmd)
		accumulated += reduction
	}

	gap := targetMW - accumulated
	if gap < 0 {
		gap = 0
	}

	return &DispatchResult{
		Commands:         commands,
		TotalReductionMW: accumulated,
		DeviceCount:      len(commands),
		Feasible:         accumulated >= targetMW,
		RemainingGapMW:   gap,
	}
}

func greedyScore(load *FlexibleLoad) float64 {
	if load.CostPerMW <= 0 || load.ResponseTimeMS <= 0 {
		return load.MaxReductionMW * 1000
	}

	costEfficiency := 1.0 / load.CostPerMW
	speedEfficiency := 1.0 / load.ResponseTimeMS
	capacityWeight := load.MaxReductionMW
	priorityBonus := 1.0 / float64(load.Priority)

	score := capacityWeight * (0.4*costEfficiency + 0.3*speedEfficiency + 0.3*priorityBonus)

	if load.DeviceType == "battery" && load.SOC > 20 {
		score *= 1.2
	} else if load.DeviceType == "hvac" {
		score *= 1.1
	}

	return score
}

func determineControlAction(load *FlexibleLoad, reductionMW float64) string {
	ratio := reductionMW / load.MaxReductionMW

	switch load.DeviceType {
	case "hvac":
		switch {
		case ratio >= 0.9:
			return "HVAC_SHUTDOWN"
		case ratio >= 0.5:
			return "HVAC_SETPOINT_RAISE"
		default:
			return "HVAC_LOAD_LIMIT"
		}
	case "ev_charger":
		switch {
		case ratio >= 0.9:
			return "EV_CHARGE_PAUSE"
		case ratio >= 0.5:
			return "EV_CHARGE_REDUCE_50"
		default:
			return "EV_CHARGE_REDUCE_25"
		}
	case "battery":
		if reductionMW > 0 {
			return "BATTERY_DISCHARGE_INCREASE"
		}
		return "BATTERY_IDLE"
	case "industrial":
		switch {
		case ratio >= 0.9:
			return "INDUSTRIAL_SHUTDOWN"
		default:
			return "INDUSTRIAL_LOAD_REDUCE"
		}
	default:
		if ratio >= 0.9 {
			return "DEVICE_SHUTDOWN"
		}
		return "DEVICE_LOAD_REDUCE"
	}
}

func (e *Engine) EmergencyShutdown(req *proto.EmergencyShutdownRequest) *proto.EmergencyShutdownResponse {
	e.mu.Lock()
	defer e.mu.Unlock()

	var commands []*proto.DeviceControlCommand
	totalReduction := 0.0

	targetIDs := req.DeviceIDs
	if len(targetIDs) == 0 {
		for id := range e.loads {
			targetIDs = append(targetIDs, id)
		}
	}

	for _, id := range targetIDs {
		load, ok := e.loads[id]
		if !ok || !load.Online {
			continue
		}

		cmd := &proto.DeviceControlCommand{
			DeviceID:       load.DeviceID,
			DeviceType:     load.DeviceType,
			CurrentLoadMW:  load.CurrentLoadMW,
			TargetLoadMW:   0,
			ReductionMW:    load.CurrentLoadMW,
			ResponseTimeMS: load.ResponseTimeMS,
			CostPerMW:      load.CostPerMW,
			Priority:       int32(load.Priority),
			ControlAction:  "EMERGENCY_SHUTDOWN",
		}

		commands = append(commands, cmd)
		totalReduction += load.CurrentLoadMW
	}

	return &proto.EmergencyShutdownResponse{
		RequestID:         req.RequestID,
		Success:           true,
		DeviceCount:       int32(len(commands)),
		ActualReductionMW: totalReduction,
		Commands:          commands,
	}
}

func (e *Engine) FleetStatus(req *proto.FleetStatusRequest) *proto.FleetStatusResponse {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var devices []*proto.DeviceStatus
	totalCapacity := 0.0
	totalAvailable := 0.0

	targetIDs := req.DeviceIDs
	if len(targetIDs) == 0 {
		for id := range e.loads {
			targetIDs = append(targetIDs, id)
		}
	}

	for _, id := range targetIDs {
		load, ok := e.loads[id]
		if !ok || !load.Online {
			continue
		}

		status := &proto.DeviceStatus{
			DeviceID:       load.DeviceID,
			DeviceType:     load.DeviceType,
			CurrentLoadMW:  load.CurrentLoadMW,
			MaxReductionMW: load.MaxReductionMW,
			ResponseTimeMS: load.ResponseTimeMS,
			CostPerMW:      load.CostPerMW,
			SOC:            load.SOC,
			Online:         load.Online,
		}

		devices = append(devices, status)
		totalCapacity += load.CurrentLoadMW
		totalAvailable += load.MaxReductionMW
	}

	return &proto.FleetStatusResponse{
		Devices:          devices,
		TotalCapacityMW:  totalCapacity,
		TotalAvailableMW: totalAvailable,
		DeviceCount:      int32(len(devices)),
	}
}

func (e *Engine) syncLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if e.redisPool == nil {
			continue
		}

		ctx := context.Background()
		nodes, err := e.redisPool.GetAllNodes(ctx)
		if err != nil {
			continue
		}

		e.mu.Lock()
		for nodeID, state := range nodes {
			load, ok := e.loads[nodeID]
			if !ok {
				load = &FlexibleLoad{
					DeviceID:   nodeID,
					DeviceType: inferDeviceType(nodeID),
					Online:     true,
				}
				e.loads[nodeID] = load
			}

			load.CurrentLoadMW = state.ActivePower
			load.SOC = state.SOC
			load.MaxReductionMW = calculateReductionPotential(load)

			if state.ActivePower == 0 && state.SOC == 0 {
				load.Online = false
			} else {
				load.Online = true
			}
		}
		e.mu.Unlock()
	}
}

func inferDeviceType(nodeID string) string {
	switch {
	case len(nodeID) >= 4 && nodeID[:4] == "HVAC":
		return "hvac"
	case len(nodeID) >= 2 && nodeID[:2] == "EV":
		return "ev_charger"
	case len(nodeID) >= 3 && nodeID[:3] == "BAT":
		return "battery"
	case len(nodeID) >= 3 && nodeID[:3] == "IND":
		return "industrial"
	default:
		return "generic"
	}
}

func calculateReductionPotential(load *FlexibleLoad) float64 {
	switch load.DeviceType {
	case "hvac":
		return load.CurrentLoadMW * 0.6
	case "ev_charger":
		return load.CurrentLoadMW * 0.9
	case "battery":
		if load.SOC > 10 {
			return load.CurrentLoadMW * 0.8
		}
		return 0
	case "industrial":
		return load.CurrentLoadMW * 0.3
	default:
		return load.CurrentLoadMW * 0.4
	}
}
