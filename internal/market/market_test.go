package market

import (
	"math"
	"testing"
)

func buildTestVPPEconomics() *VPPEconomics {
	return &VPPEconomics{
		Batteries: []BatteryEconomics{
			{
				DeviceID: "BAT-01", CapacityMWh: 10, CurrentSOC: 0.5, MinSOC: 0.1, MaxSOC: 0.95,
				ChargeRateMW: 5, DischargeRateMW: 5, RampRateMWPerMin: 2,
				RoundTripEfficiency: 0.92, CycleLife: 5000, ReplacementCost: 1000000,
				DegradationCostPerCycle: 200, InitialInvestment: 1000000,
			},
		},
		FlexibleLoads: []FlexibleLoadEconomics{
			{
				DeviceID: "HVAC-01", DeviceType: "hvac", MaxLoadMW: 10, MinLoadMW: 3,
				RampRateMWPerMin: 2, CurtailmentCostPerMWh: 80,
			},
		},
		NetworkLossRate: 0.03,
		TradingFeeRate:  0.02,
	}
}

func buildTestLMPForecast() *LMPForecast {
	fc := &LMPForecast{NodeID: "NODE-TEST"}
	for h := 0; h < 24; h++ {
		switch {
		case h >= 0 && h < 6:
			fc.Hourly[h] = 200
		case h >= 6 && h < 9:
			fc.Hourly[h] = 350
		case h >= 9 && h < 17:
			fc.Hourly[h] = 500
		case h >= 17 && h < 21:
			fc.Hourly[h] = 650
		default:
			fc.Hourly[h] = 300
		}
	}
	return fc
}

func TestBiddingOptimizerBasic(t *testing.T) {
	vppEcon := buildTestVPPEconomics()
	optimizer := NewBiddingOptimizer(vppEcon)

	lmpFC := buildTestLMPForecast()
	optimizer.SetLMPForecast(lmpFC)

	result, err := optimizer.Optimize()
	if err != nil {
		t.Fatalf("optimization failed: %v", err)
	}

	if result.Plan == nil {
		t.Fatal("expected non-nil plan")
	}

	if !result.Plan.Feasible {
		t.Error("expected feasible plan")
	}

	t.Logf("Total Profit: %.2f", result.Plan.TotalProfit)
	t.Logf("Total Revenue: %.2f", result.Plan.TotalRevenue)
	t.Logf("Total Cost: %.2f", result.Plan.TotalCost)
	t.Logf("Solve Time: %dms", result.Plan.SolveTimeMS)

	for h := 0; h < 24; h++ {
		bid := result.Plan.HourlyBids[h]
		if bid == nil {
			continue
		}
		if bid.NetProfit != 0 {
			t.Logf("  Hour %02d: LMP=%.0f, sellPQ=%d, buyPQ=%d, profit=%.2f",
				h, lmpFC.Hourly[h], len(bid.SellPQ), len(bid.BuyPQ), bid.NetProfit)
		}
	}
}

func TestBiddingOptimizerHighPriceDischarge(t *testing.T) {
	vppEcon := buildTestVPPEconomics()
	optimizer := NewBiddingOptimizer(vppEcon)

	fc := &LMPForecast{NodeID: "NODE-TEST"}
	for h := 0; h < 24; h++ {
		if h >= 17 && h < 21 {
			fc.Hourly[h] = 800
		} else {
			fc.Hourly[h] = 100
		}
	}
	optimizer.SetLMPForecast(fc)

	result, err := optimizer.Optimize()
	if err != nil {
		t.Fatalf("optimization failed: %v", err)
	}

	if !result.Plan.Feasible {
		t.Error("expected feasible plan")
	}

	peakDischarge := 0.0
	for h := 17; h < 21; h++ {
		battSched := result.BatterySchedule[h]
		for _, entry := range battSched {
			peakDischarge += entry.DischargeMW
		}
	}

	t.Logf("Peak hours (17-21) total discharge: %.2f MW", peakDischarge)
	t.Logf("Total Profit: %.2f", result.Plan.TotalProfit)

	if peakDischarge <= 0 {
		t.Error("expected battery discharge during high price hours")
	}
}

func TestBiddingOptimizerNoLMP(t *testing.T) {
	vppEcon := buildTestVPPEconomics()
	optimizer := NewBiddingOptimizer(vppEcon)

	_, err := optimizer.Optimize()
	if err == nil {
		t.Error("expected error when LMP forecast is missing")
	}
}

