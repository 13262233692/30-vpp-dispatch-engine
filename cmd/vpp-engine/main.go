package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vpp/dispatch-engine/internal/dispatcher"
	grpcServer "github.com/vpp/dispatch-engine/internal/grpc"
	"github.com/vpp/dispatch-engine/internal/ingestion"
	"github.com/vpp/dispatch-engine/internal/lifecycle"
	"github.com/vpp/dispatch-engine/internal/market"
	"github.com/vpp/dispatch-engine/internal/mms"
	"github.com/vpp/dispatch-engine/internal/redispool"
	"github.com/vpp/dispatch-engine/pkg/config"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Println("[main] VPP Dispatch Engine starting...")

	cfg := config.Load()
	log.Printf("[main] configuration: %s", cfg.Summary())

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	gm := lifecycle.NewGoroutineManager(rootCtx)

	healthMonitor := lifecycle.NewHealthMonitor(gm, cfg.Lifecycle.HealthInterval)
	healthMonitor.Start(rootCtx)
	defer healthMonitor.Stop()

	var pool *redispool.Pool
	var err error

	pool, err = redispool.NewPool(redispool.Config{
		Addr:          cfg.Redis.Addr,
		Password:      cfg.Redis.Password,
		DB:            cfg.Redis.DB,
		PoolSize:      cfg.Redis.PoolSize,
		BatchSize:     cfg.Redis.BatchSize,
		FlushInterval: cfg.Redis.FlushInterval,
		WriteTimeout:  cfg.Redis.WriteTimeout,
		ReadTimeout:   cfg.Redis.ReadTimeout,
		GM:            gm,
	})
	if err != nil {
		log.Printf("[main] Redis connection failed (running without Redis): %v", err)
	}

	eng := dispatcher.NewEngine(pool,
		dispatcher.WithGoroutineManager(gm),
		dispatcher.WithSyncInterval(cfg.Dispatcher.SyncInterval),
	)

	seedDemoLoads(eng)

	ingestionSrv := ingestion.NewServer(ingestion.Config{
		ListenAddr:  cfg.IEC61850.ListenAddr,
		RedisPool:   pool,
		GM:          gm,
		MaxConns:    cfg.IEC61850.MaxConns,
		IdleTimeout: cfg.IEC61850.IdleTimeout,
	})

	ingestionSrv.OnMeasurement(func(data *mms.SubstationData) {
		log.Printf("[main] measurement received: IED=%s nodes=%d", data.IEDName, len(data.Measurements))
	})

	if err := ingestionSrv.Start(rootCtx); err != nil {
		log.Fatalf("[main] IEC 61850 ingestion server failed: %v", err)
	}

	grpcSrv := grpcServer.NewDispatchServer(grpcServer.ServerConfig{
		ListenAddr:     cfg.GRPC.ListenAddr,
		Engine:         eng,
		GM:             gm,
		MaxConns:       cfg.GRPC.MaxConns,
		MaxRate:        cfg.GRPC.MaxRate,
		RateBurst:      cfg.GRPC.RateBurst,
		RequestTimeout: cfg.GRPC.RequestTimeout,
	})

	if err := grpcSrv.Start(rootCtx); err != nil {
		log.Fatalf("[main] gRPC server failed: %v", err)
	}

	eng.Start(rootCtx)

	var biddingOp *market.BiddingOperator
	if cfg.Market.Enabled {
		vppEcon := buildDemoVPPEconomics()
		biddingOp = market.NewBiddingOperator(market.BiddingOperatorConfig{
			VPPEconomics: vppEcon,
			FetcherConfig: market.DataFetcherConfig{
				WeatherAPIURL: cfg.Market.WeatherAPIURL,
				LMPAPIURL:     cfg.Market.LMPAPIURL,
				WeatherAPIKey: cfg.Market.WeatherAPIKey,
				LMPAPIKey:     cfg.Market.LMPAPIKey,
				NodeID:        cfg.Market.NodeID,
				HTTPTimeout:   10 * time.Second,
				FetchInterval: 15 * time.Minute,
			},
			BidClientConfig: market.BidClientConfig{
				TradingCenterURL: cfg.Market.TradingCenterURL,
				APIKey:           cfg.Market.TradingAPIKey,
				APISecret:        cfg.Market.TradingAPISecret,
				VPPID:            cfg.Market.VPPID,
				HTTPTimeout:      15 * time.Second,
			},
			GM:               gm,
			ScheduleInterval: cfg.Market.ScheduleInterval,
		})
		biddingOp.Start(rootCtx)
	}

	log.Println("[main] VPP Dispatch Engine is running")
	log.Printf("[main]   IEC 61850 TCP:  %s (maxConns=%d, idle=%v)", cfg.IEC61850.ListenAddr, cfg.IEC61850.MaxConns, cfg.IEC61850.IdleTimeout)
	log.Printf("[main]   gRPC dispatch:  %s (maxConns=%d, maxRate=%d/s, timeout=%v)", cfg.GRPC.ListenAddr, cfg.GRPC.MaxConns, cfg.GRPC.MaxRate, cfg.GRPC.RequestTimeout)
	log.Printf("[main]   Redis:          %s (writeTimeout=%v, readTimeout=%v)", cfg.Redis.Addr, cfg.Redis.WriteTimeout, cfg.Redis.ReadTimeout)
	log.Printf("[main]   Lifecycle:      healthInterval=%v, shutdownTimeout=%v", cfg.Lifecycle.HealthInterval, cfg.Lifecycle.ShutdownTimeout)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	log.Println("[main] shutdown signal received, gracefully stopping...")

	rootCancel()

	log.Println("[main] waiting for goroutines to exit...")
	if !gm.WaitTimeout(cfg.Lifecycle.ShutdownTimeout) {
		spawned, active, _ := gm.Stats()
		log.Printf("[main] WARNING: %d goroutines still active after %v (spawned=%d), forcing shutdown",
			active, cfg.Lifecycle.ShutdownTimeout, spawned)
		activeList := gm.DumpActive()
		for _, name := range activeList {
			log.Printf("[main]   stuck: %s", name)
		}
		gm.CancelAll()
		time.Sleep(1 * time.Second)
	}

	grpcSrv.Stop()
	ingestionSrv.Stop()
	eng.Stop()

	if biddingOp != nil {
		biddingOp.Stop()
	}

	if pool != nil {
		pool.Close()
	}

	spawned, active, panics := gm.Stats()
	log.Printf("[main] VPP Dispatch Engine stopped (goroutines: spawned=%d, active=%d, panics=%d)",
		spawned, active, panics)
}

