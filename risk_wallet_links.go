package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

var (
	// Table B: Risk EOA Pool (from whale_alerts, with TTL)
	riskEoaPool   = make(map[string]*RiskWalletEntry)
	riskEoaPoolMu sync.RWMutex

	// Cache: all linked addresses → risk entries (multi-root)
	allLinkedAddresses   = make(map[string][]*RiskWalletEntry)
	allLinkedAddressesMu sync.RWMutex
)

const riskTTL = 30 * 24 * 3600 // 30 days

func refreshRiskPool() {
	entries := loadWhaleAddresses()
	now := time.Now().Unix()

	riskEoaPoolMu.Lock()
	newPool := make(map[string]*RiskWalletEntry)
	newLinked := make(map[string][]*RiskWalletEntry)
	riskEoaPoolMu.Unlock()

	for _, entry := range entries {
		if isWhitelisted(strings.ToLower(entry.RootAddresses[0])) {
			continue
		}
		if entry.LastActive > 0 && now-entry.LastActive > riskTTL {
			continue
		}

		eoa := strings.ToLower(entry.RootAddresses[0])
		newPool[eoa] = &entry

		// Add EOA itself
		newLinked[eoa] = append(newLinked[eoa], &entry)

		// Add funder links
		func() {
			rows, err := db.Query(`SELECT DISTINCT primary_funder_address FROM whale_alerts WHERE address = ?`, eoa)
			if err != nil {
				return
			}
			defer rows.Close()
			for rows.Next() {
				var funder string
				rows.Scan(&funder)
				funder = strings.ToLower(funder)
				if funder != "" && funder != eoa && !isWhitelisted(funder) {
					newLinked[funder] = append(newLinked[funder], &entry)
				}
			}
		}()

		// Add funded links
		func() {
			rows, err := db.Query(`SELECT DISTINCT address FROM whale_alerts WHERE primary_funder_address = ?`, eoa)
			if err != nil {
				return
			}
			defer rows.Close()
			for rows.Next() {
				var funded string
				rows.Scan(&funded)
				funded = strings.ToLower(funded)
				if funded != "" && funded != eoa && !isWhitelisted(funded) {
					newLinked[funded] = append(newLinked[funded], &entry)
				}
			}
		}()
	}

	riskEoaPoolMu.Lock()
	riskEoaPool = newPool
	riskEoaPoolMu.Unlock()

	allLinkedAddressesMu.Lock()
	allLinkedAddresses = newLinked
	allLinkedAddressesMu.Unlock()

	log.Printf("[wallet] pool: %d risk EOAs, %d linked addresses (with whitelist+TTL)", len(newPool), len(newLinked))
}

func lookupRiskWallet(address string) []*RiskWalletEntry {
	addr := strings.ToLower(address)

	// Step 1: Check all linked addresses
	allLinkedAddressesMu.RLock()
	if entries := allLinkedAddresses[addr]; len(entries) > 0 {
		allLinkedAddressesMu.RUnlock()
		return entries
	}
	allLinkedAddressesMu.RUnlock()

	// Step 2: Direct EOA pool check
	riskEoaPoolMu.RLock()
	defer riskEoaPoolMu.RUnlock()
	if entry := riskEoaPool[addr]; entry != nil {
		return []*RiskWalletEntry{entry}
	}
	return nil
}

func lookupRiskEoa(eoa string) bool {
	riskEoaPoolMu.RLock()
	defer riskEoaPoolMu.RUnlock()
	_, ok := riskEoaPool[strings.ToLower(eoa)]
	return ok
}

func startWalletLinkRefresher() {
	refreshRiskPool()
	go func() {
		ticker := time.NewTicker(time.Duration(informedConfig.WalletRefreshSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			refreshRiskPool()
		}
	}()
}
