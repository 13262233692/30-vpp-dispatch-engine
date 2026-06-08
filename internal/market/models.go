package market

import "time"

type LMPData struct {
	NodeID    string
	Timestamp time.Time
	LMP       float64
	Energy    float64
	Congestion float64
	Loss      float64
}

type LMPForecast struct {
	NodeID  string
	Hourly  [24]float64
	FetchedAt time.Time
}

type WeatherForecast struct {
	Temperature  [24]float64
	SolarIrradiance [24]float64
	WindSpeed    [24]float64
	CloudCover   [24]float64
	Humidity     [24]float64
	FetchedAt    time.Time
}

type BatteryEconomics struct {
	DeviceID           string
	CapacityMWh       float64
	CurrentSOC         float64
	MinSOC             float64
	MaxSOC             float64
	ChargeRateMW       float64
	DischargeRateMW    float64
	RampRateMWPerMin   float64
	RoundTripEfficiency float64
	CycleLife          int
	ReplacementCost    float64
	DegradationCostPerCycle float64
	InitialInvestment  float64
}

type FlexibleLoadEconomics struct {
	DeviceID       string
	DeviceType     string
	MaxLoadMW      float64
	MinLoadMW      float64
	RampRateMWPerMin float64
	CurtailmentCostPerMWh float64
	Availability   [24]bool
}

type VPPEconomics struct {
	Batteries     []BatteryEconomics
	FlexibleLoads []FlexibleLoadEconomics
	NetworkLossRate float64
	TradingFeeRate  float64
}

type PriceQuantityPair struct {
	Price    float64
	Quantity float64
}

type HourlyBid struct {
	Hour            int
	TradingPeriod   string
	SellPQ          []PriceQuantityPair
	BuyPQ           []PriceQuantityPair
	ExpectedRevenue float64
	ExpectedCost    float64
	NetProfit       float64
}

type DailyBidPlan struct {
	BiddingDate   string
	NodeID        string
	HourlyBids    [24]*HourlyBid
	TotalRevenue  float64
	TotalCost     float64
	TotalProfit   float64
	SolvedAt      time.Time
	SolveTimeMS   int64
	Feasible      bool
}

type OptimizationResult struct {
	Plan            *DailyBidPlan
	BatterySchedule [24]map[string]BatteryScheduleEntry
	LoadSchedule    [24]map[string]LoadScheduleEntry
	ShadowPrices    [24]float64
}

type BatteryScheduleEntry struct {
	DeviceID    string
	ChargeMW    float64
	DischargeMW float64
	SOCAfter    float64
	Degradation float64
}

type LoadScheduleEntry struct {
	DeviceID     string
	ScheduledMW  float64
	CurtailmentMW float64
	Cost         float64
}

type BidSubmissionRequest struct {
	VPPID        string            `json:"vpp_id"`
	BiddingDate  string            `json:"bidding_date"`
	NodeID       string            `json:"node_id"`
	HourlyBids   []HourlyBidJSON   `json:"hourly_bids"`
	Signature    string            `json:"signature"`
	Timestamp    int64             `json:"timestamp"`
}

type HourlyBidJSON struct {
	Hour   int               `json:"hour"`
	SellPQ []PQJSON          `json:"sell_pq"`
	BuyPQ  []PQJSON          `json:"buy_pq"`
}

type PQJSON struct {
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
}

type BidSubmissionResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	BidID     string `json:"bid_id"`
	Timestamp int64  `json:"timestamp"`
}
