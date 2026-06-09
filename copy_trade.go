package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const copyTradeAmount = 5.0        // base $5 per copy trade
const copyTradeBoostAmount = 10.0   // $10 when market closes within 24h
const minShares = 5.0               // Polymarket CLOB minimum order size
const sellSlippageAllowance = 0.97  // accept 97% of best bid as sell limit

var (
	copyIndex   = make(map[string]*CopyPosition)
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

// autoCopyTrade places a real FOK order first, then creates a virtual position
// only if the fill succeeded (or live trading is off).
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

	source := "risk_pool"
	for _, t := range alert.Data.Tags {
		if strings.Contains(t, "原生发现") {
			source = "native_discovery"
			break
		}
	}

	// ── Step 1: determine trade amount (boost near settlement) ──
	tradeAmount := copyTradeAmount
	if ed := fetchEndDate(tokenID); ed != "" {
		if h := hoursToEnd(ed); h >= 0 && h < 24 {
			tradeAmount = copyTradeBoostAmount
		}
	}

	// ── Step 2: get FPMM market price ──
	fpmmPrice := fetchFPMMPrice(conditionID, tokenType)
	if fpmmPrice <= 0 || fpmmPrice >= 1 {
		log.Printf("[copytrade] no FPMM price for token %s, skip", tokenID)
		return
	}
	if fpmmPrice > 0.95 {
		log.Printf("[copytrade] fpmm %.4f > 0.95, risk/reward too poor, skip", fpmmPrice)
		return
	}

	estShares := tradeAmount / fpmmPrice

	// Ensure minimum 5 shares
	if estShares < minShares {
		tradeAmount = fpmmPrice * minShares * 1.05
		estShares = tradeAmount / fpmmPrice
		log.Printf("[copytrade] bumped budget to $%.2f for min %d shares", tradeAmount, int(minShares))
	}

	// Limit order at FPMM + 2% to ensure fill against AMM
	limitPrice := fpmmPrice * 1.02

	// ── Step 3: place real limit order ──
	result := clobBuy(tokenID, limitPrice, tradeAmount, estShares)

	// ── Step 3: use real fill data if live; otherwise use estimate ──
	entryPrice := fpmmPrice
	entryShares := estShares
	realOrderID := ""
	realFilledPrice := 0.0
	realFilledShares := 0.0

	if liveTrading {
		if !result.Filled {
			return // real order didn't fill — no position
		}
		entryPrice = result.FilledPrice
		entryShares = result.FilledShares
		realOrderID = result.OrderID
		realFilledPrice = result.FilledPrice
		realFilledShares = result.FilledShares
	}

	// ── Step 4: add to existing position or create new one ──
	totalCost := entryPrice * entryShares
	key := makeCopyKey(wallet, marketSlug, tokenType)

	copyIndexMu.Lock()
	existing := copyIndex[key]
	if existing != nil {
		// Add to existing position — update weighted average entry price
		oldCost := existing.TotalCost
		oldShares := existing.Shares
		newShares := oldShares + entryShares
		newCost := oldCost + totalCost
		newEntryPrice := newCost / newShares

		existing.Shares = newShares
		existing.TotalCost = newCost
		existing.EntryPrice = newEntryPrice
		existing.AlertScore = alert.Data.RiskScore // latest score
		existing.UpdatedAt = time.Now().Format(time.RFC3339)
		copyIndexMu.Unlock()

		addToCopyPosition(existing.ID, entryShares, totalCost)
		insertCopyTradeLog(&CopyTradeLog{
			PositionID: existing.ID,
			Action:     "add",
			Price:      entryPrice,
			Shares:     entryShares,
			Cost:       totalCost,
		})

		log.Printf("[copytrade] added: wallet=%s market=%s %s +%.1f shares (now %.1f) cost=+$%.2f (now $%.2f) price=%.4f",
			wallet[:10], marketSlug, tokenType, entryShares, newShares, totalCost, newCost, entryPrice)
		return
	}
	copyIndexMu.Unlock()

	pos := &CopyPosition{
		Wallet:           wallet,
		MarketSlug:       marketSlug,
		MarketTitle:      marketTitle,
		TokenID:          tokenID,
		TokenType:        tokenType,
		ConditionID:      conditionID,
		EntryPrice:       entryPrice,
		Shares:           entryShares,
		TotalCost:        totalCost,
		AlertScore:       alert.Data.RiskScore,
		AlertSource:      source,
		Status:           "active",
		RealOrderID:      realOrderID,
		RealFilledPrice:  realFilledPrice,
		RealFilledShares: realFilledShares,
	}

	id, err := insertCopyPosition(pos)
	if err != nil {
		log.Printf("[copytrade] insert failed: %v", err)
		return
	}
	pos.ID = id

	copyIndexMu.Lock()
	copyIndex[key] = pos
	copyIndexMu.Unlock()

	insertCopyTradeLog(&CopyTradeLog{
		PositionID: id,
		Action:     "open",
		Price:      entryPrice,
		Shares:     entryShares,
		Cost:       totalCost,
	})

	log.Printf("[copytrade] opened: wallet=%s market=%s %s price=%.4f shares=%.1f cost=$%.2f score=%d source=%s live=%v",
		wallet[:10], marketSlug, tokenType, entryPrice, entryShares, totalCost, alert.Data.RiskScore, source, liveTrading)
}

