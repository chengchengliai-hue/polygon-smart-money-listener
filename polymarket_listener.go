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

var dataAPIKey = ""
var polymarketLastTimestamp int64

func init() {
	dataAPIKey = getEnv("POLYMARKET_DATA_API_KEY", "")
}

type dataTrade struct {
	ProxyWallet     string  `json:"proxyWallet"`
	Side            string  `json:"side"`
	Price           float64 `json:"price"`
	Size            float64 `json:"size"`
	Title           string  `json:"title"`
	Slug            string  `json:"slug"`
	EventSlug       string  `json:"eventSlug"`
	Outcome         string  `json:"outcome"`
	OutcomeIndex    int     `json:"outcomeIndex"`
	Asset           string  `json:"asset"`
	ConditionID     string  `json:"conditionId"`
	TransactionHash string  `json:"transactionHash"`
	Timestamp       int64   `json:"timestamp"`
}

func startPolymarketListener() {
	if dataAPIKey == "" {
		log.Printf("[polymarket] no DATA_API_KEY, listener disabled")
		return
	}
	go func() {
		for {
			err := runDataAPIPoller()
			if err != nil {
				log.Printf("[polymarket] data api error: %v, retrying in 10s...", err)
				time.Sleep(10 * time.Second)
			}
		}
	}()
}

func runDataAPIPoller() error {
	if polymarketLastTimestamp == 0 {
		polymarketLastTimestamp = time.Now().Unix() - 600 // 10min buffer for API delay
	}
	log.Printf("[polymarket] data api poller started from ts=%d", polymarketLastTimestamp)

	var eventCount, matchedCount, alertedCount uint64
	var rawCount, hashSkipCount, sideSkipCount uint64
	lastDebug := time.Now()

	for {
		url := fmt.Sprintf("https://data-api.polymarket.com/trades?limit=1000&apiKey=%s&_t=%d", dataAPIKey, time.Now().UnixMilli())
		resp, err := http.Get(url)
		if err != nil {
			return fmt.Errorf("http get: %w", err)
		}

		var trades []dataTrade
		if err := json.NewDecoder(resp.Body).Decode(&trades); err != nil {
			resp.Body.Close()
			return fmt.Errorf("decode: %w", err)
		}
		resp.Body.Close()

		for _, t := range trades {
			rawCount++
			// Precise dedup by tx hash
			if isPolymarketEventSeen(t.TransactionHash) {
				hashSkipCount++
				continue
			}
			markPolymarketEventSeen(t.TransactionHash, t.TransactionHash, 0, 0)
			eventCount++

			// Check tracked positions for exit (before side filter, catches SELL too)
			checkTrackedExit(t)

			side := strings.ToUpper(t.Side)
			if side != "BUY" {
				sideSkipCount++
				continue
			}

			notional := t.Price * t.Size

			// Check crypto short-term accumulation
			checkAccumulation(t.ProxyWallet, t.Slug, t.Title, t.Outcome, side, notional, t.Price)
			if notional < informedConfig.MinTradeUsdc {
				continue
			}

			entries := lookupRiskWallet(t.ProxyWallet)
			if len(entries) == 0 {
				isNew, ageHours := checkNewWallet(t.ProxyWallet)
				if isNew {
					tokenOutcome := &TokenOutcome{
						TokenID:      t.Asset,
						ConditionID:  t.ConditionID,
						Outcome:      strings.ToUpper(t.Outcome),
						OutcomeIndex: t.OutcomeIndex,
						Question:     t.Title,
						MarketSlug:   "market/" + t.Slug,
					}
					dirLabel := directionLabel(t.Outcome, side)
					trade := DecodedTrade{
						TxHash:       t.TransactionHash,
						Maker:        t.ProxyWallet,
						TakerAssetID: t.Asset,
						MakerAmount:  notional,
						TakerAmount:  notional,
					}
					native := MatchedTrade{
						DecodedTrade:      trade,
						MatchedWallet:     t.ProxyWallet,
						MatchedWalletType: WalletEOA,
						MatchedRole:       "maker",
						RootAddress:       t.ProxyWallet,
						TokenOutcome:      tokenOutcome,
						Action:            side,
						Direction:         dirLabel,
					}
					nativeScore := 70
					nativeTags := []string{"原生发现(+70)", "大额定向(+20)"}
					if ageHours < 2 {
						nativeScore += 15
						nativeTags = append(nativeTags, "数小时内新建(+15)")
					} else if ageHours < 12 {
						nativeScore += 5
						nativeTags = append(nativeTags, "近期新建(+5)")
					}
					if ed := fetchEndDate(t.Asset); ed != "" {
						if h := hoursToEnd(ed); h >= 0 {
							if h < 2 {
								nativeScore += 25
								nativeTags = append(nativeTags, "临期入场(+25)")
							} else if h < 24 {
								nativeScore += 15
								nativeTags = append(nativeTags, "临近结算(+15)")
							}
						}
					}
					scored := InformedScoredEvent{
						MatchedTrade: native,
						RiskScore:    nativeScore,
						Tags:         nativeTags,
						Severity:     "normal",
					}
					if scored.RiskScore >= informedConfig.AlertThreshold {
						alertedCount++
						outputInformedAlert(scored)
					}
				}
				continue
			}

			matchedCount++
			entry := entries[0]

			tokenOutcome := &TokenOutcome{
				TokenID:      t.Asset,
				ConditionID:  t.ConditionID,
				Outcome:      strings.ToUpper(t.Outcome),
				OutcomeIndex: t.OutcomeIndex,
				Question:     t.Title,
				MarketSlug:   "market/" + t.Slug,
			}

			dirLabel := directionLabel(t.Outcome, side)

			trade := DecodedTrade{
				TxHash:       t.TransactionHash,
				Maker:        t.ProxyWallet,
				TakerAssetID: t.Asset,
				MakerAmount:  notional,
				TakerAmount:  notional,
			}
			matched := MatchedTrade{
				DecodedTrade:      trade,
				MatchedWallet:     t.ProxyWallet,
				MatchedWalletType: WalletEOA,
				MatchedRole:       "maker",
				RootAddress:       entry.RootAddresses[0],
				TokenOutcome:      tokenOutcome,
				Action:            side,
				Direction:         dirLabel,
			}

			scored := scoreInformedEvent(&matched)
			if scored.RiskScore >= informedConfig.AlertThreshold {
				alertedCount++
				outputInformedAlert(scored)
			}
		}

			if time.Since(lastDebug) > 60*time.Second {
			cleanAccumulationMap()
			log.Printf("[polymarket] raw=%d hashSkip=%d sideSkip=%d events=%d matched=%d alerted=%d", rawCount, hashSkipCount, sideSkipCount, eventCount, matchedCount, alertedCount)
			lastDebug = time.Now()
		}
		time.Sleep(30 * time.Second)
	}
}


