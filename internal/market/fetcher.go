package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sync"
	"time"
)

type DataFetcherConfig struct {
	WeatherAPIURL  string
	LMPAPIURL      string
	WeatherAPIKey  string
	LMPAPIKey      string
	NodeID         string
	HTTPTimeout    time.Duration
	FetchInterval  time.Duration
}

type DataFetcher struct {
	cfg          DataFetcherConfig
	client       *http.Client
	lmpForecast  *LMPForecast
	weather      *WeatherForecast
	lmpHistory   []LMPData
	mu           sync.RWMutex
	fetchCount   int64
	errorCount   int64
	lastFetchLMP time.Time
	lastFetchWx  time.Time
}

func NewDataFetcher(cfg DataFetcherConfig) *DataFetcher {
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	if cfg.FetchInterval == 0 {
		cfg.FetchInterval = 15 * time.Minute
	}

	return &DataFetcher{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.HTTPTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		lmpHistory: make([]LMPData, 0),
	}
}

func (df *DataFetcher) Start(ctx context.Context) {
	go df.fetchLoop(ctx)
}

func (df *DataFetcher) fetchLoop(ctx context.Context) {
	df.fetchLMP(ctx)
	df.fetchWeather(ctx)

	ticker := time.NewTicker(df.cfg.FetchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			df.fetchLMP(ctx)
			df.fetchWeather(ctx)
		}
	}
}

func (df *DataFetcher) fetchLMP(ctx context.Context) {
	if df.cfg.LMPAPIURL == "" {
		df.generateSyntheticLMP()
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, df.cfg.HTTPTimeout)
	defer cancel()

	url := fmt.Sprintf("%s?node_id=%s&forecast=true", df.cfg.LMPAPIURL, df.cfg.NodeID)
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		df.errorCount++
		log.Printf("[market-fetcher] LMP request creation error: %v", err)
		df.generateSyntheticLMP()
		return
	}

	if df.cfg.LMPAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+df.cfg.LMPAPIKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := df.client.Do(req)
	if err != nil {
		df.errorCount++
		log.Printf("[market-fetcher] LMP fetch error: %v", err)
		df.generateSyntheticLMP()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		df.errorCount++
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[market-fetcher] LMP API returned %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
		df.generateSyntheticLMP()
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		df.errorCount++
		return
	}

	var lmpResp struct {
		NodeID   string  `json:"node_id"`
		Hourly   []struct {
			Hour int     `json:"hour"`
			LMP  float64 `json:"lmp"`
		} `json:"hourly"`
	}

	if err := json.Unmarshal(body, &lmpResp); err != nil {
		df.errorCount++
		log.Printf("[market-fetcher] LMP parse error: %v", err)
		df.generateSyntheticLMP()
		return
	}

	fc := &LMPForecast{
		NodeID:    lmpResp.NodeID,
		FetchedAt: time.Now(),
	}
	for _, h := range lmpResp.Hourly {
		if h.Hour >= 0 && h.Hour < 24 {
			fc.Hourly[h.Hour] = h.LMP
		}
	}

	df.mu.Lock()
	df.lmpForecast = fc
	df.lastFetchLMP = time.Now()
	df.mu.Unlock()
	df.fetchCount++

	log.Printf("[market-fetcher] LMP forecast updated for node %s (%d hourly prices)", fc.NodeID, len(lmpResp.Hourly))
}

