package dispatcher

import (
	"testing"

	proto "github.com/vpp/dispatch-engine/api/proto"
)

func seedEngine() *Engine {
	eng := &Engine{
		loads: make(map[string]*FlexibleLoad),
	}

	loads := []*FlexibleLoad{
		{DeviceID: "BAT-01", DeviceType: "battery", CurrentLoadMW: 10, MaxReductionMW: 8, ResponseTimeMS: 100, CostPerMW: 30, SOC: 85, Priority: 1, Online: true},
		{DeviceID: "BAT-02", DeviceType: "battery", CurrentLoadMW: 6, MaxReductionMW: 5, ResponseTimeMS: 150, CostPerMW: 35, SOC: 72, Priority: 1, Online: true},
		{DeviceID: "HVAC-01", DeviceType: "hvac", CurrentLoadMW: 15, MaxReductionMW: 9, ResponseTimeMS: 2000, CostPerMW: 50, SOC: 0, Priority: 3, Online: true},
		{DeviceID: "EV-01", DeviceType: "ev_charger", CurrentLoadMW: 8, MaxReductionMW: 7.2, ResponseTimeMS: 500, CostPerMW: 80, SOC: 65, Priority: 4, Online: true},
		{DeviceID: "IND-01", DeviceType: "industrial", CurrentLoadMW: 25, MaxReductionMW: 7.5, ResponseTimeMS: 5000, CostPerMW: 120, SOC: 0, Priority: 5, Online: true},
		{DeviceID: "OFFLINE-01", DeviceType: "hvac", CurrentLoadMW: 5, MaxReductionMW: 3, ResponseTimeMS: 1000, CostPerMW: 40, SOC: 0, Priority: 2, Online: false},
	}

	for _, l := range loads {
		eng.RegisterLoad(l)
	}

	return eng
}

func TestLoadSheddingGreedy(t *testing.T) {
	eng := seedEngine()

	req := &proto.LoadSheddingRequest{
		RequestID:        "test-001",
		TargetReductionMW: 20.0,
		Strategy:         proto.StrategyGreedy,
	}

	resp := eng.LoadShedding(req)

	if !resp.Success {
		t.Errorf("expected success, got: %s", resp.Message)
	}
	if resp.ActualReductionMW < 20.0 {
		t.Errorf("expected >= 20MW reduction, got %.2f", resp.ActualReductionMW)
	}
	if resp.DeviceCount == 0 {
		t.Error("expected at least 1 device")
	}

	t.Logf("LoadShedding: reduction=%.2fMW, devices=%d, compute=%dus",
		resp.ActualReductionMW, resp.DeviceCount, resp.ComputeTimeUS)
	for _, cmd := range resp.Commands {
		t.Logf("  - %s: %.2f -> %.2f MW (reduce %.2f MW, action=%s)",
			cmd.DeviceID, cmd.CurrentLoadMW, cmd.TargetLoadMW, cmd.ReductionMW, cmd.ControlAction)
	}
}

func TestLoadSheddingPartial(t *testing.T) {
	eng := seedEngine()

	req := &proto.LoadSheddingRequest{
		RequestID:        "test-002",
		TargetReductionMW: 100.0,
		Strategy:         proto.StrategyGreedy,
	}

	resp := eng.LoadShedding(req)

	if resp.Success {
		t.Error("should not succeed with 100MW target when only ~36.7MW available")
	}
	if resp.ActualReductionMW <= 0 {
		t.Error("should have partial reduction")
	}
	if resp.RemainingGapMW <= 0 {
		t.Error("should have remaining gap")
	}

	t.Logf("Partial: reduction=%.2fMW, gap=%.2fMW", resp.ActualReductionMW, resp.RemainingGapMW)
}

func TestLoadSheddingCostOptimal(t *testing.T) {
	eng := seedEngine()

	req := &proto.LoadSheddingRequest{
		RequestID:        "test-003",
		TargetReductionMW: 15.0,
		Strategy:         proto.StrategyCostOptimal,
	}

	resp := eng.LoadShedding(req)

	if !resp.Success {
		t.Errorf("expected success: %s", resp.Message)
	}

	var totalCost float64
	for _, cmd := range resp.Commands {
		totalCost += cmd.ReductionMW * cmd.CostPerMW
	}

	t.Logf("CostOptimal: reduction=%.2fMW, totalCost=%.2f, devices=%d",
		resp.ActualReductionMW, totalCost, resp.DeviceCount)
}

func TestLoadSheddingSpeedPriority(t *testing.T) {
	eng := seedEngine()

	req := &proto.LoadSheddingRequest{
		RequestID:        "test-004",
		TargetReductionMW: 15.0,
		Strategy:         proto.StrategySpeedPriority,
	}

	resp := eng.LoadShedding(req)

	if !resp.Success {
		t.Errorf("expected success: %s", resp.Message)
	}

	if len(resp.Commands) > 0 {
		if resp.Commands[0].ResponseTimeMS > 500 {
			t.Errorf("speed priority should pick fastest devices first, got %vms", resp.Commands[0].ResponseTimeMS)
		}
	}

	t.Logf("SpeedPriority: reduction=%.2fMW, devices=%d", resp.ActualReductionMW, resp.DeviceCount)
}

func TestLoadSheddingExclusion(t *testing.T) {
	eng := seedEngine()

	req := &proto.LoadSheddingRequest{
		RequestID:         "test-005",
		TargetReductionMW: 20.0,
		Strategy:          proto.StrategyGreedy,
		ExcludedDeviceIDs: []string{"BAT-01", "BAT-02"},
	}

	resp := eng.LoadShedding(req)

	for _, cmd := range resp.Commands {
		if cmd.DeviceID == "BAT-01" || cmd.DeviceID == "BAT-02" {
			t.Error("excluded devices should not appear in commands")
		}
	}

	t.Logf("Exclusion: reduction=%.2fMW, devices=%d", resp.ActualReductionMW, resp.DeviceCount)
}

