package proto

type SheddingStrategy int32

const (
	StrategyGreedy        SheddingStrategy = 0
	StrategyCostOptimal   SheddingStrategy = 1
	StrategySpeedPriority SheddingStrategy = 2
)

func (s SheddingStrategy) String() string {
	switch s {
	case StrategyGreedy:
		return "GREEDY"
	case StrategyCostOptimal:
		return "COST_OPTIMAL"
	case StrategySpeedPriority:
		return "SPEED_PRIORITY"
	default:
		return "UNKNOWN"
	}
}

type LoadSheddingRequest struct {
	RequestID        string           `json:"request_id"`
	TargetReductionMW float64         `json:"target_reduction_mw"`
	DeadlineMS       int64            `json:"deadline_ms"`
	Strategy         SheddingStrategy `json:"strategy"`
	ExcludedDeviceIDs []string        `json:"excluded_device_ids"`
	MaxCostPerMW     float64          `json:"max_cost_per_mw"`
}

type LoadSheddingResponse struct {
	RequestID          string                `json:"request_id"`
	Success            bool                  `json:"success"`
	ActualReductionMW  float64               `json:"actual_reduction_mw"`
	DeviceCount        int32                 `json:"device_count"`
	Commands           []*DeviceControlCommand `json:"commands"`
	ComputeTimeUS      int64                 `json:"compute_time_us"`
	Message            string                `json:"message"`
	RemainingGapMW     float64               `json:"remaining_gap_mw"`
}

type DeviceControlCommand struct {
	DeviceID       string  `json:"device_id"`
	DeviceType     string  `json:"device_type"`
	CurrentLoadMW  float64 `json:"current_load_mw"`
	TargetLoadMW   float64 `json:"target_load_mw"`
	ReductionMW    float64 `json:"reduction_mw"`
	ResponseTimeMS float64 `json:"response_time_ms"`
	CostPerMW      float64 `json:"cost_per_mw"`
	Priority       int32   `json:"priority"`
	ControlAction  string  `json:"control_action"`
}

type FleetStatusRequest struct {
	DeviceIDs []string `json:"device_ids"`
}

type FleetStatusResponse struct {
	Devices          []*DeviceStatus `json:"devices"`
	TotalCapacityMW  float64         `json:"total_capacity_mw"`
	TotalAvailableMW float64         `json:"total_available_mw"`
	DeviceCount      int32           `json:"device_count"`
}

type DeviceStatus struct {
	DeviceID       string  `json:"device_id"`
	DeviceType     string  `json:"device_type"`
	CurrentLoadMW  float64 `json:"current_load_mw"`
	MaxReductionMW float64 `json:"max_reduction_mw"`
	ResponseTimeMS float64 `json:"response_time_ms"`
	CostPerMW      float64 `json:"cost_per_mw"`
	SOC            float64 `json:"soc"`
	Online         bool    `json:"online"`
}

type EmergencyShutdownRequest struct {
	RequestID        string   `json:"request_id"`
	DeviceIDs        []string `json:"device_ids"`
	TargetReductionMW float64 `json:"target_reduction_mw"`
}

type EmergencyShutdownResponse struct {
	RequestID          string                `json:"request_id"`
	Success            bool                  `json:"success"`
	DeviceCount        int32                 `json:"device_count"`
	ActualReductionMW  float64               `json:"actual_reduction_mw"`
	Commands           []*DeviceControlCommand `json:"commands"`
}
