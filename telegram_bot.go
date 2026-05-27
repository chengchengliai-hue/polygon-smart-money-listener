package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type tgUpdate struct {
	UpdateID      int              `json:"update_id"`
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgMessage struct {
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

type tgCallbackQuery struct {
	ID      string     `json:"id"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUpdatesResponse struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

var botOffsetSeeded bool
var botLastUpdateID int

func startTelegramBot() {
	if tgBotToken == "" {
		return
	}

	setBotCommands()

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			pollBotUpdates()
		}
	}()
	log.Println("[tg-bot] polling started")
}

func setBotCommands() {
	commands := `{"commands":[
		{"command":"smart_money","description":"聪明钱预警"},
		{"command":"accumulation","description":"吸筹预警"}
	]}`
	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/setMyCommands", tgBotToken),
		"application/json",
		strings.NewReader(commands),
	)
	if err != nil {
		log.Printf("[tg-bot] setMyCommands failed: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[tg-bot] setMyCommands: %s", safeTruncate(string(body), 80))
}

func pollBotUpdates() {
	offset := 0
	if botOffsetSeeded {
		offset = botLastUpdateID + 1
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=1", tgBotToken, offset)
	resp, err := http.Get(url)
	if err != nil {
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var updates tgUpdatesResponse
	if err := json.Unmarshal(body, &updates); err != nil || !updates.OK {
		return
	}

	if !botOffsetSeeded && len(updates.Result) > 0 {
		botLastUpdateID = updates.Result[len(updates.Result)-1].UpdateID
		botOffsetSeeded = true
		return
	}
	botOffsetSeeded = true

	for _, u := range updates.Result {
		if u.UpdateID > botLastUpdateID {
			botLastUpdateID = u.UpdateID
		}

		if u.CallbackQuery != nil {
			handleCallback(u.CallbackQuery)
		} else if u.Message != nil && strings.TrimSpace(u.Message.Text) != "" {
			handleCommand(u.Message)
		}
	}
}

func handleCommand(msg *tgMessage) {
	text := strings.TrimSpace(msg.Text)
	text = strings.TrimPrefix(text, "/")

	switch text {
	case "smart_money", "smartmoney":
		alerts := queryRecentAlerts("informed_event_activity", 20)
		sendAlerts(msg.Chat.ID, alerts)
	case "accumulation":
		alerts := queryRecentAlerts("accumulation_detected", 20)
		sendAlerts(msg.Chat.ID, alerts)
	case "start":
		showPanel(msg.Chat.ID)
	default:
		showPanel(msg.Chat.ID)
	}
}

func showPanel(chatID int64) {
	kb := `{"inline_keyboard":[[{"text":"🧠 聪明钱预警","callback_data":"smart_money"}],[{"text":"📈 吸筹预警","callback_data":"accumulation"}]]}`
	sendTgMessage(chatID, "🔘 选择预警类型：", kb)
}

func handleCallback(cb *tgCallbackQuery) {
	answerCallback(cb.ID)

	switch cb.Data {
	case "smart_money":
		alerts := queryRecentAlerts("informed_event_activity", 20)
		sendAlerts(cb.Message.Chat.ID, alerts)
	case "accumulation":
		alerts := queryRecentAlerts("accumulation_detected", 20)
		sendAlerts(cb.Message.Chat.ID, alerts)
	}
}

type tgAlertRow struct {
	EventType         string
	RootAddress       string
	MatchedAddress    string
	MatchedWalletType string
	ConditionID       string
	TokenID           string
	Outcome           string
	Action            string
	Category          string
	MarketQuestion    string
	MarketSlug        string
	EstimatedUsdc     float64
	Score             int
	Severity          string
	Tags              []string
	TxHash            string
	LogIndex          int64
	BlockNumber       int64
	AlertedAt         string
}

func queryRecentAlerts(eventType string, limit int) []tgAlertRow {
	if db == nil {
		return nil
	}

	rows, err := db.Query(
		`SELECT event_type, root_address, matched_address, matched_wallet_type,
		        condition_id, token_id, outcome, action,
		        category, estimated_usdc, score, severity, tags, alerted_at,
		        market_question, market_slug, tx_hash, log_index, block_number
		 FROM informed_event_alerts
		 WHERE event_type = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		eventType, limit,
	)
	if err != nil {
		log.Printf("[tg-bot] query error: %v", err)
		return nil
	}
	defer rows.Close()

	var results []tgAlertRow
	for rows.Next() {
		var r tgAlertRow
		var tagsJSON string
		var matchedWalletType, conditionID, tokenID sql.NullString
		var category, outcome, action, marketQuestion, marketSlug, txHash sql.NullString
		var logIndex, blockNumber sql.NullInt64
		rows.Scan(&r.EventType, &r.RootAddress, &r.MatchedAddress, &matchedWalletType,
			&conditionID, &tokenID, &outcome, &action,
			&category, &r.EstimatedUsdc, &r.Score, &r.Severity, &tagsJSON, &r.AlertedAt,
			&marketQuestion, &marketSlug, &txHash, &logIndex, &blockNumber)
		r.MatchedWalletType = matchedWalletType.String
		r.ConditionID = conditionID.String
		r.TokenID = tokenID.String
		r.Category = category.String
		r.Outcome = outcome.String
		r.Action = action.String
		r.MarketQuestion = marketQuestion.String
		r.MarketSlug = marketSlug.String
		r.TxHash = txHash.String
		r.LogIndex = logIndex.Int64
		r.BlockNumber = blockNumber.Int64
		json.Unmarshal([]byte(tagsJSON), &r.Tags)

		if t, err := time.Parse("2006-01-02 15:04:05", r.AlertedAt); err == nil {
			r.AlertedAt = t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
		}

		results = append(results, r)
	}
	return results
}

func buildMarketURL(slug string) string {
	if slug == "" {
		return ""
	}
	if strings.HasPrefix(slug, "market/") {
		return "https://polymarket.com/" + slug
	}
	return "https://polymarket.com/market/" + slug
}

func shortenAddr(addr string) string {
	if len(addr) > 14 {
		return addr[:8] + "..." + addr[len(addr)-6:]
	}
	return addr
}

func sendAlerts(chatID int64, alerts []tgAlertRow) {
	if len(alerts) == 0 {
		sendTgMessage(chatID, "暂无数据", "")
		return
	}

	for i := range alerts {
		text, kb := formatOneAlert(alerts[i], i+1, len(alerts))
		sendTgMessage(chatID, text, kb)
	}
}

func formatOneAlert(a tgAlertRow, idx, total int) (string, string) {
	if a.EventType == "accumulation_detected" {
		return formatAccumulationAlert(a, idx, total)
	}
	return formatSmartMoneyAlert(a, idx, total)
}

func formatAccumulationAlert(a tgAlertRow, idx, total int) (string, string) {
	market := a.MarketQuestion
	if market == "" {
		market = a.Category
	}
	if market == "" {
		market = a.MarketSlug
	}

	header := "🚨 提前吸筹预警 — 高危"
	if total > 1 {
		header = fmt.Sprintf("🚨 提前吸筹预警 — 高危 (%d/%d)", idx, total)
	}

	text := fmt.Sprintf("%s\n\n"+
		"👛 地址: %s\n"+
		"📌 市场: %s\n"+
		"📊 方向: BUY %s\n"+
		"📦 加仓: %s  💰 总金额: $%.0f\n"+
		"📈 评分: %d\n"+
		"%s"+
		"\n⏰ %s（北京时间）\n"+
		"\n🔗 %s",
		header,
		a.MatchedAddress,
		market,
		strings.ToUpper(a.Outcome),
		a.Category, a.EstimatedUsdc,
		a.Score,
		formatSignals(a.Tags),
		a.AlertedAt,
		buildMarketURL(a.MarketSlug),
	)

	marketURL := buildMarketURL(a.MarketSlug)
	walletLink := "https://polygonscan.com/address/" + a.MatchedAddress
	profileLink := "https://polymarket.com/profile/" + a.MatchedAddress
	kb := fmt.Sprintf(
		`{"inline_keyboard":[[{"text":"📊 查看市场","url":"%s"},{"text":"🔍 钱包","url":"%s"}],[{"text":"💼 持仓","url":"%s"}]]}`,
		marketURL, walletLink, profileLink,
	)
	return text, kb
}

func formatSmartMoneyAlert(a tgAlertRow, idx, total int) (string, string) {
	actionCN := "买入"
	if a.Action == "SELL" {
		actionCN = "卖出"
	}
	dirExplain := fmt.Sprintf("%s %s", actionCN, a.Outcome)
	switch {
	case a.Action == "BUY" && a.Outcome == "YES":
		dirExplain = "看多 YES"
	case a.Action == "BUY" && a.Outcome == "NO":
		dirExplain = "看空（买入 NO）"
	case a.Action == "SELL" && a.Outcome == "NO":
		dirExplain = "看多（卖出 NO）"
	case a.Action == "SELL" && a.Outcome == "YES":
		dirExplain = "看空（卖出 YES）"
	}

	market := a.MarketQuestion
	if market == "" {
		market = a.Category
	}
	if market == "" {
		market = a.MarketSlug
	}

	cat := a.Category
	if cat == "" {
		cat = "未知"
	}

	severityCN := map[string]string{"high": "高危", "normal": "普通", "watch": "观察"}
	emoji := map[string]string{"high": "🔴", "normal": "🟡", "watch": "⚪"}
	cn := severityCN[a.Severity]
	em := emoji[a.Severity]

	source := "历史巨鲸池命中"
	for _, t := range a.Tags {
		if strings.Contains(t, "原生发现") {
			source = "Polymarket 原生发现"
			break
		}
	}

	header := fmt.Sprintf("%s 聪明钱预警 — %s", em, cn)
	if total > 1 {
		header = fmt.Sprintf("%s 聪明钱预警 — %s (%d/%d)", em, cn, idx, total)
	}

	text := fmt.Sprintf("%s\n\n"+
		"👛 捕获钱包: %s\n"+
		"🐋 关联巨鲸: %s\n"+
		"📌 市场: %s\n"+
		"🏷 分类: %s  |  💰 金额: $%.0f  |  %s %s\n"+
		"📊 方向: %s\n"+
		"📈 评分: %d 分  |  来源: %s\n"+
		"%s"+
		"\n⏰ %s（北京时间）\n"+
		"\n🔗 %s",
		header,
		shortenAddr(a.MatchedAddress),
		shortenAddr(a.RootAddress),
		market,
		cat, a.EstimatedUsdc, actionCN, a.Outcome,
		dirExplain,
		a.Score, source,
		formatTags(a.Tags),
		a.AlertedAt,
		buildMarketURL(a.MarketSlug),
	)

	marketURL := buildMarketURL(a.MarketSlug)
	walletLink := "https://polygonscan.com/address/" + a.MatchedAddress
	profileLink := "https://polymarket.com/profile/" + a.MatchedAddress
	txLink := "https://polygonscan.com/tx/" + a.TxHash

	var kb string
	if a.TxHash != "" {
		kb = fmt.Sprintf(
			`{"inline_keyboard":[[{"text":"📊 查看市场","url":"%s"},{"text":"🔍 钱包","url":"%s"}],[{"text":"📝 交易","url":"%s"}],[{"text":"💼 持仓","url":"%s"}]]}`,
			marketURL, walletLink, txLink, profileLink,
		)
	} else {
		kb = fmt.Sprintf(
			`{"inline_keyboard":[[{"text":"📊 查看市场","url":"%s"},{"text":"🔍 钱包","url":"%s"}],[{"text":"💼 持仓","url":"%s"}]]}`,
			marketURL, walletLink, profileLink,
		)
	}

	return text, kb
}

func formatSignals(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	lines := []string{"", "🏷 信号:"}
	for _, t := range tags {
		lines = append(lines, fmt.Sprintf("  %s", t))
	}
	return strings.Join(lines, "\n")
}

func formatTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return fmt.Sprintf("\n🏷 标签: %s", strings.Join(tags, " · "))
}

func sendTgMessage(chatID int64, text, keyboard string) {
	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if keyboard != "" {
		var kbMap map[string]interface{}
		json.Unmarshal([]byte(keyboard), &kbMap)
		payload["reply_markup"] = kbMap
	}

	payloadBytes, _ := json.Marshal(payload)
	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tgBotToken),
		"application/json",
		bytes.NewReader(payloadBytes),
	)
	if err != nil {
		log.Printf("[tg-bot] send error: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Printf("[tg-bot] sent: %s", safeTruncate(string(body), 80))
}

func answerCallback(callbackID string) {
	payload := fmt.Sprintf(`{"callback_query_id":"%s"}`, callbackID)
	http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", tgBotToken),
		"application/json",
		strings.NewReader(payload),
	)
}

func safeTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