func TestLoadSheddingMaxCost(t *testing.T) {
	eng := seedEngine()

	req := &proto.LoadSheddingRequest{
		RequestID:        "test-006",
		TargetReductionMW: 20.0,
		Strategy:         proto.StrategyGreedy,
		MaxCostPerMW:     60,
	}

	resp := eng.LoadShedding(req)

	for _, cmd := range resp.Commands {
		if cmd.CostPerMW > 60 {
			t.Errorf("command cost %.2f exceeds max 60", cmd.CostPerMW)
		}
	}

	t.Logf("MaxCost: reduction=%.2fMW, devices=%d", resp.ActualReductionMW, resp.DeviceCount)
}

func TestEmergencyShutdown(t *testing.T) {
	eng := seedEngine()

	req := &proto.EmergencyShutdownRequest{
		RequestID:        "emergency-001",
		DeviceIDs:        []string{"BAT-01", "HVAC-01"},
		TargetReductionMW: 25.0,
	}

	resp := eng.EmergencyShutdown(req)

	if !resp.Success {
		t.Error("emergency shutdown should succeed")
	}
	if resp.DeviceCount != 2 {
		t.Errorf("expected 2 devices, got %d", resp.DeviceCount)
	}
	if resp.ActualReductionMW != 25.0 {
		t.Errorf("expected 25MW total reduction, got %.2f", resp.ActualReductionMW)
	}

	for _, cmd := range resp.Commands {
		if cmd.ControlAction != "EMERGENCY_SHUTDOWN" {
			t.Errorf("expected EMERGENCY_SHUTDOWN action, got %s", cmd.ControlAction)
		}
		if cmd.TargetLoadMW != 0 {
			t.Errorf("emergency shutdown should set target to 0, got %.2f", cmd.TargetLoadMW)
		}
	}
}

func TestFleetStatus(t *testing.T) {
	eng := seedEngine()

	req := &proto.FleetStatusRequest{}

	resp := eng.FleetStatus(req)

	if resp.DeviceCount != 5 {
		t.Errorf("expected 5 online devices, got %d", resp.DeviceCount)
	}
	if resp.TotalCapacityMW <= 0 {
		t.Error("expected positive total capacity")
	}
	if resp.TotalAvailableMW <= 0 {
		t.Error("expected positive total available")
	}

	t.Logf("Fleet: capacity=%.2fMW, available=%.2fMW, devices=%d",
		resp.TotalCapacityMW, resp.TotalAvailableMW, resp.DeviceCount)
}

func TestFleetStatusSpecificDevices(t *testing.T) {
	eng := seedEngine()

	req := &proto.FleetStatusRequest{
		DeviceIDs: []string{"BAT-01", "EV-01"},
	}

	resp := eng.FleetStatus(req)

	if resp.DeviceCount != 2 {
		t.Errorf("expected 2 devices, got %d", resp.DeviceCount)
	}
}

func TestControlActionDetermination(t *testing.T) {
	tests := []struct {
		load      *FlexibleLoad
		reduction float64
		expected  string
	}{
		{&FlexibleLoad{DeviceType: "hvac"}, 9.5, "HVAC_SHUTDOWN"},
		{&FlexibleLoad{DeviceType: "hvac"}, 6.0, "HVAC_SETPOINT_RAISE"},
		{&FlexibleLoad{DeviceType: "hvac"}, 2.0, "HVAC_LOAD_LIMIT"},
		{&FlexibleLoad{DeviceType: "ev_charger"}, 9.5, "EV_CHARGE_PAUSE"},
		{&FlexibleLoad{DeviceType: "ev_charger"}, 6.0, "EV_CHARGE_REDUCE_50"},
		{&FlexibleLoad{DeviceType: "ev_charger"}, 2.0, "EV_CHARGE_REDUCE_25"},
		{&FlexibleLoad{DeviceType: "battery"}, 5.0, "BATTERY_DISCHARGE_INCREASE"},
		{&FlexibleLoad{DeviceType: "industrial"}, 9.5, "INDUSTRIAL_SHUTDOWN"},
		{&FlexibleLoad{DeviceType: "industrial"}, 5.0, "INDUSTRIAL_LOAD_REDUCE"},
	}

	for _, tt := range tests {
		tt.load.MaxReductionMW = 10
		action := determineControlAction(tt.load, tt.reduction)
		if action != tt.expected {
			t.Errorf("device=%s reduction=%.1f: expected %s, got %s",
				tt.load.DeviceType, tt.reduction, tt.expected, action)
		}
	}
}

func TestGreedyScore(t *testing.T) {
	batteryLoad := &FlexibleLoad{DeviceType: "battery", MaxReductionMW: 8, CostPerMW: 30, ResponseTimeMS: 100, SOC: 85, Priority: 1}
	hvacLoad := &FlexibleLoad{DeviceType: "hvac", MaxReductionMW: 9, CostPerMW: 50, ResponseTimeMS: 2000, SOC: 0, Priority: 3}

	batteryScore := greedyScore(batteryLoad)
	hvacScore := greedyScore(hvacLoad)

	if batteryScore <= hvacScore {
		t.Errorf("battery (cost=30, speed=100ms) should score higher than hvac (cost=50, speed=2000ms), got battery=%.2f hvac=%.2f",
			batteryScore, hvacScore)
	}
}
