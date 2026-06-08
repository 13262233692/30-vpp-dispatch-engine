package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	IEC61850   IEC61850Config
	Redis      RedisConfig
	GRPC       GRPCConfig
	Dispatcher DispatcherConfig
	Lifecycle  LifecycleConfig
}

type IEC61850Config struct {
	ListenAddr  string
	MaxConns    int
	IdleTimeout time.Duration
}

type RedisConfig struct {
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	BatchSize    int
	FlushInterval time.Duration
	WriteTimeout  time.Duration
	ReadTimeout   time.Duration
}

type GRPCConfig struct {
	ListenAddr    string
	MaxConns      int
	MaxRate       int
	RateBurst     int
	RequestTimeout time.Duration
}

type DispatcherConfig struct {
	SyncInterval time.Duration
}

type LifecycleConfig struct {
	HealthInterval   time.Duration
	ShutdownTimeout  time.Duration
}

func Load() *Config {
	return &Config{
		IEC61850: IEC61850Config{
			ListenAddr:  envOrDefault("IEC61850_LISTEN_ADDR", "0.0.0.0:102"),
			MaxConns:    envOrDefaultInt("IEC61850_MAX_CONNS", 500),
			IdleTimeout: envOrDefaultDuration("IEC61850_IDLE_TIMEOUT", 60*time.Second),
		},
		Redis: RedisConfig{
			Addr:          envOrDefault("REDIS_ADDR", "localhost:6379"),
			Password:      envOrDefault("REDIS_PASSWORD", ""),
			DB:            envOrDefaultInt("REDIS_DB", 0),
			PoolSize:      envOrDefaultInt("REDIS_POOL_SIZE", 20),
			BatchSize:     envOrDefaultInt("REDIS_BATCH_SIZE", 256),
			FlushInterval: envOrDefaultDuration("REDIS_FLUSH_INTERVAL", 100*time.Millisecond),
			WriteTimeout:  envOrDefaultDuration("REDIS_WRITE_TIMEOUT", 5*time.Second),
			ReadTimeout:   envOrDefaultDuration("REDIS_READ_TIMEOUT", 3*time.Second),
		},
		GRPC: GRPCConfig{
			ListenAddr:     envOrDefault("GRPC_LISTEN_ADDR", "0.0.0.0:50051"),
			MaxConns:       envOrDefaultInt("GRPC_MAX_CONNS", 1000),
			MaxRate:        envOrDefaultInt("GRPC_MAX_RATE", 1000),
			RateBurst:      envOrDefaultInt("GRPC_RATE_BURST", 2000),
			RequestTimeout: envOrDefaultDuration("GRPC_REQUEST_TIMEOUT", 30*time.Second),
		},
		Dispatcher: DispatcherConfig{
			SyncInterval: envOrDefaultDuration("DISPATCHER_SYNC_INTERVAL", 5*time.Second),
		},
		Lifecycle: LifecycleConfig{
			HealthInterval:  envOrDefaultDuration("LIFECYCLE_HEALTH_INTERVAL", 30*time.Second),
			ShutdownTimeout: envOrDefaultDuration("LIFECYCLE_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
	}
}

func (c *Config) Summary() string {
	return fmt.Sprintf(
		"Config{IEC61850=%s(maxConns=%d), Redis=%s, GRPC=%s(maxConns=%d,maxRate=%d), Lifecycle(shutdown=%v)}",
		c.IEC61850.ListenAddr, c.IEC61850.MaxConns,
		c.Redis.Addr,
		c.GRPC.ListenAddr, c.GRPC.MaxConns, c.GRPC.MaxRate,
		c.Lifecycle.ShutdownTimeout,
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
