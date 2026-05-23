package main

import (
	"context"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Entity trade aggregation (key: rootEOA, value: total USD in window)
var (
	rootTradeWindows   = make(map[string]*rootTradeAgg)
	rootTradeWindowsMu sync.Mutex
)

type rootTradeAgg struct {
	TotalUsd    float64
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

const entityWindowSec = 300 // 5 minutes for entity-level aggregation

func scoreInformedEvent(matched *MatchedTrade) InformedScoredEvent {
	score := 0
	var tags []string

	// ── Step 1: Mixer detection (funders from Tornado Cash etc) ──
	// Check ALL funders of the original whale (not just current trade)
	// If any funder is a mixer → massive bonus
	rootAddr := matched.RootAddress
	if rootAddr != "" {
		func() {
			rows, err := db.Query(`SELECT DISTINCT primary_funder_address FROM whale_alerts WHERE address = ?`, rootAddr)
			if err != nil {
				return
			}
			defer rows.Close()
			for rows.Next() {
				var funder string
				rows.Scan(&funder)
				if isMixer(funder) {
					score += 50
					tags = append(tags, "Mixer Funded")
					return
				}
			}
		}()
	}

	// ── Step 2: Base: risk wallet matched Polymarket trade → +40 ──
	score += 40
	tags = append(tags, "Polymarket Trade")

	// ── Step 3: Proxy/Safe/Deposit confirmation ──
	switch matched.MatchedWalletType {
	case WalletPolyProxy, WalletGnosisSafe, WalletDeposit:
		score += 10
		tags = append(tags, "Proxy Wallet Match")
	case WalletEOA:
		if matched.MatchedWallet != matched.RootAddress {
			tags = append(tags, "Proxy Unknown")
		}
	}

	// ── Step 4: High-information market → +20 ──
	if matched.TokenOutcome != nil && isHighInfoCategory(matched.TokenOutcome.Category) {
		score += 20
		tags = append(tags, "High Information Market")
	}

	// ── Step 5: Entity-level 5-min aggregation ──
	estimatedUsdc := matched.TakerAmount
	if estimatedUsdc < matched.MakerAmount {
		estimatedUsdc = matched.MakerAmount
	}

	// Aggregate all trades from this root EOA + all linked wallets in the last 5 min
	rootTradeWindowsMu.Lock()
	agg, exists := rootTradeWindows[rootAddr]
	now := time.Now()
	if !exists || now.Sub(agg.FirstSeenAt) > time.Duration(entityWindowSec)*time.Second {
		agg = &rootTradeAgg{TotalUsd: 0, FirstSeenAt: now, LastSeenAt: now}
		rootTradeWindows[rootAddr] = agg
	}
	agg.TotalUsd += estimatedUsdc
	agg.LastSeenAt = now
	rootTradeWindowsMu.Unlock()

	// Single trade OR aggregated total >= $5K → +20
	if estimatedUsdc >= informedConfig.MinTradeUsdc || agg.TotalUsd >= informedConfig.MinTradeUsdc {
		if agg.TotalUsd >= 2*informedConfig.MinTradeUsdc && estimatedUsdc < informedConfig.MinTradeUsdc {
			tags = append(tags, "Split Order Aggregation")
		}
		score += 20
		tags = append(tags, "Large Directional Buy")
	}

	// ── Step 6: Clear direction → +10 ──
	if matched.Direction != "unknown" {
		score += 10
	}

	// ── Step 7: YES/NO hedging → -50 ──
	isHedged := detectHedge(matched)
	if isHedged {
		score += informedConfig.HedgePenalty
		tags = append(tags, "Hedged / Arbitrage Pattern")
	}

	// ── Step 8: Determine severity ──
	severity := "watch"
	if score >= informedConfig.HighThreshold {
		severity = "high"
	} else if score >= informedConfig.AlertThreshold {
		severity = "normal"
	}

	// ── Step 9: GC old aggregation windows ──
	rootTradeWindowsMu.Lock()
	for k, v := range rootTradeWindows {
		if now.Sub(v.LastSeenAt) > time.Duration(entityWindowSec)*time.Second {
			delete(rootTradeWindows, k)
		}
	}
	rootTradeWindowsMu.Unlock()

	return InformedScoredEvent{
		MatchedTrade: *matched,
		RiskScore:    score,
		Tags:         tags,
		Severity:     severity,
		IsHedged:     isHedged,
	}
}

// scoreNativeDiscovery checks if a Polymarket trade not in risk pool is suspicious
// Targets: new wallet (nonce≤1) + high-info market + ≥$5K → potential insider
func scoreNativeDiscovery(trade *DecodedTrade, client *ethclient.Client) *InformedScoredEvent {
	// Check maker first, then taker
	addr := trade.Maker
	assetID := trade.TakerAssetID
	role := "maker"

	// If maker is a known address (contract/CEX), try taker
	if isWhitelisted(addr) {
		addr = trade.Taker
		assetID = trade.MakerAssetID
		role = "taker"
	}

	// Skip whitelisted
	if isWhitelisted(addr) {
		return nil
	}

	// Check: nonce ≤ 1?
	nonce := getNonceVal(client, addr)
	if nonce == nil || *nonce > 1 {
		return nil
	}

	// Check: high-information market?
	tokenOutcome := lookupTokenOutcome(assetID)
	if tokenOutcome == nil || !isHighInfoCategory(tokenOutcome.Category) {
		return nil
	}

	// Check: amount ≥ $5K?
	amount := trade.TakerAmount
	if trade.MakerAmount > amount {
		amount = trade.MakerAmount
	}
	if amount < informedConfig.MinTradeUsdc {
		return nil
	}

	// All conditions met → Polymarket Native Discovery
	direction := determineDirection(assetID, "BUY")
	score := 70 // base: new wallet + high info market + large buy
	tags := []string{"Polymarket Native Discovery", "Fresh Wallet", "High Information Market", "Large Directional Buy"}

	severity := "normal"
	if score >= informedConfig.HighThreshold {
		severity = "high"
	}

	return &InformedScoredEvent{
		MatchedTrade: MatchedTrade{
			DecodedTrade:      *trade,
			MatchedWallet:     addr,
			MatchedWalletType: WalletEOA,
			MatchedRole:       role,
			RootAddress:       addr,
			TokenOutcome:      tokenOutcome,
			Action:            "BUY",
			Direction:         direction,
		},
		RiskScore: score,
		Tags:      tags,
		Severity:  severity,
		IsHedged:  false,
	}
}

func getNonceVal(client *ethclient.Client, addr string) *int64 {
	if client == nil {
		return nil
	}
	address := common.HexToAddress(addr)
	nonce, err := client.NonceAt(context.Background(), address, nil)
	if err != nil {
		return nil
	}
	n := int64(nonce)
	return &n
}
