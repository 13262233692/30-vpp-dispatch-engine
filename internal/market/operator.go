package market

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/vpp/dispatch-engine/internal/lifecycle"
)

type BiddingOperator struct {
	optimizer *BiddingOptimizer
	fetcher   *DataFetcher
	bidClient *BidClient
	gm        *lifecycle.GoroutineManager

	scheduleInterval time.Duration
	submitDeadline   time.Time

	mu          sync.RWMutex
	latestResult *OptimizationResult
	latestPlan   *DailyBidPlan

	optimizeCount int64
	submitCount   int64
	errorCount    int64
}

type BiddingOperatorConfig struct {
	VPPEconomics     *VPPEconomics
	FetcherConfig    DataFetcherConfig
	BidClientConfig  BidClientConfig
	GM               *lifecycle.GoroutineManager
	ScheduleInterval time.Duration
}

func NewBiddingOperator(cfg BiddingOperatorConfig) *BiddingOperator {
	gm := cfg.GM
	if gm == nil {
		gm = lifecycle.NewGoroutineManager(context.Background())
	}

	if cfg.ScheduleInterval == 0 {
		cfg.ScheduleInterval = 1 * time.Hour
	}

	fetcher := NewDataFetcher(cfg.FetcherConfig)
	bidClient := NewBidClient(cfg.BidClientConfig)
	optimizer := NewBiddingOptimizer(cfg.VPPEconomics)

	return &BiddingOperator{
		optimizer:        optimizer,
		fetcher:          fetcher,
		bidClient:        bidClient,
		gm:               gm,
		scheduleInterval: cfg.ScheduleInterval,
	}
}

func (bo *BiddingOperator) Start(ctx context.Context) {
	bo.fetcher.Start(ctx)

	now := time.Now()
	nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	bo.submitDeadline = nextDay.Add(-2 * time.Hour)

	bo.gm.Go(func(gmCtx context.Context) error {
		bo.scheduleLoop(gmCtx)
		return nil
	}, lifecycle.WithName("bidding-scheduler"), lifecycle.WithParent(ctx))

	bo.gm.Go(func(gmCtx context.Context) error {
		bo.submitWatcher(gmCtx)
		return nil
	}, lifecycle.WithName("bidding-submitter"), lifecycle.WithParent(ctx))

	log.Printf("[bidding-operator] started (interval=%v, deadline=%v)",
		bo.scheduleInterval, bo.submitDeadline.Format("15:04:05"))
}

func (bo *BiddingOperator) Stop() {
	bo.gm.CancelAll()
}

func (bo *BiddingOperator) scheduleLoop(ctx context.Context) {
	bo.runOptimization()

	ticker := time.NewTicker(bo.scheduleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bo.runOptimization()
		}
	}
}

func (bo *BiddingOperator) submitWatcher(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			if now.After(bo.submitDeadline) || now.Equal(bo.submitDeadline) {
				bo.submitCurrentBid(ctx)

				nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
				bo.submitDeadline = nextDay.Add(-2 * time.Hour)
			}
		}
	}
}

func (bo *BiddingOperator) runOptimization() {
	lmpFC := bo.fetcher.GetLMPForecast()
	if lmpFC == nil {
		log.Printf("[bidding-operator] waiting for LMP forecast data...")
		return
	}

	weatherFC := bo.fetcher.GetWeatherForecast()

	bo.optimizer.SetLMPForecast(lmpFC)
	if weatherFC != nil {
		bo.optimizer.SetWeatherForecast(weatherFC)
	}

	result, err := bo.optimizer.Optimize()
	bo.optimizeCount++

	if err != nil {
		bo.errorCount++
		log.Printf("[bidding-operator] optimization failed: %v", err)
		return
	}

	if result.Plan == nil || !result.Plan.Feasible {
		bo.errorCount++
		log.Printf("[bidding-operator] optimization result infeasible")
		return
	}

	bo.mu.Lock()
	bo.latestResult = result
	bo.latestPlan = result.Plan
	bo.mu.Unlock()

	log.Printf("[bidding-operator] optimization #%d: profit=%.2f, revenue=%.2f, cost=%.2f, solveTime=%dms",
		bo.optimizeCount, result.Plan.TotalProfit, result.Plan.TotalRevenue,
		result.Plan.TotalCost, result.Plan.SolveTimeMS)
}

func (bo *BiddingOperator) submitCurrentBid(ctx context.Context) {
	bo.mu.RLock()
	plan := bo.latestPlan
	bo.mu.RUnlock()

	if plan == nil {
		log.Printf("[bidding-operator] no bid plan available for submission")
		return
	}

	if !plan.Feasible {
		log.Printf("[bidding-operator] current plan is infeasible, skipping submission")
		return
	}

	resp, err := bo.bidClient.SubmitBid(ctx, plan)
	bo.submitCount++

	if err != nil {
		bo.errorCount++
		log.Printf("[bidding-operator] bid submission failed: %v", err)
		return
	}

	if resp.Success {
		log.Printf("[bidding-operator] bid submitted: bidID=%s, date=%s", resp.BidID, plan.BiddingDate)
	}
}

func (bo *BiddingOperator) GetLatestPlan() *DailyBidPlan {
	bo.mu.RLock()
	defer bo.mu.RUnlock()
	return bo.latestPlan
}

func (bo *BiddingOperator) GetLatestResult() *OptimizationResult {
	bo.mu.RLock()
	defer bo.mu.RUnlock()
	return bo.latestResult
}

func (bo *BiddingOperator) Stats() (optimizes int64, submits int64, errors int64) {
	return bo.optimizeCount, bo.submitCount, bo.errorCount
}
