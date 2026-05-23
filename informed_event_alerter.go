package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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

	if scored.TokenOutcome != nil {
		category = scored.TokenOutcome.Category
		question = scored.TokenOutcome.Question
		conditionID = scored.TokenOutcome.ConditionID
		tokenID = scored.TokenOutcome.TokenID
		outcome = scored.TokenOutcome.Outcome
		outcomeIdx = scored.TokenOutcome.OutcomeIndex
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
	emoji := map[string]string{"high": "\xF0\x9F\x94\xB4", "normal": "\xF0\x9F\x9F\xA1", "watch": "\xE2\x9A\xAA"}
	actionCN := map[string]string{"BUY": "买入", "SELL": "卖出", "UNKNOWN": "未知"}
	roleCN := map[string]string{"maker": "挂单成交", "taker": "吃单成交"}
	outcomeCN := map[string]string{"YES": "YES（看多）", "NO": "NO（看空）"}
	walletTypeCN := map[string]string{
		"EOA":             "EOA 普通钱包",
		"POLY_PROXY":      "Polymarket 代理合约",
		"GNOSIS_SAFE":     "Gnosis Safe 代理合约",
		"DEPOSIT_WALLET":  "Polymarket 存款钱包",
		"SESSION_SIGNER":  "会话签名",
	}

	cn := severityCN[alert.Severity]
	dir := fmt.Sprintf("%s%s", alert.Data.Action, alert.Data.Outcome)
	if out, ok := outcomeCN[alert.Data.Outcome]; ok {
		dir = fmt.Sprintf("%s%s", actionCN[alert.Data.Action], out)
	}
	role := roleCN[alert.Severity]
	if r, ok := roleCN[alert.Data.MatchedRole]; ok {
		role = r
	}
	wt := alert.Data.MatchedWalletType
	if w, ok := walletTypeCN[alert.Data.MatchedWalletType]; ok {
		wt = w
	}

	// Source tag
	source := "历史巨鲸池命中"
	for _, t := range alert.Data.Tags {
		if t == "Polymarket Native Discovery" {
			source = "Polymarket 原生发现"
			break
		}
	}

	// Convert UTC to Beijing time
	beijing := alert.Data.DetectedAt
	if t, err := time.Parse(time.RFC3339, alert.Data.DetectedAt); err == nil {
		beijing = t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
	}

	text := fmt.Sprintf("%s 聪明钱报警 — %s\n\n市场：%s\n分类：%s\n下注金额：$%.0f USDC\n方向：%s\n交易角色：%s\n\n根钱包（巨鲸）：%s\n匹配钱包（代理）：%s\n钱包类型：%s\n\n来源：%s\n风险评分：%d 分\n标签：%s\n交易哈希：%s\n区块号：%d\n发现时间：%s（北京时间）",
		emoji[alert.Severity],
		cn,
		alert.Data.MarketQuestion,
		alert.Data.EventCategory,
		alert.Data.EstimatedUsdc,
		dir,
		role,
		alert.Data.RootWalletAddress,
		alert.Data.MatchedWalletAddress,
		wt,
		source,
		alert.Data.RiskScore,
		strings.Join(alert.Data.Tags, "、"),
		alert.Data.TxHash,
		alert.Data.BlockNumber,
		beijing,
	)

	resp, err := http.Get(fmt.Sprintf(
		"https://api.telegram.org/bot%s/sendMessage?chat_id=%s&text=%s",
		tgBotToken, tgChatID, url.QueryEscape(text),
	))
	if err != nil {
		log.Printf("[tg] push failed: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[tg] pushed: %s", string(body)[:80])
}
