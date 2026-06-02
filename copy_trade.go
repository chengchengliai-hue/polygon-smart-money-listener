package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
)

const copyTradeAmount = 50.0 // fixed $50 per copy trade

var (
	copyIndex   = make(map[string]*CopyPosition) // key: "wallet|slug|tokenType"
	copyIndexMu sync.RWMutex
)

func loadCopyIndex() {
	positions := getActiveCopyPositions()
	copyIndexMu.Lock()
	for _, p := range positions {
		key := makeCopyKey(p.Wallet, p.MarketSlug, p.TokenType)
		pos := p
		copyIndex[key] = &pos
	}
	copyIndexMu.Unlock()
	log.Printf("[copytrade] loaded %d active copy positions", len(positions))
}

func makeCopyKey(wallet, marketSlug, tokenType string) string {
	return strings.ToLower(wallet) + "|" + marketSlug + "|" + strings.ToUpper(tokenType)
}

// autoCopyTrade is called from outputInformedAlert (async). Creates a virtual $50 position.
func autoCopyTrade(alert *InformedEventAlert) {
	wallet := alert.Data.MatchedWalletAddress
	marketSlug := alert.Data.MarketSlug
	tokenType := alert.Data.Outcome
	tokenID := alert.Data.TokenID
	conditionID := alert.Data.ConditionID
	marketTitle := alert.Data.MarketQuestion

	if tokenID == "" || tokenType == "" {
		return
	}

	// Determine source
	source := "risk_pool"
	for _, t := range alert.Data.Tags {
		if strings.Contains(t, "原生发现") {
			source = "native_discovery"
			break
		}
	}

	// Fetch entry price from CLOB
	price := fetchClobPrice(tokenID)
	if price <= 0 || price >= 1 {
		log.Printf("[copytrade] bad price %.4f for token %s, skip", price, tokenID)
		return
	}

	shares := copyTradeAmount / price

	pos := &CopyPosition{
		Wallet:      wallet,
		MarketSlug:  marketSlug,
		MarketTitle: marketTitle,
		TokenID:     tokenID,
		TokenType:   tokenType,
		ConditionID: conditionID,
		EntryPrice:  price,
		Shares:      shares,
		TotalCost:   copyTradeAmount,
		AlertScore:  alert.Data.RiskScore,
		AlertSource: source,
		Status:      "active",
	}

	id, err := insertCopyPosition(pos)
	if err != nil {
		log.Printf("[copytrade] insert failed: %v", err)
		return
	}
	pos.ID = id

	// Add to in-memory index
	key := makeCopyKey(wallet, marketSlug, tokenType)
	copyIndexMu.Lock()
	copyIndex[key] = pos
	copyIndexMu.Unlock()

	// Log the open action
	insertCopyTradeLog(&CopyTradeLog{
		PositionID: id,
		Action:     "open",
		Price:      price,
		Shares:     shares,
		Cost:       copyTradeAmount,
	})

	log.Printf("[copytrade] opened: wallet=%s market=%s %s price=%.4f shares=%.1f cost=$%.0f score=%d source=%s",
		wallet[:10], marketSlug, tokenType, price, shares, copyTradeAmount, alert.Data.RiskScore, source)
}

// checkCopyTrade is called from the Data API poller for every new trade.
// Detects when the source wallet sells, triggering our exit.
func checkCopyTrade(t dataTrade) {
	wallet := strings.ToLower(t.ProxyWallet)
	outcome := strings.ToUpper(t.Outcome)
	slug := t.Slug // raw slug from Data API (no "market/" prefix)

	// Try both with and without "market/" prefix
	var pos *CopyPosition
	var key string
	copyIndexMu.RLock()
	for _, prefix := range []string{"", "market/"} {
		key = makeCopyKey(wallet, prefix+slug, outcome)
		if p, ok := copyIndex[key]; ok {
			pos = p
			break
		}
	}
	copyIndexMu.RUnlock()

	if pos == nil {
		return
	}

	side := strings.ToUpper(t.Side)
	if side != "SELL" {
		return
	}

	// Source wallet is selling → we close our position
	sellPrice := t.Price
	if sellPrice <= 0 {
		sellPrice = pos.EntryPrice // fallback
	}
	proceeds := pos.Shares * sellPrice
	pnl := proceeds - pos.TotalCost

	closeCopyPosition(pos.ID, sellPrice, pnl)
	insertCopyTradeLog(&CopyTradeLog{
		PositionID: pos.ID,
		Action:     "close",
		Price:      sellPrice,
		Shares:     pos.Shares,
		Cost:       -proceeds,
		Pnl:        pnl,
		TriggerTx:  t.TransactionHash,
	})

	copyIndexMu.Lock()
	delete(copyIndex, key)
	copyIndexMu.Unlock()

	log.Printf("[copytrade] closed: wallet=%s market=%s %s exitPrice=%.4f pnl=$%.2f tx=%s",
		wallet[:10], pos.MarketSlug, outcome, sellPrice, pnl, t.TransactionHash[:10])
}

// fetchClobPrice gets the best bid price for a token from CLOB order book.
func fetchClobPrice(tokenID string) float64 {
	url := "https://clob.polymarket.com/book?token_id=" + tokenID
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[copytrade] clob book error: %v", err)
		return 0
	}
	defer resp.Body.Close()

	var book struct {
		Bids []struct {
			Price string `json:"price"`
			Size  string `json:"size"`
		} `json:"bids"`
		Asks []struct {
			Price string `json:"price"`
			Size  string `json:"size"`
		} `json:"asks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&book); err != nil {
		return 0
	}

	// Use best ask (the price we'd pay to buy immediately)
	var price float64
	if len(book.Asks) > 0 {
		fmt.Sscanf(book.Asks[0].Price, "%f", &price)
	}
	if price <= 0 && len(book.Bids) > 0 {
		// Fallback to best bid
		fmt.Sscanf(book.Bids[0].Price, "%f", &price)
	}
	return price
}
