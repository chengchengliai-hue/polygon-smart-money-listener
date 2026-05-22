package main

func scoreInformedEvent(matched *MatchedTrade) InformedScoredEvent {
	score := 0
	var tags []string

	// Base: risk wallet matched Polymarket trade → +40
	score += 40
	tags = append(tags, "Polymarket Trade")

	// Check if wallet type is confirmed Proxy/Safe/Deposit
	switch matched.MatchedWalletType {
	case WalletPolyProxy, WalletGnosisSafe, WalletDeposit:
		score += 10
		tags = append(tags, "Proxy Wallet Match")
	case WalletEOA:
		if matched.MatchedWalletType == WalletEOA {
			// EOA only, no proxy found → slight penalty
			score -= 5
			// Only add tag if we actually tried to find proxy
			if matched.MatchedWallet != matched.RootAddress {
				tags = append(tags, "Proxy Unknown")
			}
		}
	}

	// Market is high-information category → +20
	if matched.TokenOutcome != nil && isHighInfoCategory(matched.TokenOutcome.Category) {
		score += 20
		tags = append(tags, "High Information Market")
	}

	// Single trade >= 5,000 USDC → +20
	if matched.MakerAmount >= informedConfig.MinTradeUsdc || matched.TakerAmount >= informedConfig.MinTradeUsdc {
		score += 20
		tags = append(tags, "Large Directional Buy")
	}

	// Direction is clear (YES/NO buy) → +10
	if matched.Direction != "unknown" {
		score += 10
	}

	// Check for YES/NO hedging on same condition
	isHedged := detectHedge(matched)
	if isHedged {
		score += informedConfig.HedgePenalty
		tags = append(tags, "Hedged / Arbitrage Pattern")
	}

	// Determine severity
	severity := "watch"
	if score >= informedConfig.HighThreshold {
		severity = "high"
	} else if score >= informedConfig.AlertThreshold {
		severity = "normal"
	}

	return InformedScoredEvent{
		MatchedTrade: *matched,
		RiskScore:    score,
		Tags:         tags,
		Severity:     severity,
		IsHedged:     isHedged,
	}
}
