package main

import (
	"sync"
	"time"
)

var (
	windows   = make(map[string]*AddressWindow)
	windowsMu sync.Mutex
)

func addTransfer(event *TransferEvent) {
	windowsMu.Lock()
	defer windowsMu.Unlock()

	// Track both incoming (to) and outgoing (from) for each address
	// For the TO address: this is an inflow
	addToWindow(event.To, event, event.ValueUsd)
	// For the FROM address: this is an outflow (subtract from their net)
	addToWindow(event.From, event, -event.ValueUsd)
}

func addToWindow(address string, event *TransferEvent, usdDelta float64) {
	// Skip zero address
	if address == "0x0000000000000000000000000000000000000000" {
		return
	}

	win, ok := windows[address]
	if !ok {
		win = &AddressWindow{
			Address:   address,
			Transfers: []TransferEvent{},
			Funders:   make(map[string]float64),
			FirstSeen: event.Timestamp,
			LastSeen:  event.Timestamp,
		}
		windows[address] = win
	}

	if event.Timestamp < win.FirstSeen {
		win.FirstSeen = event.Timestamp
	}
	if event.Timestamp > win.LastSeen {
		win.LastSeen = event.Timestamp
	}

	win.Transfers = append(win.Transfers, *event)
	win.TxCount++
	win.TotalUsd += usdDelta
	if usdDelta > 0 {
		win.GrossInflow += usdDelta
		win.Funders[event.From] += usdDelta
	}
}

// getExceededWindows returns addresses whose window has CLOSED (15 min elapsed) and net inflow >= threshold
func getExceededWindows() []*AddressWindow {
	windowsMu.Lock()
	defer windowsMu.Unlock()

	now := time.Now().Unix()
	windowSec := int64(config.WindowSeconds)
	var results []*AddressWindow

	for key, win := range windows {
		windowAge := now - win.FirstSeen
		// Only alert after the full window has passed
		if windowAge >= windowSec && win.TotalUsd >= config.BalanceThresholdUsd {
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
	threshold := now - int64(config.WindowSeconds*2) // Keep for 2x window for net flow tracking
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
