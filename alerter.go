package main

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

func outputAlert(scored ScoredAddress) {
	severity := "watch"
	confidence := "low"
	if scored.Score >= 90 {
		severity = "high"
		confidence = "high"
	} else if scored.Score >= 70 {
		severity = "normal"
		confidence = "medium"
	}

	alert := WhaleAlert{
		SchemaVersion:  "1.0",
		EventType:      "smart_money_detected",
		Severity:       severity,
		ConfidenceLevel: confidence,
		Chain:          "polygon",
		Data: WhaleAlertData{
			TargetAddress:         scored.Address,
			PrimaryFunderAddress:  scored.PrimaryFunder,
			Funders:               scored.Funders,
			TotalUsdAccumulated:   math.Round(scored.TotalUsd*100) / 100,
			AccumulationWindowSec: scored.WindowSeconds,
			TxCountInWindow:       scored.TxCount,
			RiskScore:             scored.Score,
			Tags:                  scored.Tags,
			DetectedAt:            time.Now().UTC().Format(time.RFC3339),
		},
	}

	jsonBytes, _ := json.Marshal(alert)
	fmt.Println(string(jsonBytes))

	// Persist to DB
	saveWhaleAlert(scored.Address, scored.PrimaryFunder, scored.TotalUsd, scored.Score, severity, scored.Tags)
}

func outputSummary(processed, alerts int64) {
	msg := StatusMessage{
		SchemaVersion: "1.0",
		EventType:     "status",
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Data: StatusData{
			Processed:  processed,
			Alerts:     alerts,
			WindowSize: len(windows),
		},
	}
	jsonBytes, _ := json.Marshal(msg)
	fmt.Println(string(jsonBytes))
}