// checkCopyTrade is called from the Data API poller when the source wallet trades.
func checkCopyTrade(t dataTrade) {
	wallet := strings.ToLower(t.ProxyWallet)
	outcome := strings.ToUpper(t.Outcome)
	slug := t.Slug

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

	// ── Step 1: resolve sell limit price (FPMM market price with slippage) ──
	sellLimitPrice := fetchFPMMPrice(pos.ConditionID, pos.TokenType)
	if sellLimitPrice <= 0 {
		sellLimitPrice = t.Price // fallback to trade price
	}
	if sellLimitPrice < 0.001 {
		log.Printf("[copytrade] sell price %.4f too low, keeping active: wallet=%s", sellLimitPrice, wallet[:10])
		return
	}
	sellLimitPrice = sellLimitPrice * sellSlippageAllowance

	// ── Step 2: sell shares ──
	sellShares := pos.Shares
	if pos.RealFilledShares > 0 {
		sellShares = pos.RealFilledShares
	}

	// ── Step 3: try real sell order ──
	exitResult := clobSell(pos.TokenID, sellLimitPrice, sellShares)
	if liveTrading {
		if !exitResult.Filled {
			log.Printf("[copytrade] real sell unfilled, keeping active: wallet=%s limit=%.4f",
				wallet[:10], sellLimitPrice)
			return
		}
		sellShares = exitResult.FilledShares
		sellLimitPrice = exitResult.FilledPrice
	}

	// ── Step 4: close virtual position ──
	proceeds := sellShares * sellLimitPrice
	pnl := proceeds - pos.TotalCost

	closeCopyPosition(pos.ID, sellLimitPrice, pnl)
	if exitResult.OrderID != "" {
		updateCopySellOrder(pos.ID, exitResult.OrderID, sellLimitPrice)
	}

	insertCopyTradeLog(&CopyTradeLog{
		PositionID: pos.ID,
		Action:     "close",
		Price:      sellLimitPrice,
		Shares:     sellShares,
		Cost:       -proceeds,
		Pnl:        pnl,
		TriggerTx:  t.TransactionHash,
	})

	copyIndexMu.Lock()
	delete(copyIndex, key)
	copyIndexMu.Unlock()

	log.Printf("[copytrade] closed: wallet=%s market=%s %s exitPrice=%.4f pnl=$%.2f tx=%s live=%v",
		wallet[:10], pos.MarketSlug, outcome, sellLimitPrice, pnl, t.TransactionHash[:10], liveTrading)
}

// ── Order book helpers ──

