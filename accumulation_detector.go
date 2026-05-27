package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type accumEntry struct {
	Wallet    string
	Slug      string
	Title     string
	MarketEnd int64
	Outcome   string
	Count     int
	TotalUsd  float64
	Prices    []float64
	MaxSingle float64
	FirstSeen time.Time
	LastSeen  time.Time
	AlertedAt  int
	AlertedUsd float64
}

var (
	firstSeenMap  = make(map[string]time.Time)
	accumMap      = make(map[string]*accumEntry)
	accumMu       sync.Mutex
	accumCallCount     uint64
	accumChecked       uint64
	accumSkippedSELL   uint64
	accumSkippedTitle  uint64
	accumSkippedNotional uint64
	accumLastLog       time.Time

	positionCache     = make(map[string]map[string]float64)
	positionCacheTime = make(map[string]time.Time)
	positionCacheMu   sync.Mutex

	binancePrices   = make(map[string]float64)
	binancePricesMu sync.RWMutex
)

func startBinancePoller() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			fetchBinancePrices()
		}
	}()
	fetchBinancePrices()
}

func fetchBinancePrices() {
	symbols := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "XRPUSDT"}
	for _, sym := range symbols {
		resp, err := http.Get("https://api.binance.com/api/v3/ticker/price?symbol=" + sym)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var data struct{ Price string }
		json.Unmarshal(body, &data)
		if p, err := strconv.ParseFloat(data.Price, 64); err == nil && p > 0 {
			binancePricesMu.Lock()
			binancePrices[sym] = p
			binancePricesMu.Unlock()
		}
	}
}

func isCryptoShortTerm(title string) bool {
	t := strings.ToLower(title)
	crypto := strings.Contains(t, "bitcoin") || strings.Contains(t, "ethereum") ||
		strings.Contains(t, "sol") || strings.Contains(t, "xrp") ||
		strings.Contains(t, "doge") || strings.Contains(t, "hype") || strings.Contains(t, "bnb")
	short := strings.Contains(t, "up or down") || strings.Contains(t, "5-min") ||
		strings.Contains(t, "15-min") || strings.Contains(t, "15m") || strings.Contains(t, "5m")
	return crypto && short
}

func parseMarketEnd(slug string) int64 {
	re := regexp.MustCompile(`(\d{10})`)
	match := re.FindString(slug)
	if match == "" {
		return 0
	}
	start, _ := strconv.ParseInt(match, 10, 64)
	if start == 0 {
		return 0
	}
	s := strings.ToLower(slug)
	if strings.Contains(s, "5m") || strings.Contains(s, "5-min") {
		return start + 300
	}
	return start + 900
}