func seedDemoLoads(eng *dispatcher.Engine) {
	demoLoads := []*dispatcher.FlexibleLoad{
		{DeviceID: "HVAC-BLDG-A-01", DeviceType: "hvac", CurrentLoadMW: 15.0, MaxReductionMW: 9.0, ResponseTimeMS: 2000, CostPerMW: 50, SOC: 0, Priority: 3, Online: true},
		{DeviceID: "HVAC-BLDG-A-02", DeviceType: "hvac", CurrentLoadMW: 12.0, MaxReductionMW: 7.2, ResponseTimeMS: 2500, CostPerMW: 55, SOC: 0, Priority: 3, Online: true},
		{DeviceID: "HVAC-BLDG-B-01", DeviceType: "hvac", CurrentLoadMW: 18.0, MaxReductionMW: 10.8, ResponseTimeMS: 1800, CostPerMW: 45, SOC: 0, Priority: 2, Online: true},
		{DeviceID: "EV-STATION-01", DeviceType: "ev_charger", CurrentLoadMW: 8.0, MaxReductionMW: 7.2, ResponseTimeMS: 500, CostPerMW: 80, SOC: 65, Priority: 4, Online: true},
		{DeviceID: "EV-STATION-02", DeviceType: "ev_charger", CurrentLoadMW: 6.0, MaxReductionMW: 5.4, ResponseTimeMS: 600, CostPerMW: 85, SOC: 42, Priority: 4, Online: true},
		{DeviceID: "EV-STATION-03", DeviceType: "ev_charger", CurrentLoadMW: 10.0, MaxReductionMW: 9.0, ResponseTimeMS: 400, CostPerMW: 75, SOC: 80, Priority: 5, Online: true},
		{DeviceID: "BAT-SITE-01", DeviceType: "battery", CurrentLoadMW: 5.0, MaxReductionMW: 4.0, ResponseTimeMS: 100, CostPerMW: 30, SOC: 85, Priority: 1, Online: true},
		{DeviceID: "BAT-SITE-02", DeviceType: "battery", CurrentLoadMW: 8.0, MaxReductionMW: 6.4, ResponseTimeMS: 150, CostPerMW: 35, SOC: 72, Priority: 1, Online: true},
		{DeviceID: "BAT-SITE-03", DeviceType: "battery", CurrentLoadMW: 3.0, MaxReductionMW: 0, ResponseTimeMS: 120, CostPerMW: 40, SOC: 8, Priority: 2, Online: true},
		{DeviceID: "IND-FACTORY-01", DeviceType: "industrial", CurrentLoadMW: 25.0, MaxReductionMW: 7.5, ResponseTimeMS: 5000, CostPerMW: 120, SOC: 0, Priority: 5, Online: true},
		{DeviceID: "IND-FACTORY-02", DeviceType: "industrial", CurrentLoadMW: 20.0, MaxReductionMW: 6.0, ResponseTimeMS: 8000, CostPerMW: 100, SOC: 0, Priority: 5, Online: true},
		{DeviceID: "HVAC-BLDG-C-01", DeviceType: "hvac", CurrentLoadMW: 10.0, MaxReductionMW: 6.0, ResponseTimeMS: 3000, CostPerMW: 60, SOC: 0, Priority: 3, Online: true},
	}

	for _, load := range demoLoads {
		eng.RegisterLoad(load)
	}

	log.Printf("[main] seeded %d demo flexible loads", len(demoLoads))
}

