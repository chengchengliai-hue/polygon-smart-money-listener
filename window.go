package main

import (
	"fmt"
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
// Returns true if duplicate, false if new
func isDuplicate(txHash string, logIndex uint) bool {
	key := fmt.Sprintf("%s_%d", txHash, logIndex)
	processedMu.Lock()
	defer processedMu.Unlock()

	now := time.Now().Unix()
	if expiry, ok := processedLogs[key]; ok && expiry > now {
		return true
	}
	processedLogs[key] = now + 900 // 15 minute TTL
	return false
}

// removeProcessedLog removes a log from the dedup cache (for reorg reversal)
func removeProcessedLog(txHash string, logIndex uint) {
	key := fmt.Sprintf("%s_%d", txHash, logIndex)
	processedMu.Lock()
	delete(processedLogs, key)
	processedMu.Unlock()
}

// cleanProcessedLogs removes expired entries from the dedup cache
func cleanProcessedLogs() {
	processedMu.Lock()
	defer processedMu.Unlock()
	now := time.Now().Unix()
	for key, expiry := range processedLogs {
		if expiry < now {
			delete(processedLogs, key)
		}
	}
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
