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
		{"command":"accumulation","description":"吸筹预警"},
		{"command":"positions","description":"跟踪仓位"},
		{"command":"clear","description":"清除记录"}
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
		alerts := queryRecentAlerts("informed_event_activity", 5)
		sendAlerts(msg.Chat.ID, alerts)
	case "accumulation":
		alerts := queryRecentAlerts("accumulation_detected", 5)
		sendAlerts(msg.Chat.ID, alerts)
	case "positions":
		showTrackedPositions(msg.Chat.ID)
	case "clear":
		sendTgMessage(msg.Chat.ID, "── 以上记录已清除 ──", "")
	case "start":
		showPanel(msg.Chat.ID)
	default:
		showPanel(msg.Chat.ID)
	}
}

func showPanel(chatID int64) {
	kb := `{"inline_keyboard":[[{"text":"🧠 聪明钱预警","callback_data":"smart_money"}],[{"text":"📈 吸筹预警","callback_data":"accumulation"}],[{"text":"📋 跟踪仓位","callback_data":"positions"}]]}`
	sendTgMessage(chatID, "🔘 选择预警类型：", kb)
}

func handleCallback(cb *tgCallbackQuery) {
	data := cb.Data

	// Track position: "t|<shortID>"
	if strings.HasPrefix(data, "t|") {
		shortID := data[2:]
		handleTrackCallback(cb, shortID)
		return
	}

	// Untrack position: "u|<positionID>"
	if strings.HasPrefix(data, "u|") {
		handleUntrackCallback(cb, data[2:])
		return
	}

	answerCallback(cb.ID)

	switch data {
	case "smart_money":
		alerts := queryRecentAlerts("informed_event_activity", 5)
		sendAlerts(cb.Message.Chat.ID, alerts)
	case "accumulation":
		alerts := queryRecentAlerts("accumulation_detected", 5)
		sendAlerts(cb.Message.Chat.ID, alerts)
	case "positions":
		showTrackedPositions(cb.Message.Chat.ID)
	}
}

func handleTrackCallback(cb *tgCallbackQuery, shortID string) {
	trackContextsMu.Lock()
	ctx, ok := trackContexts[shortID]
	if ok {
		delete(trackContexts, shortID)
	}
	trackContextsMu.Unlock()

	if !ok {
		answerCallback(cb.ID)
		sendTgMessage(cb.Message.Chat.ID, "⚠️ 跟踪信息已过期，请重新查看预警", "")
		return
	}

	id, err := insertTrackedPosition(ctx.Wallet, ctx.MarketSlug, ctx.MarketTitle, ctx.TokenType, ctx.Amount, ctx.Score)
	if err != nil {
		log.Printf("[tracker] insert position failed: %v", err)
		answerCallback(cb.ID)
		return
	}

	// Add to in-memory index
	pos := &TrackedPosition{
		ID:            id,
		Wallet:        ctx.Wallet,
		MarketSlug:    ctx.MarketSlug,
		MarketTitle:   ctx.MarketTitle,
		TokenType:     ctx.TokenType,
		TrackedAmount: ctx.Amount,
		EntryScore:    ctx.Score,
		Status:        "active",
	}
	key := makeTrackKey(ctx.Wallet, ctx.MarketSlug, ctx.TokenType)
	trackIndexMu.Lock()
	trackIndex[key] = pos
	trackIndexMu.Unlock()

	// Edit original message: change button to "✅ 已跟踪"
	editInlineKeyboard(cb.Message.Chat.ID, cb.Message.MessageID, fmt.Sprintf(
		`{"inline_keyboard":[[{"text":"📊 查看市场","url":"https://polymarket.com/%s"}],[{"text":"🔍 钱包","url":"https://polygonscan.com/address/%s"}],[{"text":"💼 持仓","url":"https://polymarket.com/profile/%s"}],[{"text":"✅ 已跟踪","callback_data":"tracked"}]]}`,
		ctx.MarketSlug, ctx.Wallet, ctx.Wallet,
	))

	answerCallback(cb.ID)
	log.Printf("[tracker] position tracked: wallet=%s market=%s token=%s amount=$%.0f", ctx.Wallet[:10], ctx.MarketSlug, ctx.TokenType, ctx.Amount)
}

