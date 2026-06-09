package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

var (
	windows       = make(map[string]*AddressWindow)
	windowsMu     sync.Mutex
	processedLogs = make(map[string]int64) // key: "txHash_logIndex" → expiry unix timestamp
	processedMu   sync.Mutex
)

// isDuplicate checks if this log was already processed (idempotency)
// Returns true if duplicate, false if new. Cleans expired entries inline.
func isDuplicate(txHash string, logIndex uint) bool {
	key := fmt.Sprintf("%s_%d", txHash, logIndex)
	processedMu.Lock()
	defer processedMu.Unlock()

	now := time.Now().Unix()
	if expiry, ok := processedLogs[key]; ok && expiry > now {
		return true
	}
	// Write new entry
	processedLogs[key] = now + 900

	// Inline GC: delete stale entries when map gets too large
	if len(processedLogs) > 5000 {
		for k, exp := range processedLogs {
			if exp < now {
				delete(processedLogs, k)
			}
		}
	}
	return false
}

// removeProcessedLog removes a log from the dedup cache (for reorg reversal)
func removeProcessedLog(txHash string, logIndex uint) {
	key := fmt.Sprintf("%s_%d", txHash, logIndex)
	processedMu.Lock()
	delete(processedLogs, key)
	processedMu.Unlock()
}

func addTransfer(event *TransferEvent) {
	windowsMu.Lock()
	defer windowsMu.Unlock()

	key := event.To
	win, ok := windows[key]
	if !ok {
		win = &AddressWindow{
			Address:   key,
			Transfers: []TransferEvent{},
			Funders:   make(map[string]float64),
			FirstSeen: event.Timestamp,
			LastSeen:  event.Timestamp,
		}
		windows[key] = win
	}

	if event.Timestamp < win.FirstSeen {
		win.FirstSeen = event.Timestamp
	}
	if event.Timestamp > win.LastSeen {
		win.LastSeen = event.Timestamp
	}

	// Prune transfers older than window before recalculating
	cutoff := event.Timestamp - int64(config.WindowSeconds)
	var kept []TransferEvent
	win.TotalUsd = 0
	win.TxCount = 0
	win.Funders = make(map[string]float64)
	for _, t := range win.Transfers {
		if t.Timestamp >= cutoff {
			kept = append(kept, t)
			win.TotalUsd += t.ValueUsd
			win.TxCount++
			win.Funders[t.From] += t.ValueUsd
		}
	}
	win.Transfers = kept

	win.Transfers = append(win.Transfers, *event)
	win.TxCount++
	win.TotalUsd += event.ValueUsd
	win.Funders[event.From] += event.ValueUsd
}

func getExceededWindows() []*AddressWindow {
	windowsMu.Lock()
	defer windowsMu.Unlock()

	var results []*AddressWindow
	for key, win := range windows {
		if win.TotalUsd >= config.BalanceThresholdUsd {
			results = append(results, win)
			delete(windows, key)
		}
	}
	return results
}

func collectGarbage() {
	windowsMu.Lock()
	defer windowsMu.Unlock()

	now := time.Now().Unix()
	threshold := now - int64(config.WindowSeconds)
	for key, win := range windows {
		if win.LastSeen < threshold {
			delete(windows, key)
		}
	}

	// GC DB dedup tables every 5 minutes (not every 60s to avoid thrashing)
	if now%300 < 60 {
		go gcDBTables()
	}
}

func gcDBTables() {
	dbWriteMu.Lock()
	defer dbWriteMu.Unlock()
	cutoff := time.Now().Add(-30 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	r1, _ := db.Exec(`DELETE FROM polymarket_events_seen WHERE seen_at < ?`, cutoff)
	r2, _ := db.Exec(`DELETE FROM seen_addresses WHERE seen_at < ?`, cutoff)
	if r1 != nil {
		if n, _ := r1.RowsAffected(); n > 0 {
			log.Printf("[gc] removed %d old event dedup entries", n)
		}
	}
	if r2 != nil {
		if n, _ := r2.RowsAffected(); n > 0 {
			log.Printf("[gc] removed %d old address dedup entries", n)
		}
	}
}

func getFundersList(win *AddressWindow) []FundersEntry {
	var list []FundersEntry
	for addr, usd := range win.Funders {
		list = append(list, FundersEntry{Address: addr, Usd: usd})
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].Usd > list[i].Usd {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	return list
}

func getPrimaryFunder(win *AddressWindow) string {
	var maxFunder string
	var maxUsd float64
	for addr, usd := range win.Funders {
		if usd > maxUsd {
			maxUsd = usd
			maxFunder = addr
		}
	}
	return maxFunder
}
