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
