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

var serverChanKey = ""

func init() {
	serverChanKey = getEnv("SERVER_CHAN_KEY", "")
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

	// Output JSON to stdout
	jsonBytes, _ := json.Marshal(alert)
	fmt.Println(string(jsonBytes))

	// Persist to SQLite
	saveInformedAlert(alert)

	// Push to WeChat via Server酱
	if serverChanKey != "" {
		pushToWechat(&alert)
	}
}

func pushToWechat(alert *InformedEventAlert) {
	severityEmoji := map[string]string{
		"high":   "🔴", "normal": "🟡", "watch": "⚪",
	}

	title := fmt.Sprintf("%s Polymarket %s: %s",
		severityEmoji[alert.Severity],
		alert.Severity,
		alert.Data.Direction,
	)

	content := fmt.Sprintf(`市场: %s
类别: %s
钱包: %s
匹配地址: %s (%s)
角色: %s
下注: $%.0f USDC
方向: %s %s
分数: %d
标签: %s
时间: %s
`,
		alert.Data.MarketQuestion,
		alert.Data.EventCategory,
		alert.Data.RootWalletAddress,
		alert.Data.MatchedWalletAddress,
		alert.Data.MatchedWalletType,
		alert.Data.MatchedRole,
		alert.Data.EstimatedUsdc,
		alert.Data.Action,
		alert.Data.Outcome,
		alert.Data.RiskScore,
		strings.Join(alert.Data.Tags, ", "),
		alert.Data.DetectedAt,
	)

	resp, err := http.PostForm(
		fmt.Sprintf("https://sc.ftqq.com/%s.send", serverChanKey),
		url.Values{"text": {title}, "desp": {content}},
	)
	if err != nil {
		log.Printf("[wechat] push failed: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[wechat] pushed: %s", string(body)[:100])
}
