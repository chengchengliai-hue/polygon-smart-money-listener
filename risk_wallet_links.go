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
// v1: basic heuristics — check known Polymarket proxy factory patterns
// Future: query Polymarket relayer API or on-chain proxy registry
func discoverLinkedWallets(eoa string) []LinkedWallet {
	var linked []LinkedWallet
	addr := strings.ToLower(eoa)

	// Polymarket Proxy factory deployed proxies are deterministic
	// For v1, we check if the address itself matches known patterns
	// Full proxy discovery requires querying the Polymarket proxy registry

	// Check existing labels in DB
	label := getAddressLabel(addr)
	switch label {
	case "polymarket_proxy":
		linked = append(linked, LinkedWallet{Address: addr, Type: WalletPolyProxy})
	case "gnosis_safe":
		linked = append(linked, LinkedWallet{Address: addr, Type: WalletGnosisSafe})
	case "polymarket_deposit":
		linked = append(linked, LinkedWallet{Address: addr, Type: WalletDeposit})
	}

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
