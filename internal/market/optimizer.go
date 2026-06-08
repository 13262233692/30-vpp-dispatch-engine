package market

import (
	"fmt"
	"log"
	"math"
	"time"

	"github.com/vpp/dispatch-engine/internal/simplex"
)

type BiddingOptimizer struct {
	vppEcon    *VPPEconomics
	lmpForecast *LMPForecast
	weather     *WeatherForecast
}

func NewBiddingOptimizer(vppEcon *VPPEconomics) *BiddingOptimizer {
	return &BiddingOptimizer{
		vppEcon: vppEcon,
	}
}

func (bo *BiddingOptimizer) SetLMPForecast(fc *LMPForecast) {
	bo.lmpForecast = fc
}

func (bo *BiddingOptimizer) SetWeatherForecast(wf *WeatherForecast) {
	bo.weather = wf
}

func (bo *BiddingOptimizer) Optimize() (*OptimizationResult, error) {
	if bo.lmpForecast == nil {
		return nil, fmt.Errorf("market: LMP forecast not available")
	}
	if bo.vppEcon == nil {
		return nil, fmt.Errorf("market: VPP economics not configured")
	}

	start := time.Now()

	numBatteries := len(bo.vppEcon.Batteries)
	numLoads := len(bo.vppEcon.FlexibleLoads)
	numHours := 24

	battChargeVars := numBatteries * numHours
	battDischargeVars := numBatteries * numHours
	loadVars := numLoads * numHours
	totalVars := battChargeVars + battDischargeVars + loadVars

	builder := simplex.NewBuilder(totalVars)

	objective := make([]float64, totalVars)

	for b := 0; b < numBatteries; b++ {
		batt := bo.vppEcon.Batteries[b]
		degCost := batt.DegradationCostPerCycle / float64(numHours)
		for h := 0; h < numHours; h++ {
			lmp := bo.lmpForecast.Hourly[h]

			dischargeIdx := b*numHours + h
			objective[dischargeIdx] = lmp - degCost

			chargeIdx := battChargeVars + b*numHours + h
			chargeCost := lmp / batt.RoundTripEfficiency + degCost
			objective[chargeIdx] = -chargeCost

			sellPrice := lmp * (1 - bo.vppEcon.TradingFeeRate)
			objective[dischargeIdx] = sellPrice - degCost
		}
	}

	for l := 0; l < numLoads; l++ {
		load := bo.vppEcon.FlexibleLoads[l]
		allAvailable := true
		for h := 0; h < 24; h++ {
			if !load.Availability[h] {
				allAvailable = false
				break
			}
		}
		for h := 0; h < numHours; h++ {
			if !allAvailable && !load.Availability[h] {
				continue
			}
			lmp := bo.lmpForecast.Hourly[h]
			curtailedVal := lmp - load.CurtailmentCostPerMWh
			loadIdx := battChargeVars + battDischargeVars + l*numHours + h
			objective[loadIdx] = curtailedVal
		}
	}

	builder.Maximize(objective)

	for b := 0; b < numBatteries; b++ {
		batt := bo.vppEcon.Batteries[b]
		for h := 0; h < numHours; h++ {
			coeffs := make([]float64, totalVars)

			dischargeIdx := b*numHours + h
			chargeIdx := battChargeVars + b*numHours + h

			coeffs[dischargeIdx] = 1
			coeffs[chargeIdx] = 1

			maxPower := math.Min(batt.DischargeRateMW, batt.ChargeRateMW)
			builder.AddRow(coeffs, simplex.LTE, maxPower)
		}
	}

	for b := 0; b < numBatteries; b++ {
		batt := bo.vppEcon.Batteries[b]
		for h := 0; h < numHours; h++ {
			coeffs := make([]float64, totalVars)

			dischargeIdx := b*numHours + h
			coeffs[dischargeIdx] = batt.DischargeRateMW

			builder.AddRow(coeffs, simplex.LTE, batt.DischargeRateMW)
		}

		for h := 0; h < numHours; h++ {
			coeffs := make([]float64, totalVars)

			chargeIdx := battChargeVars + b*numHours + h
			coeffs[chargeIdx] = batt.ChargeRateMW

			builder.AddRow(coeffs, simplex.LTE, batt.ChargeRateMW)
		}
	}

	for b := 0; b < numBatteries; b++ {
		batt := bo.vppEcon.Batteries[b]

		for h := 0; h < numHours; h++ {
			coeffs := make([]float64, totalVars)

			for k := 0; k <= h; k++ {
				chargeIdx := battChargeVars + b*numHours + k
				dischargeIdx := b*numHours + k

				coeffs[chargeIdx] = batt.ChargeRateMW * batt.RoundTripEfficiency
				coeffs[dischargeIdx] = -batt.DischargeRateMW
			}

			maxSOC := batt.CapacityMWh * (batt.MaxSOC - batt.CurrentSOC)
			builder.AddRow(coeffs, simplex.LTE, maxSOC)
		}

		for h := 0; h < numHours; h++ {
			coeffs := make([]float64, totalVars)

			for k := 0; k <= h; k++ {
				chargeIdx := battChargeVars + b*numHours + k
				dischargeIdx := b*numHours + k

				coeffs[chargeIdx] = -batt.ChargeRateMW * batt.RoundTripEfficiency
				coeffs[dischargeIdx] = batt.DischargeRateMW
			}

			minSOC := batt.CapacityMWh * (batt.CurrentSOC - batt.MinSOC)
			builder.AddRow(coeffs, simplex.LTE, minSOC)
		}
	}

	for b := 0; b < numBatteries; b++ {
		batt := bo.vppEcon.Batteries[b]
		rampPerHour := batt.RampRateMWPerMin * 60

		for h := 1; h < numHours; h++ {
			coeffs := make([]float64, totalVars)

			prevDischargeIdx := b*numHours + (h - 1)
			prevChargeIdx := battChargeVars + b*numHours + (h - 1)
			currDischargeIdx := b*numHours + h
			currChargeIdx := battChargeVars + b*numHours + h

			coeffs[currDischargeIdx] = 1
			coeffs[currChargeIdx] = 1
			coeffs[prevDischargeIdx] = -1
			coeffs[prevChargeIdx] = -1

			builder.AddRow(coeffs, simplex.LTE, rampPerHour)

			coeffs2 := make([]float64, totalVars)
			coeffs2[prevDischargeIdx] = 1
			coeffs2[prevChargeIdx] = 1
			coeffs2[currDischargeIdx] = -1
			coeffs2[currChargeIdx] = -1

			builder.AddRow(coeffs2, simplex.LTE, rampPerHour)
		}
	}

	for l := 0; l < numLoads; l++ {
		load := bo.vppEcon.FlexibleLoads[l]
		allAvailable := true
		for h := 0; h < 24; h++ {
			if !load.Availability[h] {
				allAvailable = false
				break
			}
		}
		for h := 0; h < numHours; h++ {
			coeffs := make([]float64, totalVars)

			loadIdx := battChargeVars + battDischargeVars + l*numHours + h
			coeffs[loadIdx] = 1

			maxQ := load.MaxLoadMW - load.MinLoadMW
			if !allAvailable && !load.Availability[h] {
				maxQ = 0
			}
			builder.AddRow(coeffs, simplex.LTE, maxQ)
		}
	}

	for l := 0; l < numLoads; l++ {
		load := bo.vppEcon.FlexibleLoads[l]
		for h := 1; h < numHours; h++ {
			coeffs := make([]float64, totalVars)

			prevIdx := battChargeVars + battDischargeVars + l*numHours + (h - 1)
			currIdx := battChargeVars + battDischargeVars + l*numHours + h

			coeffs[currIdx] = 1
			coeffs[prevIdx] = -1

			rampPerHour := load.RampRateMWPerMin * 60
			builder.AddRow(coeffs, simplex.LTE, rampPerHour)
		}
	}

	solution := builder.Solve()

	elapsed := time.Since(start)

	result := &OptimizationResult{
		Plan: &DailyBidPlan{
			BiddingDate: time.Now().Format("2006-01-02"),
			NodeID:      bo.lmpForecast.NodeID,
			SolvedAt:    time.Now(),
			SolveTimeMS: elapsed.Milliseconds(),
			Feasible:    solution.Feasible,
		},
	}

	if !solution.Feasible {
		log.Printf("[market] LP optimization infeasible (iterations=%d, unbounded=%v)",
			solution.Iterations, solution.Unbounded)
		return result, fmt.Errorf("market: optimization infeasible")
	}

	totalRevenue := 0.0
	totalCost := 0.0

	for h := 0; h < numHours; h++ {
		bid := &HourlyBid{
			Hour:          h,
			TradingPeriod: fmt.Sprintf("%02d:00-%02d:00", h, h+1),
		}

		sellQty := 0.0
		buyQty := 0.0

		battSchedule := make(map[string]BatteryScheduleEntry)
		for b := 0; b < numBatteries; b++ {
			batt := bo.vppEcon.Batteries[b]

			chargeIdx := battChargeVars + b*numHours + h
			dischargeIdx := b*numHours + h

			chargeMW := 0.0
			dischargeMW := 0.0
			if chargeIdx < len(solution.Variables) {
				chargeMW = math.Max(0, solution.Variables[chargeIdx])
			}
			if dischargeIdx < len(solution.Variables) {
				dischargeMW = math.Max(0, solution.Variables[dischargeIdx])
			}

			netMW := dischargeMW - chargeMW
			if netMW > 0 {
				sellQty += netMW
			} else {
				buyQty += -netMW
			}

			socDelta := (chargeMW*batt.RoundTripEfficiency - dischargeMW) * 1.0
			socAfter := batt.CurrentSOC + socDelta/batt.CapacityMWh

			degradation := (chargeMW + dischargeMW) * batt.DegradationCostPerCycle / float64(batt.CycleLife)

			battSchedule[batt.DeviceID] = BatteryScheduleEntry{
				DeviceID:    batt.DeviceID,
				ChargeMW:    chargeMW,
				DischargeMW: dischargeMW,
				SOCAfter:    math.Max(0, math.Min(1, socAfter)),
				Degradation: degradation,
			}

			totalCost += degradation
		}

		result.BatterySchedule[h] = battSchedule

		loadSchedule := make(map[string]LoadScheduleEntry)
		for l := 0; l < numLoads; l++ {
			load := bo.vppEcon.FlexibleLoads[l]
			loadIdx := battChargeVars + battDischargeVars + l*numHours + h

			curtailedMW := 0.0
			if loadIdx < len(solution.Variables) {
				curtailedMW = math.Max(0, solution.Variables[loadIdx])
			}

			sellQty += curtailedMW

			cost := curtailedMW * load.CurtailmentCostPerMWh
			loadSchedule[load.DeviceID] = LoadScheduleEntry{
				DeviceID:      load.DeviceID,
				ScheduledMW:   curtailedMW,
				CurtailmentMW: curtailedMW,
				Cost:          cost,
			}

			totalCost += cost
		}

		result.LoadSchedule[h] = loadSchedule

		lmp := bo.lmpForecast.Hourly[h]
		sellRevenue := sellQty * lmp * (1 - bo.vppEcon.TradingFeeRate)
		buyCost := buyQty * lmp * (1 + bo.vppEcon.NetworkLossRate)

		hourlyRevenue := sellRevenue - buyCost
		hourlyCost := buyCost
		totalRevenue += hourlyRevenue

		bid.ExpectedRevenue = sellRevenue
		bid.ExpectedCost = buyCost + totalCost
		bid.NetProfit = hourlyRevenue - hourlyCost

		bid.SellPQ = generateStaircasePQ(sellQty, lmp, 5)
		bid.BuyPQ = generateStaircasePQ(buyQty, lmp, 3)

		result.Plan.HourlyBids[h] = bid
	}

	result.Plan.TotalRevenue = totalRevenue
	result.Plan.TotalCost = totalCost
	result.Plan.TotalProfit = totalRevenue - totalCost

	log.Printf("[market] optimization completed: profit=%.2f, revenue=%.2f, cost=%.2f, solveTime=%dms",
		result.Plan.TotalProfit, result.Plan.TotalRevenue, result.Plan.TotalCost, result.Plan.SolveTimeMS)

	return result, nil
}

func generateStaircasePQ(totalQty float64, refPrice float64, steps int) []PriceQuantityPair {
	if totalQty <= 1e-10 || steps <= 0 {
		return nil
	}

	pairs := make([]PriceQuantityPair, steps)
	qtyPerStep := totalQty / float64(steps)

	for i := 0; i < steps; i++ {
		priceMultiplier := 1.0 + float64(i)*0.05
		pairs[i] = PriceQuantityPair{
			Price:    roundTo2(refPrice * priceMultiplier),
			Quantity: roundTo2(qtyPerStep * float64(i+1)),
		}
	}

	return pairs
}

func roundTo2(v float64) float64 {
	return math.Round(v*100) / 100
}
