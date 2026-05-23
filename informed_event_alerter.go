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

var pushAppToken = ""
var pushUids []string

func init() {
	pushAppToken = getEnv("WXPUSHER_APP_TOKEN", "")
	if uid := getEnv("WXPUSHER_UID", ""); uid != "" {
		pushUids = []string{uid}
	}
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

	// Stdout
	jsonBytes, _ := json.Marshal(alert)
	fmt.Println(string(jsonBytes))

	// SQLite
	saveInformedAlert(alert)

	// WxPusher
	if pushAppToken != "" && len(pushUids) > 0 {
		pushToWechat(&alert)
	}
}

func pushToWechat(alert *InformedEventAlert) {
	severityEmoji := map[string]string{"high": "🔴", "normal": "🟡", "watch": "⚪"}

	title := fmt.Sprintf("%s [%s] %s → $%.0f",
		severityEmoji[alert.Severity],
		alert.Severity,
		alert.Data.Direction,
		alert.Data.EstimatedUsdc,
	)

	body := fmt.Sprintf("市场: %s\n类别: %s\n钱包: %s\n匹配: %s(%s)\n角色: %s\n金额: $%.0f USDC\n方向: %s %s\n分数: %d\n标签: %s\n%s",
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

	payload := map[string]interface{}{
		"appToken":    pushAppToken,
		"content":     body,
		"summary":     title,
		"contentType": 1,
		"uids":        pushUids,
	}

	jsonPayload, _ := json.Marshal(payload)
	resp, err := http.Post(
		"https://wxpusher.zjiecode.com/api/send/message",
		"application/json",
		bytes.NewReader(jsonPayload),
	)
	if err != nil {
		log.Printf("[wxpush] failed: %v", err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("[wxpush] sent: %s", string(respBody)[:100])
}
