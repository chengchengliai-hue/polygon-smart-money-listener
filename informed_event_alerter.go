package main

import (
	"encoding/json"
	"fmt"
	"time"
)

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

	// For market orders, takerAmountFilled is the cash/collateral leg (USDC)
	// makerAmountFilled is the share/outcome token leg
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

	// Persist
	saveInformedAlert(alert)
}