func (df *DataFetcher) fetchWeather(ctx context.Context) {
	if df.cfg.WeatherAPIURL == "" {
		df.generateSyntheticWeather()
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, df.cfg.HTTPTimeout)
	defer cancel()

	url := fmt.Sprintf("%s?hours=24", df.cfg.WeatherAPIURL)
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		df.errorCount++
		df.generateSyntheticWeather()
		return
	}

	if df.cfg.WeatherAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+df.cfg.WeatherAPIKey)
	}

	resp, err := df.client.Do(req)
	if err != nil {
		df.errorCount++
		df.generateSyntheticWeather()
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		df.errorCount++
		df.generateSyntheticWeather()
		return
	}

	var wxResp struct {
		Hourly []struct {
			Hour            int     `json:"hour"`
			Temperature     float64 `json:"temperature"`
			SolarIrradiance float64 `json:"solar_irradiance"`
			WindSpeed       float64 `json:"wind_speed"`
			CloudCover      float64 `json:"cloud_cover"`
			Humidity        float64 `json:"humidity"`
		} `json:"hourly"`
	}

	if err := json.Unmarshal(body, &wxResp); err != nil {
		df.errorCount++
		df.generateSyntheticWeather()
		return
	}

	wf := &WeatherForecast{FetchedAt: time.Now()}
	for _, h := range wxResp.Hourly {
		if h.Hour >= 0 && h.Hour < 24 {
			wf.Temperature[h.Hour] = h.Temperature
			wf.SolarIrradiance[h.Hour] = h.SolarIrradiance
			wf.WindSpeed[h.Hour] = h.WindSpeed
			wf.CloudCover[h.Hour] = h.CloudCover
			wf.Humidity[h.Hour] = h.Humidity
		}
	}

	df.mu.Lock()
	df.weather = wf
	df.lastFetchWx = time.Now()
	df.mu.Unlock()

	log.Printf("[market-fetcher] weather forecast updated (%d hourly records)", len(wxResp.Hourly))
}

func (df *DataFetcher) generateSyntheticLMP() {
	now := time.Now()
	fc := &LMPForecast{
		NodeID:    df.cfg.NodeID,
		FetchedAt: now,
	}

	baseLMP := 350.0
	for h := 0; h < 24; h++ {
		var multiplier float64
		switch {
		case h >= 0 && h < 6:
			multiplier = 0.6
		case h >= 6 && h < 9:
			multiplier = 0.6 + float64(h-6)*0.2
		case h >= 9 && h < 12:
			multiplier = 1.2
		case h >= 12 && h < 14:
			multiplier = 1.0
		case h >= 14 && h < 17:
			multiplier = 1.1 + float64(h-14)*0.15
		case h >= 17 && h < 21:
			multiplier = 1.55 + float64(h-17)*0.15
		case h >= 21 && h < 23:
			multiplier = 1.55 - float64(h-21)*0.3
		default:
			multiplier = 0.7
		}

		dayOfWeek := now.Weekday()
		if dayOfWeek == time.Saturday || dayOfWeek == time.Sunday {
			multiplier *= 0.85
		}

		fc.Hourly[h] = math.Round(baseLMP*multiplier*100) / 100
	}

	df.mu.Lock()
	df.lmpForecast = fc
	df.lastFetchLMP = time.Now()
	df.mu.Unlock()
	df.fetchCount++

	log.Printf("[market-fetcher] synthetic LMP generated for node %s", fc.NodeID)
}

func (df *DataFetcher) generateSyntheticWeather() {
	wf := &WeatherForecast{FetchedAt: time.Now()}

	for h := 0; h < 24; h++ {
		if h >= 6 && h <= 18 {
			solarAngle := math.Pi * float64(h-6) / 12.0
			wf.SolarIrradiance[h] = math.Round(800*math.Sin(solarAngle)*100) / 100
			wf.Temperature[h] = 15 + 15*math.Sin(solarAngle)
		} else {
			wf.SolarIrradiance[h] = 0
			wf.Temperature[h] = 10 + 5*float64(h%6)/6
		}
		wf.WindSpeed[h] = 3 + 2*math.Sin(float64(h)*math.Pi/12)
		wf.CloudCover[h] = math.Round(30+20*math.Sin(float64(h)*math.Pi/8)*100) / 100
		wf.Humidity[h] = 50 + 20*math.Cos(float64(h)*math.Pi/12)
	}

	df.mu.Lock()
	df.weather = wf
	df.lastFetchWx = time.Now()
	df.mu.Unlock()
}

func (df *DataFetcher) GetLMPForecast() *LMPForecast {
	df.mu.RLock()
	defer df.mu.RUnlock()
	return df.lmpForecast
}

func (df *DataFetcher) GetWeatherForecast() *WeatherForecast {
	df.mu.RLock()
	defer df.mu.RUnlock()
	return df.weather
}

func (df *DataFetcher) Stats() (fetches int64, errors int64, lastLMP time.Time, lastWx time.Time) {
	df.mu.RLock()
	defer df.mu.RUnlock()
	return df.fetchCount, df.errorCount, df.lastFetchLMP, df.lastFetchWx
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
