package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/vpp/dispatch-engine/internal/dispatcher"
	grpcServer "github.com/vpp/dispatch-engine/internal/grpc"
	"github.com/vpp/dispatch-engine/internal/ingestion"
	"github.com/vpp/dispatch-engine/internal/mms"
	"github.com/vpp/dispatch-engine/internal/redispool"
	"github.com/vpp/dispatch-engine/pkg/config"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Println("[main] VPP Dispatch Engine starting...")

	cfg := config.Load()
	log.Printf("[main] configuration: %s", cfg.Summary())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var pool *redispool.Pool
	var err error

	pool, err = redispool.NewPool(redispool.Config{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		PoolSize:     cfg.Redis.PoolSize,
		BatchSize:    cfg.Redis.BatchSize,
		FlushInterval: cfg.Redis.FlushInterval,
	})
	if err != nil {
		log.Printf("[main] Redis connection failed (running without Redis): %v", err)
	}

	eng := dispatcher.NewEngine(pool)

	seedDemoLoads(eng)

	ingestionSrv := ingestion.NewServer(ingestion.Config{
		ListenAddr: cfg.IEC61850.ListenAddr,
		RedisPool:  pool,
	})

	ingestionSrv.OnMeasurement(func(data *mms.SubstationData) {
		log.Printf("[main] measurement received: IED=%s nodes=%d", data.IEDName, len(data.Measurements))
	})

	if err := ingestionSrv.Start(ctx); err != nil {
		log.Fatalf("[main] IEC 61850 ingestion server failed: %v", err)
	}

	grpcSrv := grpcServer.NewDispatchServer(grpcServer.ServerConfig{
		ListenAddr: cfg.GRPC.ListenAddr,
		Engine:     eng,
	})

	if err := grpcSrv.Start(ctx); err != nil {
		log.Fatalf("[main] gRPC server failed: %v", err)
	}

	log.Println("[main] VPP Dispatch Engine is running")
	log.Printf("[main]   IEC 61850 TCP:  %s", cfg.IEC61850.ListenAddr)
	log.Printf("[main]   gRPC dispatch:  %s", cfg.GRPC.ListenAddr)
	log.Printf("[main]   Redis:          %s", cfg.Redis.Addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	log.Println("[main] shutdown signal received, gracefully stopping...")

	cancel()

	grpcSrv.Stop()
	ingestionSrv.Stop()

	if pool != nil {
		pool.Close()
	}

	log.Println("[main] VPP Dispatch Engine stopped")
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
