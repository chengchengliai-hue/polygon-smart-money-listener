package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
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

// refreshMarketsFromChain loads markets from SQLite cache + Gamma API + on-chain discovery
func refreshMarketsFromChain() {
	loadMarketsFromCache()

	// Try Gamma API via proxy
	gammaEvents, err := fetchGammaEvents()
	if err == nil && len(gammaEvents) > 0 {
		newMap := make(map[string]*TokenOutcome)
		for _, evt := range gammaEvents {
			if evt.Closed {
				continue
			}
			// Extract category from tags
			cat := ""
			for _, tag := range evt.Tags {
				cat = normalizeCategory(tag.Label)
				if cat != "" && highInfoCategories[cat] {
					break
				}
			}
			if cat == "" {
				cat = normalizeCategory(evt.Slug)
			}
			if !highInfoCategories[cat] {
				continue
			}
			for _, mkt := range evt.Markets {
				clobIDs := parseJSONStringArray(mkt.ClobTokenIDsRaw)
				if len(clobIDs) < 2 {
					continue
				}
				yesT := &TokenOutcome{TokenID: clobIDs[0], ConditionID: mkt.ConditionID, MarketSlug: evt.Slug, Question: mkt.Question, Outcome: "YES", OutcomeIndex: 0, Category: cat}
				noT := &TokenOutcome{TokenID: clobIDs[1], ConditionID: mkt.ConditionID, MarketSlug: evt.Slug, Question: mkt.Question, Outcome: "NO", OutcomeIndex: 1, Category: cat}
					newMap[yesT.TokenID] = yesT
					newMap[noT.TokenID] = noT
					saveInformedMarket(yesT)
					saveInformedMarket(noT)
				}
			}
			tokenOutcomeMapMu.Lock()
			tokenOutcomeMap = newMap
			tokenOutcomeMapMu.Unlock()
			log.Printf("[markets] gamma: cached %d high-info token outcomes", len(newMap))
		} else if err != nil {
		log.Printf("[markets] gamma unavailable: %v", err)
	}

	// On-chain fallback
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

	allLinkedAddressesMu.RLock()
	addrs := make([]string, 0, len(riskEoaPool))
	for _, entry := range riskEoaPool {
		addrs = append(addrs, entry.RootAddresses[0])
	}
	allLinkedAddressesMu.RUnlock()

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

// parseJSONStringArray parses Polymarket Gamma API's string-encoded JSON arrays
// e.g. "[\"Yes\", \"No\"]" → ["Yes", "No"]
func parseJSONStringArray(raw string) []string {
	if raw == "" {
		return nil
	}
	var result []string
	json.Unmarshal([]byte(raw), &result)
	return result
}

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
	case strings.Contains(cat, "geo") || strings.Contains(cat, "war") || strings.Contains(cat, "sanction") || strings.Contains(cat, "india") || strings.Contains(cat, "china") || strings.Contains(cat, "uk") || strings.Contains(cat, "france") || strings.Contains(cat, "military"):
		return "geopolitical"
	case strings.Contains(cat, "politic") || strings.Contains(cat, "elect"):
		return "political"
	case strings.Contains(cat, "fed") || strings.Contains(cat, "cpi") || strings.Contains(cat, "rate") || strings.Contains(cat, "macro") || strings.Contains(cat, "employ") || strings.Contains(cat, "economy") || strings.Contains(cat, "finance"):
		return "macro"
	case strings.Contains(cat, "sec") || strings.Contains(cat, "court") || strings.Contains(cat, "legal") || strings.Contains(cat, "regulat"):
		return "legal_regulatory"
	case strings.Contains(cat, "sports") || strings.Contains(cat, "injur") || strings.Contains(cat, "nba") || strings.Contains(cat, "nfl") || strings.Contains(cat, "soccer") || strings.Contains(cat, "ufc"):
		return "sports_injury"
	case strings.Contains(cat, "crypto") || strings.Contains(cat, "etf") || strings.Contains(cat, "bitcoin"):
		return "crypto_regulatory"
	case strings.Contains(cat, "entertain") || strings.Contains(cat, "oscar") || strings.Contains(cat, "grammy"):
		return "entertainment_leak"
	case strings.Contains(cat, "merger") || strings.Contains(cat, "bankrupt") || strings.Contains(cat, "ceo") || strings.Contains(cat, "earning") || strings.Contains(cat, "corp") || strings.Contains(cat, "ipo") || strings.Contains(cat, "business") || strings.Contains(cat, "stock"):
		return "corporate"
	case strings.Contains(cat, "ai") || strings.Contains(cat, "chip") || strings.Contains(cat, "release") || strings.Contains(cat, "product") || strings.Contains(cat, "tech"):
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




func fetchGammaEvents() ([]GammaEvent, error) {
	req, err := http.NewRequest("GET", informedConfig.GammaBaseURL+"/events?limit=200&closed=false", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
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
