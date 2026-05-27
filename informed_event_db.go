package main

import (
	"database/sql"
	"encoding/json"
	"time"
)

func initInformedTables(db *sql.DB) {
	db.Exec(`
		CREATE TABLE IF NOT EXISTS risk_wallet_links (
			linked_address TEXT PRIMARY KEY,
			root_address TEXT NOT NULL,
			wallet_type TEXT NOT NULL,
			source TEXT NOT NULL,
			risk_score INTEGER NOT NULL DEFAULT 0,
			tags TEXT NOT NULL DEFAULT '[]',
			last_verified_at TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS informed_markets (
			token_id TEXT PRIMARY KEY,
			condition_id TEXT NOT NULL,
			market_slug TEXT,
			question TEXT NOT NULL,
			outcome TEXT NOT NULL,
			outcome_index INTEGER,
			category TEXT NOT NULL,
			liquidity REAL,
			volume REAL,
			end_date TEXT,
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS polymarket_events_seen (
			event_key TEXT PRIMARY KEY,
			tx_hash TEXT NOT NULL,
			log_index INTEGER NOT NULL,
			block_number INTEGER NOT NULL,
			seen_at TEXT NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS wallet_condition_activity (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			root_address TEXT NOT NULL,
			condition_id TEXT NOT NULL,
			outcome TEXT NOT NULL,
			action TEXT NOT NULL,
			estimated_usdc REAL NOT NULL,
			tx_hash TEXT NOT NULL,
			block_number INTEGER NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS informed_event_alerts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL DEFAULT '',
			root_address TEXT NOT NULL,
			matched_address TEXT NOT NULL,
			matched_wallet_type TEXT,
			condition_id TEXT,
			token_id TEXT,
			outcome TEXT,
			action TEXT,
			category TEXT,
			estimated_usdc REAL,
			score INTEGER NOT NULL,
			severity TEXT NOT NULL,
			tags TEXT NOT NULL DEFAULT '[]',
			tx_hash TEXT,
			log_index INTEGER,
			block_number INTEGER,
			alerted_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`)

	// Migration: add event_type column for existing DBs
	db.Exec(`ALTER TABLE informed_event_alerts ADD COLUMN event_type TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE informed_event_alerts ADD COLUMN market_question TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE informed_event_alerts ADD COLUMN market_slug TEXT NOT NULL DEFAULT ''`)
}

func loadWhaleAddresses() []RiskWalletEntry {
	rows, err := db.Query(`
		SELECT a.address, a.score, a.tags, 
		       COALESCE((SELECT MAX(alerted_at) FROM whale_alerts WHERE address = a.address), datetime('now')) as last_active
		FROM (SELECT DISTINCT address, score, tags FROM whale_alerts WHERE score >= 70 ORDER BY score DESC LIMIT 500) a
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []RiskWalletEntry
	for rows.Next() {
		var addr string
		var score int
		var tagsJson string
		var lastActive string
		rows.Scan(&addr, &score, &tagsJson, &lastActive)

		var tags []string
		json.Unmarshal([]byte(tagsJson), &tags)

		// Parse last active to unix timestamp for TTL
		lastActiveUnix := int64(0)
		if t, err := time.Parse("2006-01-02 15:04:05", lastActive); err == nil {
			lastActiveUnix = t.Unix()
		}

		results = append(results, RiskWalletEntry{
			RootAddresses: []string{addr},
			RiskScore:     score,
			Tags:          tags,
			LastActive:    lastActiveUnix,
			LinkedWallets: []LinkedWallet{
				{Address: addr, Type: WalletEOA},
			},
		})
	}
	return results
}

func saveRiskWalletLink(linkedAddr, rootAddr string, wType WalletType, source string, score int, tags []string) {
	tagsJson, _ := json.Marshal(tags)
	dbWriteMu.Lock()
	db.Exec(
		`INSERT OR REPLACE INTO risk_wallet_links (linked_address, root_address, wallet_type, source, risk_score, tags, last_verified_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		linkedAddr, rootAddr, string(wType), source, score, string(tagsJson),
	)
	dbWriteMu.Unlock()
}

func isPolymarketEventSeen(eventKey string) bool {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM polymarket_events_seen WHERE event_key = ?", eventKey).Scan(&count)
	return count > 0
}

func markPolymarketEventSeen(eventKey, txHash string, logIndex uint, blockNumber uint64) {
	dbWriteMu.Lock()
	db.Exec(
		"INSERT OR IGNORE INTO polymarket_events_seen (event_key, tx_hash, log_index, block_number) VALUES (?, ?, ?, ?)",
		eventKey, txHash, logIndex, blockNumber,
	)
	dbWriteMu.Unlock()
}

func saveWalletConditionActivity(rootAddr, conditionID, outcome, action, txHash string, estimatedUsdc float64, blockNumber uint64) {
	dbWriteMu.Lock()
	db.Exec(
		`INSERT INTO wallet_condition_activity (root_address, condition_id, outcome, action, estimated_usdc, tx_hash, block_number)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rootAddr, conditionID, outcome, action, estimatedUsdc, txHash, blockNumber,
	)
	dbWriteMu.Unlock()
}

func saveInformedAlert(alert InformedEventAlert) {
	if db == nil {
		return
	}
	tagsJson, _ := json.Marshal(alert.Data.Tags)
	dbWriteMu.Lock()
	db.Exec(
		`INSERT INTO informed_event_alerts (event_type, root_address, matched_address, matched_wallet_type, condition_id, token_id, outcome, action, category, estimated_usdc, score, severity, tags, tx_hash, log_index, block_number, market_question, market_slug)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		alert.EventType,
		alert.Data.RootWalletAddress, alert.Data.MatchedWalletAddress, alert.Data.MatchedWalletType,
		alert.Data.ConditionID, alert.Data.TokenID, alert.Data.Outcome, alert.Data.Action,
		alert.Data.EventCategory, alert.Data.EstimatedUsdc, alert.Data.RiskScore, alert.Severity,
		string(tagsJson), alert.Data.TxHash, alert.Data.LogIndex, alert.Data.BlockNumber,
		alert.Data.MarketQuestion, alert.Data.MarketSlug,
	)
	dbWriteMu.Unlock()
}

func getWalletConditionHistory(rootAddr, conditionID string, windowSeconds int) []struct {
	Outcome       string
	Action        string
	EstimatedUsdc float64
} {
	rows, err := db.Query(
		`SELECT outcome, action, estimated_usdc FROM wallet_condition_activity
		 WHERE root_address = ? AND condition_id = ?
		 AND created_at > datetime('now', '-' || ? || ' seconds')`,
		rootAddr, conditionID, windowSeconds,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []struct {
		Outcome       string
		Action        string
		EstimatedUsdc float64
	}
	for rows.Next() {
		var r struct {
			Outcome       string
			Action        string
			EstimatedUsdc float64
		}
		rows.Scan(&r.Outcome, &r.Action, &r.EstimatedUsdc)
		results = append(results, r)
	}
	return results
}
