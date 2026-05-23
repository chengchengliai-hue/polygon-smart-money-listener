package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB
var dbWriteMu sync.Mutex

func initDB() {
	var err error
	db, err = sql.Open("sqlite3", config.SqlitePath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		log.Fatalf("[db] open: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS seen_addresses (
			address TEXT PRIMARY KEY,
			first_seen_block INTEGER NOT NULL,
			first_seen_at TEXT NOT NULL DEFAULT (datetime('now')),
			nonce_last_checked INTEGER,
			is_contract INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS whale_alerts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			address TEXT NOT NULL,
			primary_funder_address TEXT NOT NULL,
			total_usd REAL NOT NULL,
			score INTEGER NOT NULL,
			severity TEXT NOT NULL CHECK(severity IN ('watch','normal','high')),
			tags TEXT NOT NULL DEFAULT '[]',
			alerted_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_whale_address ON whale_alerts(address);
		CREATE INDEX IF NOT EXISTS idx_whale_alerted ON whale_alerts(alerted_at);
		CREATE TABLE IF NOT EXISTS runtime_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS address_labels (
			address TEXT PRIMARY KEY,
			label TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		log.Fatalf("[db] init schema: %v", err)
	}

	// Polymarket informed event tables
	initInformedTables(db)
}

func isAddressSeen(address string) bool {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM seen_addresses WHERE address = ?", strings.ToLower(address)).Scan(&count)
	return err == nil && count > 0
}

func markAddressSeen(address string, blockNumber uint64, nonce *int64, isContract bool) {
	contractVal := 0
	if isContract {
		contractVal = 1
	}
	var nonceVal interface{}
	if nonce != nil {
		nonceVal = *nonce
	}
	dbWriteMu.Lock()
	_, _ = db.Exec(
		"INSERT OR IGNORE INTO seen_addresses (address, first_seen_block, nonce_last_checked, is_contract) VALUES (?, ?, ?, ?)",
		strings.ToLower(address), blockNumber, nonceVal, contractVal,
	)
	dbWriteMu.Unlock()
}

func saveWhaleAlert(address, primaryFunder string, totalUsd float64, score int, severity string, tags []string) {
	tagsJson, _ := json.Marshal(tags)
	dbWriteMu.Lock()
	_, _ = db.Exec(
		"INSERT INTO whale_alerts (address, primary_funder_address, total_usd, score, severity, tags) VALUES (?, ?, ?, ?, ?, ?)",
		strings.ToLower(address), strings.ToLower(primaryFunder), totalUsd, score, severity, string(tagsJson),
	)
	dbWriteMu.Unlock()
}

func isWhaleAlerted(address string) bool {
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM whale_alerts WHERE address = ? AND alerted_at > datetime('now', '-7 days')",
		strings.ToLower(address),
	).Scan(&count)
	return err == nil && count > 0
}

func saveRuntimeState(key, value string) {
	dbWriteMu.Lock()
	_, _ = db.Exec("INSERT OR REPLACE INTO runtime_state (key, value) VALUES (?, ?)", key, value)
	dbWriteMu.Unlock()
}

func getRuntimeState(key string) string {
	var value string
	err := db.QueryRow("SELECT value FROM runtime_state WHERE key = ?", key).Scan(&value)
	if err != nil {
		return ""
	}
	return value
}

func getAddressLabel(address string) string {
	var label string
	err := db.QueryRow("SELECT label FROM address_labels WHERE address = ?", address).Scan(&label)
	if err != nil {
		return ""
	}
	return label
}
