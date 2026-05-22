package main

// detectHedge checks if the same root wallet is buying BOTH YES and NO
// on the same condition within the hedging window
func detectHedge(matched *MatchedTrade) bool {
	if matched.TokenOutcome == nil {
		return false
	}

	history := getWalletConditionHistory(
		matched.RootAddress,
		matched.TokenOutcome.ConditionID,
		informedConfig.WindowSeconds,
	)

	if len(history) == 0 {
		// Save current activity for future checks
		saveWalletConditionActivity(
			matched.RootAddress,
			matched.TokenOutcome.ConditionID,
			matched.TokenOutcome.Outcome,
			matched.Action,
			matched.TxHash,
			max(matched.MakerAmount, matched.TakerAmount),
			matched.BlockNumber,
		)
		return false
	}

	// Check if any previous activity in the window was on the OPPOSITE outcome
	currentOutcome := matched.TokenOutcome.Outcome
	currentAmount := max(matched.MakerAmount, matched.TakerAmount)

	for _, h := range history {
		if h.Outcome != currentOutcome {
			// Opposite outcome found — check if both sides are significant
			if currentAmount >= informedConfig.MinTradeUsdc && h.EstimatedUsdc >= informedConfig.MinTradeUsdc {
				// Check if amounts are similar (0.6-1.6 ratio = likely hedge)
				ratio := currentAmount / h.EstimatedUsdc
				if ratio >= 0.6 && ratio <= 1.6 {
					return true
				}
			}
		}
	}

	// Not a hedge, save for future detection
	saveWalletConditionActivity(
		matched.RootAddress,
		matched.TokenOutcome.ConditionID,
		matched.TokenOutcome.Outcome,
		matched.Action,
		matched.TxHash,
		currentAmount,
		matched.BlockNumber,
	)

	return false
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
