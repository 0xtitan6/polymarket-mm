package market

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"polymarket-mm/internal/config"
	"polymarket-mm/internal/exchange"
	"polymarket-mm/pkg/types"
)

// Scanner periodically polls the Polymarket US API to discover the best
// market-making opportunities. It fetches active markets, applies keyword/slug
// filters, fetches BBO for each candidate, and ranks by spread.
//
// Since the US market list endpoint does not include inline BBO or volume data,
// the scanner fetches BBO for each filtered market to compute spreads.

// ScanResult contains markets ranked by opportunity quality.
type ScanResult struct {
	Markets   []types.MarketAllocation
	ScannedAt time.Time
}

// Scanner periodically polls the US API for wide-spread markets.
type Scanner struct {
	client  *exchange.Client    // US API client
	cfg     config.ScannerConfig // filter thresholds + poll interval
	riskCfg config.RiskConfig   // MaxMarketsActive, MaxPositionPerMarket
	logger  *slog.Logger
	resultCh chan ScanResult // engine reads selected markets from here
}

// NewScanner creates a market scanner backed by the given exchange client.
func NewScanner(client *exchange.Client, cfg config.Config, logger *slog.Logger) *Scanner {
	return &Scanner{
		client:   client,
		cfg:      cfg.Scanner,
		riskCfg:  cfg.Risk,
		logger:   logger.With("component", "scanner"),
		resultCh: make(chan ScanResult, 1),
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

	// Fetch BBO for each filtered market to get spread/pricing data
	ranked := s.rankWithBBO(ctx, filtered)

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

func (s *Scanner) fetchMarkets(ctx context.Context) ([]types.USMarket, error) {
	var allMarkets []types.USMarket
	offset := 0
	limit := 100

	active := true
	closed := false

	for {
		page, err := s.client.GetMarkets(ctx, types.MarketQueryParams{
			Active: &active,
			Closed: &closed,
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("fetch markets page offset=%d: %w", offset, err)
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
// inactive, closed, optional include filters, excluded slugs/keywords, end date
// too near or too far.
func (s *Scanner) filterMarkets(markets []types.USMarket) []types.USMarket {
	excluded := make(map[string]bool)
	for _, slug := range s.cfg.ExcludeSlugs {
		slug = strings.ToLower(strings.TrimSpace(slug))
		if slug != "" {
			excluded[slug] = true
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

	hasIncludeFilter := len(includeSlugs) > 0 || len(includeKeywords) > 0

	now := time.Now()
	maxEnd := now.AddDate(0, 0, s.cfg.MaxEndDateDays)

	var result []types.USMarket
	for _, m := range markets {
		if !m.Active || m.Closed {
			continue
		}

		slugLower := strings.ToLower(m.Slug)
		questionLower := strings.ToLower(m.Question)

		if hasIncludeFilter {
			matched := includeSlugs[slugLower]
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

		result = append(result, m)
	}

	return result
}

// rankWithBBO fetches BBO for each filtered market concurrently and ranks by spread.
// score = spread × bidDepth × askDepth (markets with wider spreads and deeper
// books are better MM opportunities).
//
// To avoid spending 40+ seconds on sequential HTTP calls when 200+ markets
// match the keyword filter, we:
//   1. Cap the number of BBO fetches to maxBBOFetches (e.g. 30).
//   2. Use a bounded worker pool (10 goroutines) for concurrency.
//   3. Apply a per-scan timeout (30s) so we never block the engine.
func (s *Scanner) rankWithBBO(ctx context.Context, markets []types.USMarket) []types.MarketAllocation {
	const (
		maxBBOFetches = 30 // don't BBO-check more than this many markets
		workerCount   = 10 // concurrent BBO fetchers
		scanTimeout   = 30 * time.Second
	)

	// Cap the candidates — prefer diversity: take a random-ish sample
	// by just truncating (they're already in API order which is effectively
	// random across market families).
	candidates := markets
	if len(candidates) > maxBBOFetches {
		s.logger.Info("capping BBO fetches", "total_filtered", len(candidates), "cap", maxBBOFetches)
		candidates = candidates[:maxBBOFetches]
	}

	type scored struct {
		market  types.USMarket
		bestBid float64
		bestAsk float64
		spread  float64
		score   float64
	}

	scanCtx, scanCancel := context.WithTimeout(ctx, scanTimeout)
	defer scanCancel()

	// Fan-out: send markets to a work channel
	workCh := make(chan types.USMarket, len(candidates))
	for _, m := range candidates {
		workCh <- m
	}
	close(workCh)

	// Fan-in: collect results
	resultCh := make(chan scored, len(candidates))

	var wg sync.WaitGroup
	for i := 0; i < workerCount && i < len(candidates); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range workCh {
				if scanCtx.Err() != nil {
					return
				}
				bbo, err := s.client.GetBBO(scanCtx, m.Slug)
				if err != nil {
					s.logger.Debug("BBO fetch failed", "slug", m.Slug, "error", err)
					continue
				}

				bid := parseFloat(bbo.MarketData.BestBid.Value)
				ask := parseFloat(bbo.MarketData.BestAsk.Value)
				if bid <= 0 || ask <= 0 || ask <= bid {
					continue
				}

				spread := ask - bid
				if spread < s.cfg.MinSpread {
					continue
				}

				depthFactor := float64(bbo.MarketData.BidDepth+bbo.MarketData.AskDepth) / 2.0
				if depthFactor < 1 {
					depthFactor = 1
				}

				resultCh <- scored{
					market:  m,
					bestBid: bid,
					bestAsk: ask,
					spread:  spread,
					score:   spread * depthFactor,
				}
			}
		}()
	}

	// Wait for all workers then close results
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var scoredMarkets []scored
	for r := range resultCh {
		scoredMarkets = append(scoredMarkets, r)
	}

	sort.Slice(scoredMarkets, func(i, j int) bool {
		return scoredMarkets[i].score > scoredMarkets[j].score
	})

	result := make([]types.MarketAllocation, len(scoredMarkets))
	for i, sm := range scoredMarkets {
		result[i] = types.MarketAllocation{
			Market:         convertToMarketInfo(sm.market, sm.bestBid, sm.bestAsk, sm.spread),
			MaxPositionUSD: s.riskCfg.MaxPositionPerMarket,
			Score:          sm.score,
		}
	}

	return result
}

// convertToMarketInfo transforms a US API USMarket into the internal
// MarketInfo type used throughout the bot. The slug serves as the primary
// identifier in the US API: ConditionID, YesTokenID, and NoTokenID are all
// set to the slug (the exchange layer routes orders by slug).
// bestBid, bestAsk, and spread are provided from the BBO fetch.
func convertToMarketInfo(m types.USMarket, bestBid, bestAsk, spread float64) types.MarketInfo {
	var tickSize types.TickSize
	switch {
	case m.OrderPriceMinTickSize == 0.1:
		tickSize = types.Tick01
	case m.OrderPriceMinTickSize == 0.001:
		tickSize = types.Tick0001
	case m.OrderPriceMinTickSize == 0.0001:
		tickSize = types.Tick00001
	default:
		tickSize = types.Tick001
	}

	endDate, _ := time.Parse(time.RFC3339, m.EndDate)

	return types.MarketInfo{
		ID:              m.ID,
		ConditionID:     m.Slug, // slug is the universal identifier in the US API
		Slug:            m.Slug,
		Question:        m.Question,
		YesTokenID:      m.Slug, // exchange layer uses slug to identify orders
		NoTokenID:       m.Slug, // single-instrument model; book tracks one side
		TickSize:        tickSize,
		MinOrderSize:    m.OrderMinSize,
		NegRisk:         false,  // always false for US API
		Active:          m.Active,
		Closed:          m.Closed,
		AcceptingOrders: true,   // if it's active and not closed, it accepts orders
		EndDate:         endDate,
		Liquidity:       m.LiquidityNum,
		Volume24h:       m.Volume24hr,
		BestBid:         bestBid,
		BestAsk:         bestAsk,
		Spread:          spread,
		LastTradePrice:  0,       // not available in market list; populated later by book data
		RewardsMinSize:   0,      // US API does not expose rewards metadata yet
		RewardsMaxSpread: 0,      // US API does not expose rewards metadata yet
	}
}

// parseFloat is a helper that parses a decimal string to float64, returning 0 on error.
func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
