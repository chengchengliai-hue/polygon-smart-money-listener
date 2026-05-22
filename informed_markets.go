package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	tokenOutcomeMap   = make(map[string]*TokenOutcome)
	tokenOutcomeMapMu sync.RWMutex
)

// Polymarket CTF contract (same on Polygon)
var ctfContractAddr = common.HexToAddress("0x4D97DCd97eC945f40cF65F87097ACe5EA0476045")

// CTF ABI for getOutcomeSlotCount + collection IDs
var ctfABI = `[{
	"inputs": [{"internalType": "bytes32","name": "","type": "bytes32"}],
	"name": "getOutcomeSlotCount",
	"outputs": [{"internalType": "uint256","name": "","type": "uint256"}],
	"stateMutability": "view","type": "function"
}]`

// High-information event categories
var highInfoCategories = map[string]bool{
	"political": true, "macro": true, "legal_regulatory": true,
	"corporate": true, "sports_injury": true, "entertainment_leak": true,
	"geopolitical": true, "crypto_regulatory": true, "tech_release": true,
	"market_resolution": true,
}

func startMarketRefresher() {
	refreshMarketsFromChain()
	go func() {
		ticker := time.NewTicker(time.Duration(informedConfig.MarketRefreshSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			refreshMarketsFromChain()
		}
	}()
}

// refreshMarketsFromChain loads markets from SQLite cache + on-chain token discovery
func refreshMarketsFromChain() {
	loadMarketsFromCache()
	refreshFromRecentTrades()
}

func loadMarketsFromCache() {
	rows, err := db.Query(`SELECT token_id, condition_id, market_slug, question, outcome, outcome_index, category, liquidity, volume, end_date FROM informed_markets WHERE updated_at > datetime('now', '-1 day')`)
	if err != nil {
		return
	}
	defer rows.Close()

	count := 0
	tokenOutcomeMapMu.Lock()
	for rows.Next() {
		var t TokenOutcome
		rows.Scan(&t.TokenID, &t.ConditionID, &t.MarketSlug, &t.Question, &t.Outcome, &t.OutcomeIndex, &t.Category, &t.Liquidity, &t.Volume, &t.EndDate)
		tokenOutcomeMap[t.TokenID] = &t
		count++
	}
	tokenOutcomeMapMu.Unlock()

	if count > 0 {
		log.Printf("[markets] loaded %d token outcomes from cache", count)
	}
}

// refreshFromRecentTrades: for each risk wallet, fetch recent Polymarket trades and cache unknown tokens
func refreshFromRecentTrades() {
	client, err := ethclient.Dial(config.HttpRpcUrl)
	if err != nil {
		return
	}
	defer client.Close()

	riskAddressSetMu.RLock()
	addrs := make([]string, 0, len(riskAddressSet))
	for _, entry := range riskAddressSet {
		addrs = append(addrs, entry.RootAddress)
	}
	riskAddressSetMu.RUnlock()

	if len(addrs) == 0 {
		return
	}

	ctfAddr := common.HexToAddress(informedConfig.CtfExchange)
	negRiskAddr := common.HexToAddress(informedConfig.NegRiskExchange)
	currentBlock, _ := client.BlockNumber(context.Background())
	fromBlock := currentBlock - 5000 // last ~17 hours

	// Fetch recent OrderFilled events for these exchanges
	query := ethereum.FilterQuery{
		Addresses: []common.Address{ctfAddr, negRiskAddr},
		Topics:    [][]common.Hash{{orderFilledTopic, ordersMatchedTopic}},
		FromBlock: new(big.Int).SetUint64(fromBlock),
		ToBlock:   new(big.Int).SetUint64(currentBlock),
	}

	logs, err := client.FilterLogs(context.Background(), query)
	if err != nil {
		return
	}

	newTokens := 0
	for _, vLog := range logs {
		trade := decodeTrade(vLog)
		if trade == nil {
			continue
		}

		// Cache unknown token IDs with basic info
		for _, tokenID := range []string{trade.MakerAssetID, trade.TakerAssetID} {
			tokenOutcomeMapMu.RLock()
			_, exists := tokenOutcomeMap[tokenID]
			tokenOutcomeMapMu.RUnlock()

			if !exists {
				// Create basic entry for this token
				// TokenID encodes conditionId + outcome index
				condID, outcomeIdx := parseTokenID(tokenID)
				if condID != "" {
					t := &TokenOutcome{
						TokenID:      tokenID,
						ConditionID:  condID,
						Outcome:      determineOutcomeFromIndex(outcomeIdx),
						OutcomeIndex: outcomeIdx,
						Category:     "market_resolution",
					}
					tokenOutcomeMapMu.Lock()
					tokenOutcomeMap[tokenID] = t
					tokenOutcomeMapMu.Unlock()
					saveInformedMarket(t)
					newTokens++
				}
			}
		}
	}

	if newTokens > 0 {
		log.Printf("[markets] on-chain: cached %d new token outcomes from recent trades", newTokens)
	}
}

// parseTokenID extracts conditionId and outcome index from a CTF token ID
// CTF token IDs: uint256(conditionId) << 1 | outcomeIndex
func parseTokenID(tokenID string) (string, int) {
	n := new(big.Int)
	n.SetString(tokenID, 10)
	if n == nil {
		return "", 0
	}
	outcomeIdx := int(new(big.Int).And(n, big.NewInt(1)).Int64())
	conditionIDBig := new(big.Int).Rsh(n, 1)
	conditionID := fmt.Sprintf("%064x", conditionIDBig)
	return "0x" + conditionID, outcomeIdx
}

func determineOutcomeFromIndex(idx int) string {
	if idx == 0 {
		return "YES"
	}
	return "NO"
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
	case strings.Contains(cat, "entertain") || strings.Contains(cat, "oscar") || strings.Contains(cat, "grammy"):
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