func buildDemoVPPEconomics() *market.VPPEconomics {
	return &market.VPPEconomics{
		Batteries: []market.BatteryEconomics{
			{
				DeviceID: "BAT-SITE-01", CapacityMWh: 20, CurrentSOC: 0.85, MinSOC: 0.1, MaxSOC: 0.95,
				ChargeRateMW: 10, DischargeRateMW: 10, RampRateMWPerMin: 5,
				RoundTripEfficiency: 0.92, CycleLife: 6000, ReplacementCost: 2000000,
				DegradationCostPerCycle: 333, InitialInvestment: 2000000,
			},
			{
				DeviceID: "BAT-SITE-02", CapacityMWh: 30, CurrentSOC: 0.72, MinSOC: 0.1, MaxSOC: 0.95,
				ChargeRateMW: 15, DischargeRateMW: 15, RampRateMWPerMin: 4,
				RoundTripEfficiency: 0.90, CycleLife: 5000, ReplacementCost: 3000000,
				DegradationCostPerCycle: 600, InitialInvestment: 3000000,
			},
		},
		FlexibleLoads: []market.FlexibleLoadEconomics{
			{
				DeviceID: "HVAC-BLDG-A-01", DeviceType: "hvac", MaxLoadMW: 15, MinLoadMW: 5,
				RampRateMWPerMin: 2, CurtailmentCostPerMWh: 80,
			},
			{
				DeviceID: "EV-STATION-01", DeviceType: "ev_charger", MaxLoadMW: 10, MinLoadMW: 0,
				RampRateMWPerMin: 5, CurtailmentCostPerMWh: 120,
			},
			{
				DeviceID: "IND-FACTORY-01", DeviceType: "industrial", MaxLoadMW: 25, MinLoadMW: 15,
				RampRateMWPerMin: 1, CurtailmentCostPerMWh: 200,
			},
		},
		NetworkLossRate: 0.03,
		TradingFeeRate:  0.02,
	}
}