func handleUntrackCallback(cb *tgCallbackQuery, idStr string) {
	var id int64
	fmt.Sscanf(idStr, "%d", &id)
	if id <= 0 {
		answerCallback(cb.ID)
		return
	}

	markPositionExited(id)

	// Remove from in-memory index
	trackIndexMu.Lock()
	for k, v := range trackIndex {
		if v.ID == id {
			delete(trackIndex, k)
			break
		}
	}
	trackIndexMu.Unlock()

	// Edit message: replace the untrack button for that row (just answer callback with confirmation)
	// Since editing one button in a multi-row keyboard is complex, answer with toast
	answerCallbackTg(cb.ID, "已取消跟踪")

	// Re-send updated list
	showTrackedPositions(cb.Message.Chat.ID)
	log.Printf("[tracker] position %d untracked", id)
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

	trackCtx := TrackContext{
		Wallet:      a.MatchedAddress,
		MarketSlug:  a.MarketSlug,
		MarketTitle: market,
		TokenType:   a.Outcome,
		Amount:      a.EstimatedUsdc,
		Score:       a.Score,
	}
	trackID := storeTrackContext(trackCtx)
	trackCB := fmt.Sprintf("t|%s", trackID)

	var kb string
	if a.TxHash != "" {
		kb = fmt.Sprintf(
			`{"inline_keyboard":[[{"text":"📊 查看市场","url":"%s"},{"text":"🔍 钱包","url":"%s"}],[{"text":"📝 交易","url":"%s"}],[{"text":"💼 持仓","url":"%s"}],[{"text":"👁 跟踪仓位","callback_data":"%s"}]]}`,
			marketURL, walletLink, txLink, profileLink, trackCB,
		)
	} else {
		kb = fmt.Sprintf(
			`{"inline_keyboard":[[{"text":"📊 查看市场","url":"%s"},{"text":"🔍 钱包","url":"%s"}],[{"text":"💼 持仓","url":"%s"}],[{"text":"👁 跟踪仓位","callback_data":"%s"}]]}`,
			marketURL, walletLink, profileLink, trackCB,
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

func answerCallbackTg(callbackID, text string) {
	payload := fmt.Sprintf(`{"callback_query_id":"%s","text":"%s"}`, callbackID, text)
	http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", tgBotToken),
		"application/json",
		strings.NewReader(payload),
	)
}

func editInlineKeyboard(chatID int64, messageID int, keyboard string) {
	var kbMap map[string]interface{}
	json.Unmarshal([]byte(keyboard), &kbMap)

	payload := map[string]interface{}{
		"chat_id":      chatID,
		"message_id":   messageID,
		"reply_markup": kbMap,
	}
	payloadBytes, _ := json.Marshal(payload)
	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/editMessageReplyMarkup", tgBotToken),
		"application/json",
		bytes.NewReader(payloadBytes),
	)
	if err != nil {
		log.Printf("[tg-bot] edit markup error: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[tg-bot] edit markup: %s", safeTruncate(string(body), 80))
}

func showTrackedPositions(chatID int64) {
	positions := getActiveTrackedPositions()
	if len(positions) == 0 {
		sendTgMessage(chatID, "📋 暂无跟踪中的仓位", "")
		return
	}

	lines := []string{fmt.Sprintf("📋 跟踪仓位 — 共 %d 个", len(positions)), ""}

	for i, p := range positions {
		walletShort := p.Wallet
		if len(walletShort) > 14 {
			walletShort = walletShort[:8] + "..." + walletShort[len(walletShort)-6:]
		}

		direction := fmt.Sprintf("看空（买入 %s）", p.TokenType)
		if p.TokenType == "YES" {
			direction = "看多 YES"
		}

		lines = append(lines,
			fmt.Sprintf("%d. %s", i+1, walletShort),
			fmt.Sprintf("   📌 %s", p.MarketTitle),
			fmt.Sprintf("   💰 $%.0f  |  %s  |  原始评分 %d", p.TrackedAmount, direction, p.EntryScore),
			fmt.Sprintf("   ⏰ 跟踪自 %s", p.CreatedAt),
			"",
		)
	}

	text := strings.Join(lines, "\n")

	// Build keyboard: one row per position with untrack button
	kbRows := make([]json.RawMessage, 0)
	for _, p := range positions {
		marketURL := "https://polymarket.com/market/" + p.MarketSlug
		walletLink := "https://polymarket.com/profile/" + p.Wallet
		untrackCB := fmt.Sprintf("u|%d", p.ID)
		row := json.RawMessage(fmt.Sprintf(
			`[{"text":"🔗 市场","url":"%s"},{"text":"💼 当前持仓","url":"%s"},{"text":"❌ 取消跟踪","callback_data":"%s"}]`,
			marketURL, walletLink, untrackCB,
		))
		kbRows = append(kbRows, row)
	}

	kbBytes, _ := json.Marshal(map[string]interface{}{"inline_keyboard": kbRows})
	sendTgMessage(chatID, text, string(kbBytes))
}

func safeTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
