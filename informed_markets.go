package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	tokenOutcomeMap   = make(map[string]*TokenOutcome)
	tokenOutcomeMapMu sync.RWMutex
)

// High-information event categories
var highInfoCategories = map[string]bool{
	"political":            true,
	"macro":                true,
	"legal_regulatory":     true,
	"corporate":            true,
	"sports_injury":        true,
	"entertainment_leak":   true,
	"geopolitical":         true,
	"crypto_regulatory":    true,
	"tech_release":         true,
	"market_resolution":    true,
}

func startMarketRefresher() {
	refreshMarkets()
	go func() {
		ticker := time.NewTicker(time.Duration(informedConfig.MarketRefreshSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			refreshMarkets()
		}
	}()
}

func refreshMarkets() {
	events, err := fetchGammaEvents()
	if err != nil {
		log.Printf("[markets] gamma fetch failed: %v", err)
		return
	}

	newMap := make(map[string]*TokenOutcome)

	for _, evt := range events {
		cat := normalizeCategory(evt.Category)

		// Only cache high-information markets
		if !highInfoCategories[cat] {
			continue
		}

		liquidity, _ := strconv.ParseFloat(evt.Liquidity, 64)
		if liquidity < informedConfig.MinLiquidity {
			continue
		}

		for _, mkt := range evt.Markets {
			if len(mkt.ClobTokenIDs) < 2 {
				continue
			}

			yesToken := &TokenOutcome{
				TokenID:      mkt.ClobTokenIDs[0],
				ConditionID:  mkt.ConditionID,
				MarketSlug:   evt.Slug,
				Question:     mkt.Question,
				Outcome:      "YES",
				OutcomeIndex: 0,
				Category:     cat,
				Liquidity:    liquidity,
				EndDate:      evt.EndDate,
			}
			newMap[yesToken.TokenID] = yesToken

			noToken := &TokenOutcome{
				TokenID:      mkt.ClobTokenIDs[1],
				ConditionID:  mkt.ConditionID,
				MarketSlug:   evt.Slug,
				Question:     mkt.Question,
				Outcome:      "NO",
				OutcomeIndex: 1,
				Category:     cat,
				Liquidity:    liquidity,
				EndDate:      evt.EndDate,
			}
			newMap[noToken.TokenID] = noToken

			// Persist to DB
			saveInformedMarket(yesToken)
			saveInformedMarket(noToken)
		}
	}

	tokenOutcomeMapMu.Lock()
	tokenOutcomeMap = newMap
	tokenOutcomeMapMu.Unlock()

	log.Printf("[markets] cached %d high-info token outcomes", len(newMap))
}

func fetchGammaEvents() ([]GammaEvent, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	url := informedConfig.GammaBaseURL + "/events?limit=200&closed=false"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var events []GammaEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	return events, nil
}

func lookupTokenOutcome(tokenID string) *TokenOutcome {
	tokenOutcomeMapMu.RLock()
	defer tokenOutcomeMapMu.RUnlock()
	return tokenOutcomeMap[tokenID]
}

func isHighInfoCategory(cat string) bool {
	return highInfoCategories[normalizeCategory(cat)]
}

func normalizeCategory(cat string) string {
	cat = strings.ToLower(strings.TrimSpace(cat))
	switch {
	case strings.Contains(cat, "politic") || strings.Contains(cat, "elect"):
		return "political"
	case strings.Contains(cat, "fed") || strings.Contains(cat, "cpi") || strings.Contains(cat, "rate") || strings.Contains(cat, "macro") || strings.Contains(cat, "employ"):
		return "macro"
	case strings.Contains(cat, "sec") || strings.Contains(cat, "court") || strings.Contains(cat, "legal") || strings.Contains(cat, "regulat"):
		return "legal_regulatory"
	case strings.Contains(cat, "sports") || strings.Contains(cat, "injur") || strings.Contains(cat, "nba") || strings.Contains(cat, "nfl"):
		return "sports_injury"
	case strings.Contains(cat, "crypto") || strings.Contains(cat, "etf"):
		return "crypto_regulatory"
	case strings.Contains(cat, "entertainment") || strings.Contains(cat, "oscar") || strings.Contains(cat, "grammy"):
		return "entertainment_leak"
	case strings.Contains(cat, "geo") || strings.Contains(cat, "war") || strings.Contains(cat, "sanction"):
		return "geopolitical"
	case strings.Contains(cat, "merger") || strings.Contains(cat, "bankrupt") || strings.Contains(cat, "ceo") || strings.Contains(cat, "earning") || strings.Contains(cat, "corp"):
		return "corporate"
	case strings.Contains(cat, "ai") || strings.Contains(cat, "chip") || strings.Contains(cat, "release") || strings.Contains(cat, "product"):
		return "tech_release"
	default:
		return cat
	}
}

func saveInformedMarket(t *TokenOutcome) {
	db.Exec(
		`INSERT OR REPLACE INTO informed_markets (token_id, condition_id, market_slug, question, outcome, outcome_index, category, liquidity, volume, end_date, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		t.TokenID, t.ConditionID, t.MarketSlug, t.Question, t.Outcome, t.OutcomeIndex, t.Category, t.Liquidity, t.Volume, t.EndDate,
	)
}

