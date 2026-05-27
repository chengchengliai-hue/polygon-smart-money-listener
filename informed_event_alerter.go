package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

var tgBotToken = ""
var tgChatID = ""

func init() {
	tgBotToken = getEnv("TG_BOT_TOKEN", "")
	tgChatID = getEnv("TG_CHAT_ID", "")
}

func outputInformedAlert(scored InformedScoredEvent) {
	category := ""
	question := ""
	conditionID := ""
	tokenID := ""
	outcome := ""
	outcomeIdx := 0
	marketSlug := ""

	if scored.TokenOutcome != nil {
		category = scored.TokenOutcome.Category
		question = scored.TokenOutcome.Question
		conditionID = scored.TokenOutcome.ConditionID
		tokenID = scored.TokenOutcome.TokenID
		outcome = scored.TokenOutcome.Outcome
		outcomeIdx = scored.TokenOutcome.OutcomeIndex
		marketSlug = scored.TokenOutcome.MarketSlug
	}

	marketURL := ""
	if marketSlug != "" {
		marketURL = "https://polymarket.com/" + marketSlug
	}

	estimatedUsdc := scored.TakerAmount
	if estimatedUsdc < scored.MakerAmount {
		estimatedUsdc = scored.MakerAmount
	}

	alert := InformedEventAlert{
		SchemaVersion:   "1.1",
		EventType:       "informed_event_activity",
		Severity:        scored.Severity,
		ConfidenceLevel: "medium",
		Chain:           "polygon",
		Source:          "polymarket",
		Data: InformedEventData{
			RootWalletAddress:    scored.RootAddress,
			MatchedWalletAddress: scored.MatchedWallet,
			MatchedWalletType:    string(scored.MatchedWalletType),
			MatchedRole:          scored.MatchedRole,
			EventCategory:        category,
			MarketQuestion:       question,
			ConditionID:          conditionID,
			TokenID:              tokenID,
			Outcome:              outcome,
			OutcomeIndex:         outcomeIdx,
			MarketSlug:           marketSlug,
			MarketURL:            marketURL,
			Action:               scored.Action,
			Direction:            scored.Direction,
			EstimatedUsdc:        estimatedUsdc,
			RiskScore:            scored.RiskScore,
			Tags:                 scored.Tags,
			TxHash:               scored.TxHash,
			LogIndex:             scored.LogIndex,
			BlockNumber:          scored.BlockNumber,
			DetectedAt:           time.Now().UTC().Format(time.RFC3339),
		},
	}

	jsonBytes, _ := json.Marshal(alert)
	fmt.Println(string(jsonBytes))
	saveInformedAlert(alert)

	if tgBotToken != "" && tgChatID != "" {
		pushToTelegram(&alert)
	}
}

func pushToTelegram(alert *InformedEventAlert) {
	severityCN := map[string]string{"high": "高危", "normal": "普通", "watch": "观察"}
	emoji := map[string]string{"high": "🔴", "normal": "🟡", "watch": "⚪"}

	cn := severityCN[alert.Severity]

	// Direction display
	actionCN := map[string]string{"BUY": "买入", "SELL": "卖出"}
	outcomeCN := map[string]string{"YES": "YES", "NO": "NO"}
	actionStr := actionCN[alert.Data.Action]
	outcomeStr := outcomeCN[alert.Data.Outcome]

	// Direction interpretation
	dirExplain := alert.Data.Direction
	switch {
	case alert.Data.Action == "BUY" && alert.Data.Outcome == "YES":
		dirExplain = "看多 YES"
	case alert.Data.Action == "BUY" && alert.Data.Outcome == "NO":
		dirExplain = "看空（买入 NO）"
	case alert.Data.Action == "SELL" && alert.Data.Outcome == "NO":
		dirExplain = "看多（卖出 NO）"
	case alert.Data.Action == "SELL" && alert.Data.Outcome == "YES":
		dirExplain = "看空（卖出 YES）"
	}

	// Source detection
	source := "历史巨鲸池命中"
	for _, t := range alert.Data.Tags {
		if strings.Contains(t, "原生发现") {
			source = "Polymarket 原生发现"
			break
		}
	}

	// Market name
	marketName := alert.Data.MarketQuestion
	if marketName == "" {
		marketName = "(市场信息补全中)"
	}

	// Category
	category := alert.Data.EventCategory
	if category == "" {
		category = "未知"
	}

	// Wallet display (shorten)
	rootShort := alert.Data.RootWalletAddress
	matchedShort := alert.Data.RootWalletAddress
	if len(rootShort) > 14 {
		rootShort = rootShort[:8] + "..." + rootShort[len(rootShort)-6:]
	}
	if len(matchedShort) > 14 {
		matchedShort = matchedShort[:8] + "..." + matchedShort[len(matchedShort)-6:]
	}

	// Total entity position (estimate from aggregated trades)
	amount := alert.Data.EstimatedUsdc

	// Beijing time
	beijing := alert.Data.DetectedAt
	if t, err := time.Parse(time.RFC3339, alert.Data.DetectedAt); err == nil {
		beijing = t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
	}

	// Build message
		lines := []string{
			fmt.Sprintf("%s 聪明钱预警 — %s", emoji[alert.Severity], cn),
			"",
			fmt.Sprintf("👛 捕获钱包: %s", rootShort),
			fmt.Sprintf("🐋 关联巨鲸: %s", matchedShort),
			fmt.Sprintf("📌 市场: %s", marketName),
			fmt.Sprintf("🏷 分类: %s  |  💰 金额: $%.0f  |  %s %s", category, amount, actionStr, outcomeStr),
			fmt.Sprintf("📊 方向: %s", dirExplain),
			fmt.Sprintf("📈 评分: %d 分  |  来源: %s", alert.Data.RiskScore, source),
			"",
			fmt.Sprintf("🏷 标签: %s", strings.Join(alert.Data.Tags, " · ")),
			"",
			fmt.Sprintf("⏰ %s（北京时间）", beijing),
		}

	if alert.Data.MarketURL != "" {
		lines = append(lines, "", fmt.Sprintf("🔗 %s", alert.Data.MarketURL))
	}

	text := strings.Join(lines, "\n")

	// Build inline keyboard: market link + wallet link
	marketLink := alert.Data.MarketURL
	if marketLink == "" {
		marketLink = "https://polymarket.com"
	}
	walletLink := "https://polygonscan.com/address/" + alert.Data.MatchedWalletAddress
	txLink := "https://polygonscan.com/tx/" + alert.Data.TxHash
	profileLink := "https://polymarket.com/profile/" + alert.Data.MatchedWalletAddress

	keyboard := fmt.Sprintf(
		`{"inline_keyboard":[[{"text":"📊 查看市场","url":"%s"},{"text":"🔍 钱包","url":"%s"}],[{"text":"📝 交易","url":"%s"}],[{"text":"💼 持仓","url":"%s"}]]}`,
		marketLink, walletLink, txLink, profileLink,
	)

	var kbMap map[string]interface{}
	json.Unmarshal([]byte(keyboard), &kbMap)

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
		log.Printf("[tg] push failed: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[tg] pushed: %s", string(body)[:80])
}
