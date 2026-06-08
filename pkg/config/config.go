package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	IEC61850  IEC61850Config
	Redis     RedisConfig
	GRPC      GRPCConfig
	Dispatcher DispatcherConfig
}

type IEC61850Config struct {
	ListenAddr string
}

type RedisConfig struct {
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	BatchSize    int
	FlushInterval time.Duration
}

type GRPCConfig struct {
	ListenAddr string
}

type DispatcherConfig struct {
	SyncInterval time.Duration
}

func Load() *Config {
	return &Config{
		IEC61850: IEC61850Config{
			ListenAddr: envOrDefault("IEC61850_LISTEN_ADDR", "0.0.0.0:102"),
		},
		Redis: RedisConfig{
			Addr:         envOrDefault("REDIS_ADDR", "localhost:6379"),
			Password:     envOrDefault("REDIS_PASSWORD", ""),
			DB:           envOrDefaultInt("REDIS_DB", 0),
			PoolSize:     envOrDefaultInt("REDIS_POOL_SIZE", 20),
			BatchSize:    envOrDefaultInt("REDIS_BATCH_SIZE", 256),
			FlushInterval: envOrDefaultDuration("REDIS_FLUSH_INTERVAL", 100*time.Millisecond),
		},
		GRPC: GRPCConfig{
			ListenAddr: envOrDefault("GRPC_LISTEN_ADDR", "0.0.0.0:50051"),
		},
		Dispatcher: DispatcherConfig{
			SyncInterval: envOrDefaultDuration("DISPATCHER_SYNC_INTERVAL", 5*time.Second),
		},
	}
}

func (c *Config) Summary() string {
	return fmt.Sprintf(
		"Config{IEC61850=%s, Redis=%s, GRPC=%s, Dispatcher.SyncInterval=%v}",
		c.IEC61850.ListenAddr, c.Redis.Addr, c.GRPC.ListenAddr, c.Dispatcher.SyncInterval,
	)
}

func envOrDefault(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

func envOrDefaultInt(key string, defaultValue int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return defaultValue
}

func envOrDefaultDuration(key string, defaultValue time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultValue
}
