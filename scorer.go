package main

import "strings"

func scoreAddress(win *AddressWindow, nonce *int64, isContract bool) ScoredAddress {
	score := 0
	var tags []string
	address := win.Address

	// Skip zero/burn address
	if address == "0x0000000000000000000000000000000000000000" {
		return ScoredAddress{Address: address, Tags: []string{"Burn Address"}}
	}
	// Skip contracts
	if isContract {
		return ScoredAddress{
			Address: address, TotalUsd: win.TotalUsd, TxCount: win.TxCount,
			Funders: []FundersEntry{}, PrimaryFunder: "", WindowSeconds: win.LastSeen - win.FirstSeen,
			Score: 0, Tags: []string{"Contract"}, IsNewWallet: false, Nonce: nonce, IsContract: true,
		}
	}

	// Base: >= 10,000 USD threshold → +60
	if win.TotalUsd >= config.BalanceThresholdUsd {
		score += 60
	}

	// Nonce <= 1 → likely new wallet → +10
	if nonce != nil && *nonce <= 1 {
		score += 10
		tags = append(tags, "Fresh Wallet")
	}

	// First time seen → +10
	wasSeen := isAddressSeen(address)
	if !wasSeen {
		score += 10
		tags = append(tags, "New EOA")
	} else {
		score -= 15
		tags = append(tags, "Known Address")
	}

	// Check funders
	funders := getFundersList(win)
	primaryFunder := getPrimaryFunder(win)
	fromWhale := false
	fromKnownCex := false

	for _, f := range funders {
		if isWhaleAlerted(f.Address) {
			// Only count as whale if funder still has significant balance
			if checkFunderBalance(f.Address) > config.BalanceThresholdUsd {
				fromWhale = true
				score += 20
				tags = append(tags, "Fund Hopping")
			}
			break
		}
	}

	// Known CEX hot wallet → -20
	if isWhitelisted(strings.ToLower(primaryFunder)) {
		fromKnownCex = true
		score -= 20
		tags = append(tags, "CEX Withdrawal")
	}

	// Split accumulation → +5
	if win.TxCount >= 3 && len(funders) >= 2 {
		score += 5
		tags = append(tags, "Split Accumulation")
	}

	// Already alerted in past 7 days → -10
	if isWhaleAlerted(address) {
		score -= 10
		tags = append(tags, "Previously Alerted")
	}

	// Mark as seen
	firstBlock := uint64(0)
	if len(win.Transfers) > 0 {
		firstBlock = win.Transfers[0].BlockNumber
	}
	markAddressSeen(address, firstBlock, nonce, false)

	return ScoredAddress{
		Address: address, TotalUsd: win.TotalUsd, TxCount: win.TxCount,
		Funders: funders, PrimaryFunder: primaryFunder,
		WindowSeconds: win.LastSeen - win.FirstSeen,
		Score: score, Tags: tags,
		IsNewWallet: !wasSeen, Nonce: nonce, IsContract: false,
		FromWhale: fromWhale, FromKnownCex: fromKnownCex,
		IsSplitAccumulation: win.TxCount >= 3 && len(funders) >= 2,
	}
}