func TestStaircasePQGeneration(t *testing.T) {
	pairs := generateStaircasePQ(50.0, 400.0, 5)

	if len(pairs) != 5 {
		t.Fatalf("expected 5 PQ pairs, got %d", len(pairs))
	}

	for i, pq := range pairs {
		if pq.Price <= 0 {
			t.Errorf("pair %d: price should be positive, got %.2f", i, pq.Price)
		}
		if pq.Quantity <= 0 {
			t.Errorf("pair %d: quantity should be positive, got %.2f", i, pq.Quantity)
		}
	}

	if pairs[0].Price >= pairs[4].Price {
		t.Error("staircase prices should be ascending")
	}

	t.Logf("PQ pairs for 50MW @ 400 yuan/MWh, 5 steps:")
	for i, pq := range pairs {
		t.Logf("  Step %d: Price=%.2f, Quantity=%.2f", i+1, pq.Price, pq.Quantity)
	}
}

func TestStaircasePQZeroQuantity(t *testing.T) {
	pairs := generateStaircasePQ(0, 400, 5)
	if pairs != nil {
		t.Error("expected nil for zero quantity")
	}
}

func TestDataFetcherSyntheticLMP(t *testing.T) {
	fetcher := NewDataFetcher(DataFetcherConfig{
		NodeID: "NODE-TEST",
	})

	fetcher.generateSyntheticLMP()

	fc := fetcher.GetLMPForecast()
	if fc == nil {
		t.Fatal("expected non-nil LMP forecast")
	}

	if fc.NodeID != "NODE-TEST" {
		t.Errorf("expected node NODE-TEST, got %s", fc.NodeID)
	}

	for h := 0; h < 24; h++ {
		if fc.Hourly[h] <= 0 {
			t.Errorf("hour %d: expected positive LMP, got %.2f", h, fc.Hourly[h])
		}
	}

	peakLMP := math.Max(fc.Hourly[17], math.Max(fc.Hourly[18], math.Max(fc.Hourly[19], fc.Hourly[20])))
	valleyLMP := math.Min(fc.Hourly[2], math.Min(fc.Hourly[3], fc.Hourly[4]))

	if peakLMP <= valleyLMP {
		t.Errorf("peak LMP (%.2f) should be > valley LMP (%.2f)", peakLMP, valleyLMP)
	}

	t.Logf("Valley LMP (hour 2-4): %.2f", valleyLMP)
	t.Logf("Peak LMP (hour 17-20): %.2f", peakLMP)
}

func TestDataFetcherSyntheticWeather(t *testing.T) {
	fetcher := NewDataFetcher(DataFetcherConfig{})

	fetcher.generateSyntheticWeather()

	wf := fetcher.GetWeatherForecast()
	if wf == nil {
		t.Fatal("expected non-nil weather forecast")
	}

	if wf.SolarIrradiance[12] <= 0 {
		t.Error("expected positive solar irradiance at noon")
	}
	if wf.SolarIrradiance[0] != 0 {
		t.Error("expected zero solar irradiance at midnight")
	}

	t.Logf("Noon: solar=%.2f, temp=%.1f", wf.SolarIrradiance[12], wf.Temperature[12])
	t.Logf("Midnight: solar=%.2f, temp=%.1f", wf.SolarIrradiance[0], wf.Temperature[0])
}

func TestBidClientDryRun(t *testing.T) {
	client := NewBidClient(BidClientConfig{
		VPPID: "VPP-TEST-001",
	})

	plan := &DailyBidPlan{
		BiddingDate: "2026-06-08",
		NodeID:      "NODE-TEST",
		Feasible:    true,
		TotalProfit: 15000,
	}

	for h := 0; h < 24; h++ {
		plan.HourlyBids[h] = &HourlyBid{
			Hour:      h,
			SellPQ:    []PriceQuantityPair{{Price: 400, Quantity: 5}},
			NetProfit: 625,
		}
	}

	resp, err := client.SubmitBid(nil, plan)
	if err != nil {
		t.Fatalf("dry run submission failed: %v", err)
	}

	if !resp.Success {
		t.Error("expected successful dry run")
	}

	submitted, success, _ := client.Stats()
	if submitted != 1 || success != 1 {
		t.Errorf("expected 1 submit and 1 success, got %d/%d", submitted, success)
	}
}