func directionLabel(outcome, side string) string {
	outcomeStr := strings.ToUpper(outcome)
	if side != "BUY" {
		return strings.ToLower(outcomeStr)
	}
	if outcomeStr == "YES" {
		return "bullish"
	}
	if outcomeStr == "NO" {
		return "bearish"
	}
	return outcomeStr
}

func determineDirection(tokenID, action string) string {
	outcome := lookupTokenOutcome(tokenID)
	if outcome == nil {
		return "unknown"
	}
	if outcome.Outcome == "YES" && action == "BUY" {
		return "bullish_yes"
	}
	if outcome.Outcome == "NO" && action == "BUY" {
		return "bearish_yes"
	}
	return "unknown"
}

type activityItem struct {
	Timestamp int64 `json:"timestamp"`
}

var endDateCache = make(map[string]string)
var endDateCacheMu sync.RWMutex

func fetchEndDate(assetID string) string {
	endDateCacheMu.RLock()
	if ed, ok := endDateCache[assetID]; ok {
		endDateCacheMu.RUnlock()
		return ed
	}
	endDateCacheMu.RUnlock()

	// Query CLOB /book → market hash → /markets → end_date_iso
	req, _ := http.NewRequest("GET", "https://clob.polymarket.com/book?token_id="+assetID, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var book struct{ Market string `json:"market"` }
	if err := json.NewDecoder(resp.Body).Decode(&book); err != nil || book.Market == "" {
		return ""
	}

	mktReq, _ := http.NewRequest("GET", "https://clob.polymarket.com/markets/"+book.Market, nil)
	mktReq.Header.Set("User-Agent", "Mozilla/5.0")
	mktResp, err := http.DefaultClient.Do(mktReq)
	if err != nil {
		return ""
	}
	defer mktResp.Body.Close()

	var mkt struct {
		EndDateISO string `json:"end_date_iso"`
	}
	json.NewDecoder(mktResp.Body).Decode(&mkt)

	endDateCacheMu.Lock()
	endDateCache[assetID] = mkt.EndDateISO
	endDateCacheMu.Unlock()
	return mkt.EndDateISO
}

func hoursToEnd(endDateISO string) float64 {
	if endDateISO == "" {
		return -1
	}
	formats := []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"}
	for _, f := range formats {
		if t, err := time.Parse(f, endDateISO); err == nil {
			return t.Sub(time.Now()).Hours()
		}
	}
	return -1
}

func checkNewWallet(addr string) (isNew bool, ageHours float64) {
	url := fmt.Sprintf("https://data-api.polymarket.com/activity?user=%s&limit=100&apiKey=%s", addr, dataAPIKey)
	resp, err := http.Get(url)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	var activities []activityItem
	if err := json.NewDecoder(resp.Body).Decode(&activities); err != nil {
		return false, 0
	}
	n := len(activities)
	if n == 0 {
		return true, 0 // 无任何记录,绝对新
	}
	// 拉满100条还返回100 → 老用户(可能更多)
	if n >= 100 {
		return false, 0
	}
	// 记录<100条,看最早交易是否在48h内
	earliest := activities[n-1].Timestamp
	ageHours = float64(time.Now().Unix()-earliest) / 3600
	return n < 5 && ageHours < 48, ageHours
}
