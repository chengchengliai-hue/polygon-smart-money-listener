package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
)

type activityEntry struct {
	Type        string  `json:"type"`
	Side        string  `json:"side"`
	Title       string  `json:"title"`
	ConditionID string  `json:"conditionId"`
	Timestamp   int64   `json:"timestamp"`
}

type closedPosition struct {
	Title        string  `json:"title"`
	RealizedPnl  float64 `json:"realizedPnl"`
	Outcome      string  `json:"outcome"`
}

func fetchActivity(addr string, limit int) ([]activityEntry, error) {
	url := fmt.Sprintf("https://data-api.polymarket.com/activity?user=%s&limit=%d&apiKey=%s", addr, limit, dataAPIKey)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var entries []activityEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func fetchClosedPositions(addr string, limit int) ([]closedPosition, error) {
	url := fmt.Sprintf("https://data-api.polymarket.com/closed-positions?user=%s&limit=%d&sortBy=realizedPnl&sortDirection=desc&apiKey=%s", addr, limit, dataAPIKey)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var entries []closedPosition
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// calcMakerScore returns a score 0.0 (Alpha) ~ 1.0 (market maker)
func calcMakerScore(addr string) float64 {
	// ── Dimension 1: Bilateral trading (0.45 weight) ──
	activities, err := fetchActivity(addr, 50)
	if err != nil || len(activities) == 0 {
		return 0.5 // unknown, assume neutral
	}

	trades := make([]activityEntry, 0)
	for _, a := range activities {
		if a.Type == "TRADE" {
			trades = append(trades, a)
		}
	}

	conditionSides := make(map[string]map[string]bool)
	for _, t := range trades {
		cond := strings.ToLower(t.ConditionID)
		if cond == "" {
			continue
		}
		if conditionSides[cond] == nil {
			conditionSides[cond] = make(map[string]bool)
		}
		conditionSides[cond][strings.ToUpper(t.Side)] = true
	}

	bilateralCount := 0
	totalConditions := len(conditionSides)
	for _, sides := range conditionSides {
		if sides["BUY"] && sides["SELL"] {
			bilateralCount++
		} else {
			// Also check if both YES and NO were bought (BUY YES + BUY NO)
		}
	}
	bilateralRatio := 0.0
	if totalConditions > 0 {
		bilateralRatio = float64(bilateralCount) / float64(totalConditions)
	}

	// ── Dimension 2: Hold time (0.30 weight) ──
	holdTimes := make([]float64, 0)
	for i := 0; i < len(trades)-1; i++ {
		for j := i + 1; j < len(trades); j++ {
			if trades[i].ConditionID == trades[j].ConditionID &&
				trades[i].Side != trades[j].Side {
				diff := math.Abs(float64(trades[j].Timestamp - trades[i].Timestamp)) / 60.0
				holdTimes = append(holdTimes, diff)
				break
			}
		}
	}

	holdScore := 0.5
	if len(holdTimes) > 0 {
		sort.Float64s(holdTimes)
		median := holdTimes[len(holdTimes)/2]
		switch {
		case median < 2:
			holdScore = 1.0
		case median < 5:
			holdScore = 0.7
		case median < 30:
			holdScore = 0.4
		default:
			holdScore = 0.0
		}
	}

	// ── Dimension 3: PnL smoothness (0.25 weight) ──
	positions, err := fetchClosedPositions(addr, 50)
	smoothScore := 0.0
	if err == nil && len(positions) > 5 {
		pnls := make([]float64, 0)
		for _, p := range positions {
			pnls = append(pnls, p.RealizedPnl)
		}
		mean := average(pnls)
		std := stddev(pnls, mean)
		if mean > 0 {
			ratio := std / mean
			switch {
			case ratio < 0.5:
				smoothScore = 1.0 // very smooth
			case ratio < 1.0:
				smoothScore = 0.7
			case ratio < 2.0:
				smoothScore = 0.3
			default:
				smoothScore = 0.0 // jumpy = Alpha
			}
		}
	} else {
		smoothScore = 0.5 // insufficient data
	}

	// ── Composite ──
	score := bilateralRatio*0.45 + holdScore*0.30 + smoothScore*0.25
	return score
}

func average(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func stddev(vals []float64, mean float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += (v - mean) * (v - mean)
	}
	return math.Sqrt(sum / float64(len(vals)-1))
}