type clobBookEntry struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type clobBook struct {
	Bids []clobBookEntry `json:"bids"`
	Asks []clobBookEntry `json:"asks"`
}

func fetchOrderBook(tokenID string) *clobBook {
	url := "https://clob.polymarket.com/book?token_id=" + tokenID
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var book clobBook
	if err := json.NewDecoder(resp.Body).Decode(&book); err != nil {
		return nil
	}
	return &book
}

// fetchClobVWAP walks ask-side depth. Returns (vwap, shares, lastAskPrice, ok).
// lastAskPrice = deepest eaten ask level, used as FOK limit price.
func fetchClobVWAP(tokenID string, budget float64) (float64, float64, float64, bool) {
	book := fetchOrderBook(tokenID)
	if book == nil || len(book.Asks) == 0 {
		return 0, 0, 0, false
	}

	var totalSpent, totalShares float64
	var lastAskPrice float64

	for _, ask := range book.Asks {
		var price, size float64
		fmt.Sscanf(ask.Price, "%f", &price)
		fmt.Sscanf(ask.Size, "%f", &size)
		if price <= 0 || size <= 0 || price >= 1 {
			continue
		}

		lastAskPrice = price

		cost := price * size
		if totalSpent+cost >= budget {
			remain := budget - totalSpent
			totalShares += remain / price
			totalSpent += remain
			// lastAskPrice = price of the level that filled the budget
			break
		}
		totalShares += size
		totalSpent += cost
	}

	if totalShares <= 0 || totalSpent <= 0 || lastAskPrice <= 0 {
		return 0, 0, 0, false
	}

	return totalSpent / totalShares, totalShares, lastAskPrice, true
}

// fetchClobBidVWAP walks bid-side depth for exit price estimation (backtest only).
func fetchClobBidVWAP(tokenID string, sharesToSell float64) (float64, float64, bool) {
	book := fetchOrderBook(tokenID)
	if book == nil || len(book.Bids) == 0 {
		return 0, 0, false
	}

	var totalProceeds, totalShares float64
	for _, bid := range book.Bids {
		var price, size float64
		fmt.Sscanf(bid.Price, "%f", &price)
		fmt.Sscanf(bid.Size, "%f", &size)
		if price <= 0 || size <= 0 || price >= 1 {
			continue
		}
		if totalShares+size >= sharesToSell {
			remain := sharesToSell - totalShares
			totalShares += remain
			totalProceeds += remain * price
			break
		}
		totalShares += size
		totalProceeds += size * price
	}
	if totalShares <= 0 || totalProceeds <= 0 {
		return 0, 0, false
	}
	return totalProceeds / totalShares, totalProceeds, true
}

// fetchFPMMPrice gets the market price from CLOB /markets endpoint (FPMM-derived).
func tokenNames(tokens []clobMarketToken) []string {
	var s []string
	for _, t := range tokens {
		s = append(s, t.Outcome)
	}
	return s
}

func fetchFPMMPrice(conditionID string, outcome string) float64 {
	if conditionID == "" {
		return 0
	}
	url := "https://clob.polymarket.com/markets/" + conditionID

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[copytrade] FPMM fetch attempt %d failed: %v", attempt+1, err)
			continue
		}

		var market struct {
			Tokens []clobMarketToken `json:"tokens"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&market); err != nil {
			resp.Body.Close()
			log.Printf("[copytrade] FPMM decode attempt %d failed: %v", attempt+1, err)
			continue
		}
		resp.Body.Close()

		target := strings.ToUpper(strings.TrimSpace(outcome))
		for _, t := range market.Tokens {
			if strings.EqualFold(strings.TrimSpace(t.Outcome), target) && t.Price > 0 {
				log.Printf("[copytrade] FPMM %s=%.4f (attempt %d)", outcome, t.Price, attempt+1)
				return t.Price
			}
		}
		log.Printf("[copytrade] FPMM no match: outcome=%q available=%v", target, tokenNames(market.Tokens))
		return 0 // got response but no matching outcome — don't retry
	}
	return 0
}
