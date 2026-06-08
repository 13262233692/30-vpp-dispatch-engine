package market

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

type BidClientConfig struct {
	TradingCenterURL string
	APIKey           string
	APISecret        string
	VPPID            string
	HTTPTimeout      time.Duration
}

type BidClient struct {
	cfg        BidClientConfig
	client     *http.Client
	submitCount atomic.Int64
	successCount atomic.Int64
	errorCount  atomic.Int64
	lastSubmit  time.Time
}

func NewBidClient(cfg BidClientConfig) *BidClient {
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 15 * time.Second
	}

	return &BidClient{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.HTTPTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        5,
				MaxIdleConnsPerHost: 3,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}
}

func (bc *BidClient) SubmitBid(ctx context.Context, plan *DailyBidPlan) (*BidSubmissionResponse, error) {
	if bc.cfg.TradingCenterURL == "" {
		return bc.dryRunSubmit(plan)
	}

	reqBody := bc.buildRequest(plan)

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("market-bid: marshal error: %w", err)
	}

	signature := bc.sign(jsonBody)
	reqBody.Signature = signature

	jsonBody, err = json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("market-bid: marshal error: %w", err)
	}

	submitCtx, cancel := context.WithTimeout(ctx, bc.cfg.HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(submitCtx, "POST", bc.cfg.TradingCenterURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("market-bid: request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", bc.cfg.APIKey)
	req.Header.Set("X-VPP-ID", bc.cfg.VPPID)
	req.Header.Set("X-Signature", signature)

	resp, err := bc.client.Do(req)
	bc.submitCount.Add(1)
	bc.lastSubmit = time.Now()

	if err != nil {
		bc.errorCount.Add(1)
		return nil, fmt.Errorf("market-bid: submit error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		bc.errorCount.Add(1)
		return nil, fmt.Errorf("market-bid: read response error: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		bc.errorCount.Add(1)
		return nil, fmt.Errorf("market-bid: server returned %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	var bidResp BidSubmissionResponse
	if err := json.Unmarshal(body, &bidResp); err != nil {
		bc.errorCount.Add(1)
		return nil, fmt.Errorf("market-bid: parse response error: %w", err)
	}

	bc.successCount.Add(1)
	log.Printf("[market-bid] bid submitted successfully: bidID=%s, date=%s", bidResp.BidID, plan.BiddingDate)

	return &bidResp, nil
}

func (bc *BidClient) buildRequest(plan *DailyBidPlan) *BidSubmissionRequest {
	req := &BidSubmissionRequest{
		VPPID:       bc.cfg.VPPID,
		BiddingDate: plan.BiddingDate,
		NodeID:      plan.NodeID,
		Timestamp:   time.Now().UnixMilli(),
	}

	for h := 0; h < 24; h++ {
		bid := plan.HourlyBids[h]
		if bid == nil {
			continue
		}

		hbJSON := HourlyBidJSON{
			Hour: bid.Hour,
		}

		for _, pq := range bid.SellPQ {
			hbJSON.SellPQ = append(hbJSON.SellPQ, PQJSON{Price: pq.Price, Quantity: pq.Quantity})
		}
		for _, pq := range bid.BuyPQ {
			hbJSON.BuyPQ = append(hbJSON.BuyPQ, PQJSON{Price: pq.Price, Quantity: pq.Quantity})
		}

		req.HourlyBids = append(req.HourlyBids, hbJSON)
	}

	return req
}

func (bc *BidClient) sign(payload []byte) string {
	if bc.cfg.APISecret == "" {
		return ""
	}

	mac := hmac.New(sha256.New, []byte(bc.cfg.APISecret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (bc *BidClient) dryRunSubmit(plan *DailyBidPlan) (*BidSubmissionResponse, error) {
	bc.submitCount.Add(1)
	bc.successCount.Add(1)
	bc.lastSubmit = time.Now()

	log.Printf("[market-bid] DRY RUN bid for %s (node=%s, profit=%.2f)",
		plan.BiddingDate, plan.NodeID, plan.TotalProfit)

	for h := 0; h < 24; h++ {
		bid := plan.HourlyBids[h]
		if bid == nil {
			continue
		}

		sellQty := 0.0
		for _, pq := range bid.SellPQ {
			sellQty = pq.Quantity
		}
		buyQty := 0.0
		for _, pq := range bid.BuyPQ {
			buyQty = pq.Quantity
		}

		if sellQty > 0 || buyQty > 0 {
			log.Printf("[market-bid]   hour=%02d: sell=%.2fMW, buy=%.2fMW, profit=%.2f",
				h, sellQty, buyQty, bid.NetProfit)
		}
	}

	return &BidSubmissionResponse{
		Success:   true,
		Message:   "dry run submission",
		BidID:     fmt.Sprintf("DRY-%s-%d", plan.BiddingDate, time.Now().Unix()),
		Timestamp: time.Now().UnixMilli(),
	}, nil
}

func (bc *BidClient) Stats() (submitted int64, success int64, errors int64) {
	return bc.submitCount.Load(), bc.successCount.Load(), bc.errorCount.Load()
}
