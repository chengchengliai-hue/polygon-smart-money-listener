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
	emoji := map[string]string{"high": "\xF0\x9F\x94\xB4", "normal": "\xF0\x9F\x9F\xA1", "watch": "\xE2\x9A\xAA"}

	text := fmt.Sprintf("%s Polymarket %s, $%.0f\n\nMarket: %s\nCategory: %s\nWallet: %s\nMatched: %s (%s)\nRole: %s\nDirection: %s %s\nScore: %d\nTags: %s\nTime: %s",
		emoji[alert.Severity],
		alert.Severity,
		alert.Data.EstimatedUsdc,
		alert.Data.MarketQuestion,
		alert.Data.EventCategory,
		alert.Data.RootWalletAddress,
		alert.Data.MatchedWalletAddress,
		alert.Data.MatchedWalletType,
		alert.Data.MatchedRole,
		alert.Data.Action,
		alert.Data.Outcome,
		alert.Data.RiskScore,
		strings.Join(alert.Data.Tags, ", "),
		alert.Data.DetectedAt,
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
