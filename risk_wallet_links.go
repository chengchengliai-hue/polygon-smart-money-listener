package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

var (
	riskAddressSet   = make(map[string]*RiskWalletEntry) // key: linked address (lowercase)
	riskAddressSetMu sync.RWMutex
)

// Maps all linked addresses (EOA, Proxy, Safe, Deposit) → root entry
// O(1) lookup by any linked address

func refreshRiskWalletLinks() {
	entries := loadWhaleAddresses()
	riskAddressSetMu.Lock()
	defer riskAddressSetMu.Unlock()

	newSet := make(map[string]*RiskWalletEntry)

	for _, entry := range entries {
		// EOA is always linked
		entry.LinkedWallets = []LinkedWallet{
			{Address: entry.RootAddress, Type: WalletEOA},
		}
		newSet[strings.ToLower(entry.RootAddress)] = &entry

		// Try to discover linked wallets (Proxy, Safe, Deposit)
		linked := discoverLinkedWallets(entry.RootAddress)
		for _, lw := range linked {
			entry.LinkedWallets = append(entry.LinkedWallets, lw)
			newSet[strings.ToLower(lw.Address)] = &entry
		}

		// Persist links
		for _, lw := range entry.LinkedWallets {
			saveRiskWalletLink(lw.Address, entry.RootAddress, lw.Type, "auto", entry.RiskScore, entry.Tags)
		}
	}

	riskAddressSet = newSet
	log.Printf("[wallet-links] loaded %d root wallets, %d total linked addresses", len(entries), len(newSet))
}

// discoverLinkedWallets attempts to find Proxy/Safe/Deposit wallets for an EOA
func discoverLinkedWallets(eoa string) []LinkedWallet {
	var linked []LinkedWallet
	addr := strings.ToLower(eoa)

	// 1. Check DB labels for this exact address
	label := getAddressLabel(addr)
	switch label {
	case "polymarket_proxy":
		linked = append(linked, LinkedWallet{Address: addr, Type: WalletPolyProxy})
	case "gnosis_safe":
		linked = append(linked, LinkedWallet{Address: addr, Type: WalletGnosisSafe})
	case "polymarket_deposit":
		linked = append(linked, LinkedWallet{Address: addr, Type: WalletDeposit})
	}

	// 2. Query whale_alerts: add primary funders of THIS address as linked wallets
	// If a whale address was funded by a known proxy/Safe, add it too
	func() {
		rows, err := db.Query(
			`SELECT DISTINCT primary_funder_address FROM whale_alerts WHERE address = ?`,
			addr,
		)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var funder string
			rows.Scan(&funder)
			if funder != "" && strings.ToLower(funder) != addr {
				fLabel := getAddressLabel(strings.ToLower(funder))
				fType := WalletEOA
				switch fLabel {
				case "polymarket_proxy":
					fType = WalletPolyProxy
				case "gnosis_safe":
					fType = WalletGnosisSafe
				case "polymarket_deposit":
					fType = WalletDeposit
				}
				linked = append(linked, LinkedWallet{Address: funder, Type: fType})
			}
		}
	}()

	// 3. Also add addresses that THIS whale funded (potential proxy outflows)
	func() {
		rows, err := db.Query(
			`SELECT DISTINCT address FROM whale_alerts WHERE primary_funder_address = ?`,
			addr,
		)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var funded string
			rows.Scan(&funded)
			if funded != "" && strings.ToLower(funded) != addr {
				linked = append(linked, LinkedWallet{Address: funded, Type: WalletEOA})
			}
		}
	}()

	return linked
}

// Lookup checks if an address (maker/taker) matches any risk wallet
func lookupRiskWallet(address string) *RiskWalletEntry {
	riskAddressSetMu.RLock()
	defer riskAddressSetMu.RUnlock()
	return riskAddressSet[strings.ToLower(address)]
}

func startWalletLinkRefresher() {
	refreshRiskWalletLinks()

	// Periodic refresh
	go func() {
		ticker := time.NewTicker(time.Duration(informedConfig.WalletRefreshSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			refreshRiskWalletLinks()
		}
	}()
}
