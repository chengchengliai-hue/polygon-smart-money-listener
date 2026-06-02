package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// startCopyTradeSettlement periodically checks active copy positions for resolved markets.
func startCopyTradeSettlement() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		// Wait for initial load
		time.Sleep(30 * time.Second)

		for range ticker.C {
			checkResolvedPositions()
		}
	}()
	log.Println("[copytrade] settlement scanner started (every 5min)")
}

func checkResolvedPositions() {
	positions := getActiveCopyPositions()
	if len(positions) == 0 {
		return
	}

	// Deduplicate condition IDs to avoid redundant API calls
	conditionSet := make(map[string]bool)
	for _, p := range positions {
		if p.ConditionID != "" {
			conditionSet[p.ConditionID] = true
		}
	}

	// Fetch resolved status for each condition
	resolved := make(map[string]clobSimplifiedMarket)
	for cid := range conditionSet {
		market := fetchClobMarket(cid)
		if market != nil && market.Closed {
			resolved[cid] = *market
		}
	}

	settled := 0
	for _, pos := range positions {
		if pos.ConditionID == "" {
			continue
		}
		mkt, ok := resolved[pos.ConditionID]
		if !ok {
			continue
		}

		// Find if our token won
		var winner bool
		for _, tok := range mkt.Tokens {
			if tok.TokenID == pos.TokenID && tok.Winner {
				winner = true
				break
			}
		}

		var pnl float64
		if winner {
			// Each winning share pays $1.00
			pnl = pos.Shares - pos.TotalCost
		} else {
			// Loser shares are worthless
			pnl = -pos.TotalCost
		}
		pnl += pos.RealizedPnl // include any realized P&L from partial exits

		resolveCopyPosition(pos.ID, winner, pnl)
		insertCopyTradeLog(&CopyTradeLog{
			PositionID: pos.ID,
			Action:     "resolve",
			Price:      0,
			Shares:     pos.Shares,
			Cost:       0,
			Pnl:        pnl,
		})

		// Remove from in-memory index
		copyIndexMu.Lock()
		for k, v := range copyIndex {
			if v.ID == pos.ID {
				delete(copyIndex, k)
				break
			}
		}
		copyIndexMu.Unlock()

		log.Printf("[copytrade] resolved: id=%d market=%s %s winner=%v pnl=$%.2f",
			pos.ID, pos.MarketSlug, pos.TokenType, winner, pnl)
		settled++
	}

	if settled > 0 {
		log.Printf("[copytrade] settlement: %d positions resolved", settled)
	}
}

func fetchClobMarket(conditionID string) *clobSimplifiedMarket {
	url := fmt.Sprintf("https://clob.polymarket.com/markets/%s", conditionID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var market clobSimplifiedMarket
	if err := json.NewDecoder(resp.Body).Decode(&market); err != nil {
		return nil
	}
	return &market
}
