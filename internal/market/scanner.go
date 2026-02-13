package market

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"polymarket-mm/internal/config"
	"polymarket-mm/pkg/types"
)

// Scanner periodically polls the Gamma API to discover the best market-making
// opportunities. It ranks markets by a composite score:
//
//   score = spread × √(volume24h) × min(liquidity/10000, 1)
//
// High-spread, high-volume, reasonably liquid markets score highest. The engine
// reads ScanResults from the Results() channel and starts/stops market goroutines
// to match the selected markets.

// GammaMarket is the JSON shape returned by the Gamma API.
type GammaMarket struct {
	ID                    string  `json:"id"`
	Question              string  `json:"question"`
	ConditionID           string  `json:"conditionId"`
	Slug                  string  `json:"slug"`
	Active                bool    `json:"active"`
	Closed                bool    `json:"closed"`
	AcceptingOrders       bool    `json:"acceptingOrders"`
	EnableOrderBook       bool    `json:"enableOrderBook"`
	EndDate               string  `json:"endDate"`
	Liquidity             string  `json:"liquidity"`
	Volume24hr            float64 `json:"volume24hr"`
	Outcomes              string  `json:"outcomes"`
	OutcomePrices         string  `json:"outcomePrices"`
	ClobTokenIds          string  `json:"clobTokenIds"`
	NegRisk               bool    `json:"negRisk"`
	Spread                float64 `json:"spread"`
	BestBid               float64 `json:"bestBid"`
	BestAsk               float64 `json:"bestAsk"`
	LastTradePrice        float64 `json:"lastTradePrice"`
	OrderPriceMinTickSize float64 `json:"orderPriceMinTickSize"`
	OrderMinSize          float64 `json:"orderMinSize"`
	RewardsMinSize        float64 `json:"rewardsMinSize"`
	RewardsMaxSpread      float64 `json:"rewardsMaxSpread"`
}

// ScanResult contains markets ranked by opportunity quality.
type ScanResult struct {
	Markets   []types.MarketAllocation
	ScannedAt time.Time
}

// Scanner periodically polls the Gamma API for wide-spread markets.
type Scanner struct {
	httpClient *resty.Client        // HTTP client pointed at Gamma API
	cfg        config.ScannerConfig // filter thresholds + poll interval
	riskCfg    config.RiskConfig    // MaxMarketsActive, MaxPositionPerMarket
	logger     *slog.Logger
	resultCh   chan ScanResult // engine reads selected markets from here
}

// NewScanner creates a market scanner.
func NewScanner(cfg config.Config, logger *slog.Logger) *Scanner {
	client := resty.New().
		SetBaseURL(cfg.API.GammaBaseURL).
		SetTimeout(15 * time.Second).
		SetRetryCount(2).
		SetRetryWaitTime(time.Second)

	return &Scanner{
		httpClient: client,
		cfg:        cfg.Scanner,
		riskCfg:    cfg.Risk,
		logger:     logger.With("component", "scanner"),
		resultCh:   make(chan ScanResult, 1),
	}
}