func isMarketMakerForSlug(wallet, slug string) bool {
	url := fmt.Sprintf("https://data-api.polymarket.com/activity?user=%s&limit=50&apiKey=%s", wallet, dataAPIKey)
	resp, err := http.Get(url)
	if err != nil {
		return false
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	type actResp struct {
		Type        string `json:"type"`
		Side        string `json:"side"`
		Slug        string `json:"slug"`
	}
	var activities []actResp
	if err := json.Unmarshal(body, &activities); err != nil {
		return false
	}

	buyCount := 0
	sellCount := 0
	for _, a := range activities {
		if a.Type != "TRADE" || a.Slug != slug {
			continue
		}
		switch strings.ToUpper(a.Side) {
		case "BUY":
			buyCount++
		case "SELL":
			sellCount++
		}
	}

	total := buyCount + sellCount
	if total < 3 {
		return false // not enough data
	}

	ratio := float64(sellCount) / float64(total)
	if ratio > 0.2 {
		log.Printf("[accum] wallet=%s slug=%s SELL ratio=%.0f%% (>20%%), market maker, skip", wallet, slug, ratio*100)
		return true
	}
	return false
}

func hasPosition(wallet, slug string) bool {
	url := fmt.Sprintf("https://data-api.polymarket.com/positions?user=%s&limit=50&apiKey=%s", wallet, dataAPIKey)
	resp, err := http.Get(url)
	if err != nil {
		return false
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	type posResp struct {
		Slug string  `json:"slug"`
		Size float64 `json:"size"`
	}
	var positions []posResp
	if err := json.Unmarshal(body, &positions); err != nil {
		return false
	}

	for _, p := range positions {
		if p.Slug == slug && p.Size > 0 {
			return true
		}
	}
	return false
}

func checkPriceDivergence(title string) bool {
	binancePricesMu.RLock()
	defer binancePricesMu.RUnlock()
	t := strings.ToLower(title)
	switch {
	case strings.Contains(t, "bitcoin"):
		return binancePrices["BTCUSDT"] > 0
	case strings.Contains(t, "ethereum"):
		return binancePrices["ETHUSDT"] > 0
	case strings.Contains(t, "sol"):
		return binancePrices["SOLUSDT"] > 0
	case strings.Contains(t, "xrp"):
		return binancePrices["XRPUSDT"] > 0
	}
	return false
}

func cleanAccumulationMap() {
	accumMu.Lock()
	defer accumMu.Unlock()
	now := time.Now()
	for k, v := range accumMap {
		if v.MarketEnd > 0 && v.MarketEnd < now.Unix() {
			delete(accumMap, k)
		}
	}
	for k, t := range firstSeenMap {
		if now.Sub(t) > 60*time.Minute {
			delete(firstSeenMap, k)
		}
	}
}

func outputAccumulationAlert(entry *accumEntry, score int, remaining, endRemain float64, signals []string) {
	alert := InformedEventAlert{
		SchemaVersion:   "1.2",
		EventType:       "accumulation_detected",
		Severity:        "high",
		ConfidenceLevel: "medium",
		Chain:           "polygon",
		Source:          "polymarket",
		Data: InformedEventData{
			MatchedWalletAddress: entry.Wallet,
			RootWalletAddress:    entry.Wallet,
			MatchedWalletType:    "EOA",
			MarketQuestion:       entry.Title,
			MarketSlug:           entry.Slug,
			MarketURL:            "https://polymarket.com/market/" + entry.Slug,
			Action:               "BUY",
			Outcome:              entry.Outcome,
			Direction:            "accumulation_" + strings.ToLower(entry.Outcome),
			EstimatedUsdc:        entry.TotalUsd,
			RiskScore:            score,
			Tags:                 signals,
			DetectedAt:           time.Now().UTC().Format(time.RFC3339),
		},
	}
	jsonBytes, _ := json.Marshal(alert)
	fmt.Println(string(jsonBytes))
	saveInformedAlert(alert)

	// Push to Telegram
	pushAccumulationToTelegram(&alert, entry, score, remaining, endRemain, signals)
}

func pushAccumulationToTelegram(alert *InformedEventAlert, entry *accumEntry, score int, remaining, endRemain float64, signals []string) {
	if tgBotToken == "" || tgChatID == "" {
		return
	}

	// Format market URL
	marketURL := "https://polymarket.com/market/" + entry.Slug

	// Build Telegram message
	text := fmt.Sprintf("🚨 提前吸筹预警 — 高危\n\n"+
		"地址: %s\n"+
		"市场: %s\n"+
		"方向: BUY %s\n"+
		"加仓: %d次  总金额: $%.0f\n"+
		"时间: %s-%s (距结算%.0f-%.0f分钟)\n"+
		"评分: %d\n\n"+
		"信号:\n%s\n\n"+
		"⏰ %s（北京时间）\n\n"+
		"🔗 %s",
		entry.Wallet,
		entry.Title,
		strings.ToUpper(entry.Outcome),
		entry.Count, entry.TotalUsd,
		entry.FirstSeen.In(time.FixedZone("CST", 8*3600)).Format("15:04"),
		entry.LastSeen.In(time.FixedZone("CST", 8*3600)).Format("15:04"),
		endRemain, remaining,
		score,
		"  "+strings.Join(signals, "\n  "),
		time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05"),
		marketURL,
	)

	kb := fmt.Sprintf(`{"inline_keyboard":[[{"text":"📊 查看市场","url":"%s"},{"text":"🔍 钱包","url":"https://polygonscan.com/address/%s"}],[{"text":"💼 持仓","url":"https://polymarket.com/profile/%s"}]]}`,
		marketURL, entry.Wallet, entry.Wallet)

	payloadMap := map[string]interface{}{
		"chat_id":    tgChatID,
		"text":       text,
		"parse_mode": "HTML",
		"disable_web_page_preview": true,
	}
	var kbMap map[string]interface{}
	json.Unmarshal([]byte(kb), &kbMap)
	payloadMap["reply_markup"] = kbMap

	payloadBytes, _ := json.Marshal(payloadMap)
	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tgBotToken),
		"application/json",
		bytes.NewReader(payloadBytes),
	)
	if err != nil {
		log.Printf("[accum] tg push failed: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[accum] tg pushed: %s", string(body)[:80])
}
func checkAccumulation(wallet, slug, title, outcome string, side string, notional float64, price float64) {
	if side != "BUY" {
		return
	}
	if !isCryptoShortTerm(title) || notional < 10 {
		return
	}

	accumChecked++
	if time.Since(accumLastLog) > 300*time.Second {
		log.Printf("[accum] checked=%d seen=%d tracked=%d",
			accumChecked, len(firstSeenMap), len(accumMap))
		accumLastLog = time.Now()
	}

	marketEnd := parseMarketEnd(slug)
	if marketEnd == 0 {
		return
	}

	now := time.Now()
	key := strings.ToLower(wallet) + "|" + slug

	accumMu.Lock()
	defer accumMu.Unlock()

	// Already tracking → accumulate
	if entry, exists := accumMap[key]; exists {
		entry.Count++
		entry.TotalUsd += notional
		entry.Prices = append(entry.Prices, price)
		if notional > entry.MaxSingle {
			entry.MaxSingle = notional
		}
		entry.LastSeen = now

		if entry.Count < 3 {
			return
		}
		if entry.LastSeen.Sub(entry.FirstSeen).Minutes() < 3 {
			delete(accumMap, key)
			return
		}
		scoreAndAlert(entry, now)
		return
	}

	// First time seeing this wallet+slug
	// Immediately check if they already have a position (5min market settles fast)
	if hasPosition(wallet, slug) {
		if isMarketMakerForSlug(wallet, slug) {
			return
		}
		accumMap[key] = &accumEntry{
			Wallet:    wallet,
			Slug:      slug,
			Title:     title,
			Outcome:   outcome,
			MarketEnd: marketEnd,
			Count:     1,
			TotalUsd:  notional,
			Prices:    []float64{price},
			MaxSingle: notional,
			FirstSeen: now,
			LastSeen:  now,
		}
	}
}

func scoreAndAlert(entry *accumEntry, now time.Time) {
	remaining := float64(entry.MarketEnd-entry.FirstSeen.Unix()) / 60.0

	timeScore := 0
	switch {
	case remaining > 60:
		timeScore = 35
	case remaining > 30:
		timeScore = 25
	case remaining > 10:
		timeScore = 15
	case remaining > 5:
		timeScore = 5
	}

	smallScore := 0
	if entry.MaxSingle/entry.TotalUsd < 0.5 {
		smallScore = 15
	}

	chaseScore := 0
	if len(entry.Prices) >= 3 {
		avg := 0.0
		for _, p := range entry.Prices {
			avg += p
		}
		avg /= float64(len(entry.Prices))
		variance := 0.0
		for _, p := range entry.Prices {
			variance += (p - avg) * (p - avg)
		}
		std := math.Sqrt(variance / float64(len(entry.Prices)))
		if avg > 0 && std/avg < 0.4 {
			chaseScore = 15
		}
	}

	divergeScore := 0
	if checkPriceDivergence(entry.Title) {
		divergeScore = 20
	}

	totalScore := timeScore + smallScore + chaseScore + divergeScore

	signals := make([]string, 0)
	if timeScore >= 25 {
		signals = append(signals, fmt.Sprintf("提前%.0fmin布局(+%d)", remaining, timeScore))
	} else if timeScore >= 5 {
		signals = append(signals, fmt.Sprintf("提前入场(+%d)", timeScore))
	}
	if smallScore > 0 {
		signals = append(signals, fmt.Sprintf("小额分散(+%d)", smallScore))
	}
	if chaseScore > 0 {
		signals = append(signals, fmt.Sprintf("价格受控(+%d)", chaseScore))
	}
	if divergeScore > 0 {
		signals = append(signals, fmt.Sprintf("价格背离(+%d)", divergeScore))
	}

	if totalScore >= 30 && entry.TotalUsd >= 300 {
		shouldAlert := entry.AlertedAt == 0 ||
			entry.Count-entry.AlertedAt >= 3 ||
			entry.TotalUsd >= entry.AlertedUsd*2

		if shouldAlert {
			entry.AlertedAt = entry.Count
			entry.AlertedUsd = entry.TotalUsd
			span := entry.LastSeen.Sub(entry.FirstSeen).Minutes()
			endRemain := float64(entry.MarketEnd-entry.LastSeen.Unix()) / 60.0

			log.Printf("[accum] ALERT: wallet=%s title=%s count=%d usd=$%.0f score=%d span=%.0fm endIn=%.0fm signals=%v",
				entry.Wallet, entry.Title, entry.Count, entry.TotalUsd, totalScore, span, endRemain, signals)

			outputAccumulationAlert(entry, totalScore, remaining, endRemain, signals)
		}
	}
}
