package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	trackIndex   = make(map[string]*TrackedPosition) // key: "wallet|marketSlug|tokenType"
	trackIndexMu sync.RWMutex

	trackContexts   = make(map[string]TrackContext) // shortID → context, TTL 10min
	trackContextsMu sync.Mutex

	trackedSells   = make(map[string]float64) // accumulated sell amount
	trackedSellsMu sync.Mutex
)

func loadTrackIndex() {
	positions := getActiveTrackedPositions()
	trackIndexMu.Lock()
	for _, p := range positions {
		key := makeTrackKey(p.Wallet, p.MarketSlug, p.TokenType)
		// Make a copy so we don't hold a reference to the loop variable
		pos := p
		trackIndex[key] = &pos
	}
	trackIndexMu.Unlock()
	log.Printf("[tracker] loaded %d active positions into index", len(positions))
}

func makeTrackKey(wallet, marketSlug, tokenType string) string {
	return strings.ToLower(wallet) + "|" + marketSlug + "|" + strings.ToUpper(tokenType)
}

func genShortID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func storeTrackContext(ctx TrackContext) string {
	id := genShortID()
	trackContextsMu.Lock()
	trackContexts[id] = ctx
	trackContextsMu.Unlock()
	return id
}

func startTrackContextGC() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			trackContextsMu.Lock()
			// For simplicity, clear all — if context is needed it would have been consumed within seconds
			// Keep only entries from last 10 minutes (we don't track timestamps yet, simple approach)
			if len(trackContexts) > 100 {
				trackContexts = make(map[string]TrackContext)
				log.Printf("[tracker] gc: cleared stale track contexts")
			}
			trackContextsMu.Unlock()
		}
	}()
}

func checkTrackedExit(t dataTrade) {
	wallet := strings.ToLower(t.ProxyWallet)
	slug := t.Slug
	outcome := strings.ToUpper(t.Outcome)
	key := makeTrackKey(wallet, slug, outcome)

	trackIndexMu.RLock()
	pos := trackIndex[key]
	trackIndexMu.RUnlock()
	if pos == nil {
		return
	}

	side := strings.ToUpper(t.Side)
	notional := t.Price * t.Size

	if side == "BUY" {
		// They're adding to the position we're tracking — update amount
		newAmount := pos.TrackedAmount + notional
		trackIndexMu.Lock()
		if p, ok := trackIndex[key]; ok {
			p.TrackedAmount = newAmount
		}
		trackIndexMu.Unlock()
		updateTrackedAmountDB(wallet, slug, outcome, newAmount)
		log.Printf("[tracker] %s added $%.0f to tracked position (new total: $%.0f)", wallet[:10], notional, newAmount)
		return
	}

	if side != "SELL" {
		return
	}

	// SELL of the tracked token — accumulate
	trackedSellsMu.Lock()
	trackedSells[key] += notional
	totalSold := trackedSells[key]
	trackedSellsMu.Unlock()

	trackedAmount := pos.TrackedAmount
	ratio := totalSold / trackedAmount

	if ratio >= 1.0 || math.Abs(totalSold-trackedAmount) < 0.01 {
		// Full exit (or very close)
		pushExitAlert(pos, "full", totalSold)
		trackedSellsMu.Lock()
		delete(trackedSells, key)
		trackedSellsMu.Unlock()
		// Mark exited
		markPositionExited(pos.ID)
		trackIndexMu.Lock()
		delete(trackIndex, key)
		trackIndexMu.Unlock()
		log.Printf("[tracker] %s FULL EXIT from %s (%s), sold $%.0f / $%.0f", wallet[:10], pos.MarketTitle[:30], outcome, totalSold, trackedAmount)
	} else if ratio >= 0.5 {
		pushExitAlert(pos, "partial", totalSold)
		log.Printf("[tracker] %s PARTIAL EXIT from %s (%s), sold $%.0f / $%.0f", wallet[:10], pos.MarketTitle[:30], outcome, totalSold, trackedAmount)
	}
}

func pushExitAlert(pos *TrackedPosition, exitType string, soldAmount float64) {
	if tgBotToken == "" || tgChatID == "" {
		return
	}

	emoji := "🔴"
	label := "已清仓预警"
	if exitType == "partial" {
		emoji = "🟡"
		label = "大幅减仓预警"
	}

	direction := fmt.Sprintf("看空（买入 %s）", pos.TokenType)
	if pos.TokenType == "YES" {
		direction = "看多 YES"
	}

	marketURL := ""
	if pos.MarketSlug != "" {
		marketURL = "https://polymarket.com/market/" + pos.MarketSlug
	}

	walletShort := pos.Wallet
	if len(walletShort) > 14 {
		walletShort = walletShort[:8] + "..." + walletShort[len(walletShort)-6:]
	}

	lines := []string{
		fmt.Sprintf("%s %s", emoji, label),
		"",
		fmt.Sprintf("👛 钱包: %s", walletShort),
		fmt.Sprintf("📌 市场: %s", pos.MarketTitle),
		fmt.Sprintf("📉 原持仓: $%.0f（%s）", pos.TrackedAmount, direction),
	}

	if exitType == "full" {
		lines = append(lines, "🏷 已全部卖出")
	} else {
		lines = append(lines, fmt.Sprintf("🏷 已卖出 $%.0f / $%.0f（%.0f%%）", soldAmount, pos.TrackedAmount, soldAmount/pos.TrackedAmount*100))
	}

	lines = append(lines,
		"",
		fmt.Sprintf("⏰ %s（北京时间）", time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")),
	)

	if marketURL != "" {
		lines = append(lines, "", fmt.Sprintf("🔗 %s", marketURL))
	}

	text := strings.Join(lines, "\n")

	walletLink := "https://polygonscan.com/address/" + pos.Wallet
	profileLink := "https://polymarket.com/profile/" + pos.Wallet

	kb := fmt.Sprintf(
		`{"inline_keyboard":[[{"text":"📊 查看市场","url":"%s"},{"text":"🔍 钱包","url":"%s"}],[{"text":"💼 持仓","url":"%s"}]]}`,
		marketURL, walletLink, profileLink,
	)

	var kbMap map[string]interface{}
	json.Unmarshal([]byte(kb), &kbMap)

	payloadMap := map[string]interface{}{
		"chat_id":                  tgChatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup":             kbMap,
	}

	payloadBytes, _ := json.Marshal(payloadMap)
	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tgBotToken),
		"application/json",
		bytes.NewReader(payloadBytes),
	)
	if err != nil {
		log.Printf("[tracker] push exit alert failed: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[tracker] exit alert pushed: %s", safeTruncate(string(body), 80))
}