// Results returns the channel the engine reads from.
func (s *Scanner) Results() <-chan ScanResult {
	return s.resultCh
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (s *Scanner) Run(ctx context.Context) {
	// Do an immediate scan on startup
	s.scan(ctx)

	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

func (s *Scanner) scan(ctx context.Context) {
	markets, err := s.fetchMarkets(ctx)
	if err != nil {
		s.logger.Error("scan failed", "error", err)
		return
	}

	filtered := s.filterMarkets(markets)
	ranked := s.rankMarkets(filtered)

	// Cap to max active markets
	if len(ranked) > s.riskCfg.MaxMarketsActive {
		ranked = ranked[:s.riskCfg.MaxMarketsActive]
	}

	result := ScanResult{
		Markets:   ranked,
		ScannedAt: time.Now(),
	}

	s.logger.Info("scan complete",
		"total", len(markets),
		"filtered", len(filtered),
		"selected", len(ranked),
	)

	// Non-blocking send
	select {
	case s.resultCh <- result:
	default:
		// Replace stale result
		select {
		case <-s.resultCh:
		default:
		}
		s.resultCh <- result
	}
}

func (s *Scanner) fetchMarkets(ctx context.Context) ([]GammaMarket, error) {
	var allMarkets []GammaMarket
	offset := 0
	limit := 100

	for {
		var page []GammaMarket
		resp, err := s.httpClient.R().
			SetContext(ctx).
			SetQueryParams(map[string]string{
				"limit":  strconv.Itoa(limit),
				"offset": strconv.Itoa(offset),
				"active": "true",
				"closed": "false",
			}).
			SetResult(&page).
			Get("/markets")
		if err != nil {
			return nil, fmt.Errorf("fetch markets page %d: %w", offset, err)
		}
		if resp.StatusCode() != 200 {
			return nil, fmt.Errorf("fetch markets: status %d", resp.StatusCode())
		}

		allMarkets = append(allMarkets, page...)

		if len(page) < limit {
			break
		}
		offset += limit
	}

	return allMarkets, nil
}

// filterMarkets applies hard filters to eliminate unsuitable markets:
// inactive, closed, not accepting orders, no order book, optional include filters,
// excluded slugs/keywords, insufficient liquidity/volume/spread, end date too near
// or too far, missing token IDs.
func (s *Scanner) filterMarkets(markets []GammaMarket) []GammaMarket {
	excluded := make(map[string]bool)
	for _, slug := range s.cfg.ExcludeSlugs {
		slug = strings.ToLower(strings.TrimSpace(slug))
		if slug != "" {
			excluded[slug] = true
		}
	}

	includeConditionIDs := make(map[string]bool)
	for _, conditionID := range s.cfg.IncludeConditionIDs {
		conditionID = strings.ToLower(strings.TrimSpace(conditionID))
		if conditionID != "" {
			includeConditionIDs[conditionID] = true
		}
	}

	includeSlugs := make(map[string]bool)
	for _, slug := range s.cfg.IncludeSlugs {
		slug = strings.ToLower(strings.TrimSpace(slug))
		if slug != "" {
			includeSlugs[slug] = true
		}
	}

	includeKeywords := make([]string, 0, len(s.cfg.IncludeKeywords))
	for _, kw := range s.cfg.IncludeKeywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" {
			includeKeywords = append(includeKeywords, kw)
		}
	}

	excludeKeywords := make([]string, 0, len(s.cfg.ExcludeKeywords))
	for _, kw := range s.cfg.ExcludeKeywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" {
			excludeKeywords = append(excludeKeywords, kw)
		}
	}

	hasIncludeFilter := len(includeConditionIDs) > 0 || len(includeSlugs) > 0 || len(includeKeywords) > 0

	now := time.Now()
	maxEnd := now.AddDate(0, 0, s.cfg.MaxEndDateDays)

	var result []GammaMarket
	for _, m := range markets {
		if !m.Active || m.Closed || !m.AcceptingOrders || !m.EnableOrderBook {
			continue
		}

		slugLower := strings.ToLower(m.Slug)
		questionLower := strings.ToLower(m.Question)
		conditionLower := strings.ToLower(m.ConditionID)

		if hasIncludeFilter {
			matched := includeConditionIDs[conditionLower] || includeSlugs[slugLower]
			if !matched {
				for _, kw := range includeKeywords {
					if strings.Contains(slugLower, kw) || strings.Contains(questionLower, kw) {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
		}

		if excluded[slugLower] {
			continue
		}
		excludedByKeyword := false
		for _, kw := range excludeKeywords {
			if strings.Contains(slugLower, kw) || strings.Contains(questionLower, kw) {
				excludedByKeyword = true
				break
			}
		}
		if excludedByKeyword {
			continue
		}

		// Parse liquidity
		liquidity, _ := strconv.ParseFloat(m.Liquidity, 64)
		if liquidity < s.cfg.MinLiquidity {
			continue
		}

		if m.Volume24hr < s.cfg.MinVolume24h {
			continue
		}

		if m.Spread < s.cfg.MinSpread {
			continue
		}

		// Check end date (reject unparseable dates)
		if m.EndDate != "" {
			endDate, err := time.Parse(time.RFC3339, m.EndDate)
			if err != nil {
				continue
			}
			if endDate.Before(now) || endDate.After(maxEnd) {
				continue
			}
		}

		// Ensure we have token IDs
		if m.ClobTokenIds == "" {
			continue
		}

		result = append(result, m)
	}

	return result
}

// rankMarkets scores and sorts markets by opportunity quality.
// score = spread × √volume × liquidityFactor, where liquidityFactor
// is capped at 1.0 (10k USD liquidity saturates the bonus).
func (s *Scanner) rankMarkets(markets []GammaMarket) []types.MarketAllocation {
	type scored struct {
		market GammaMarket
		score  float64
	}

	var scoredMarkets []scored
	for _, m := range markets {
		liquidity, _ := strconv.ParseFloat(m.Liquidity, 64)
		liquidityFactor := math.Min(liquidity/10000.0, 1.0)
		score := m.Spread * math.Sqrt(m.Volume24hr) * liquidityFactor
		scoredMarkets = append(scoredMarkets, scored{market: m, score: score})
	}

	sort.Slice(scoredMarkets, func(i, j int) bool {
		return scoredMarkets[i].score > scoredMarkets[j].score
	})

	result := make([]types.MarketAllocation, len(scoredMarkets))
	for i, sm := range scoredMarkets {
		result[i] = types.MarketAllocation{
			Market:         convertToMarketInfo(sm.market),
			MaxPositionUSD: s.riskCfg.MaxPositionPerMarket,
			Score:          sm.score,
		}
	}

	return result
}

// convertToMarketInfo transforms a Gamma API response into the internal
// MarketInfo type used throughout the bot. It parses JSON-encoded token IDs,
// maps the numeric tick size to the TickSize enum, and converts string
// fields to their typed equivalents.
func convertToMarketInfo(gm GammaMarket) types.MarketInfo {
	liquidity, _ := strconv.ParseFloat(gm.Liquidity, 64)

	// Parse token IDs from JSON array string like "[\"id1\",\"id2\"]"
	var tokenIDs []string
	if gm.ClobTokenIds != "" {
		// Simple parsing for ["id1","id2"]
		var ids []string
		if err := parseJSONArray(gm.ClobTokenIds, &ids); err == nil {
			tokenIDs = ids
		}
	}

	var yesToken, noToken string
	if len(tokenIDs) >= 2 {
		yesToken = tokenIDs[0]
		noToken = tokenIDs[1]
	}

	var tickSize types.TickSize
	switch {
	case gm.OrderPriceMinTickSize == 0.1:
		tickSize = types.Tick01
	case gm.OrderPriceMinTickSize == 0.001:
		tickSize = types.Tick0001
	case gm.OrderPriceMinTickSize == 0.0001:
		tickSize = types.Tick00001
	default:
		tickSize = types.Tick001
	}

	endDate, _ := time.Parse(time.RFC3339, gm.EndDate)

	return types.MarketInfo{
		ID:               gm.ID,
		ConditionID:      gm.ConditionID,
		Slug:             gm.Slug,
		Question:         gm.Question,
		YesTokenID:       yesToken,
		NoTokenID:        noToken,
		TickSize:         tickSize,
		MinOrderSize:     gm.OrderMinSize,
		NegRisk:          gm.NegRisk,
		Active:           gm.Active,
		Closed:           gm.Closed,
		AcceptingOrders:  gm.AcceptingOrders,
		EndDate:          endDate,
		Liquidity:        liquidity,
		Volume24h:        gm.Volume24hr,
		BestBid:          gm.BestBid,
		BestAsk:          gm.BestAsk,
		Spread:           gm.Spread,
		LastTradePrice:   gm.LastTradePrice,
		RewardsMinSize:   gm.RewardsMinSize,
		RewardsMaxSpread: gm.RewardsMaxSpread,
	}
}

// parseJSONArray parses a JSON array string into a string slice.
func parseJSONArray(s string, out *[]string) error {
	return json.Unmarshal([]byte(s), out)
}
