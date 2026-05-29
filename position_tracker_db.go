package main

import "database/sql"

func initPositionTrackerTables(database *sql.DB) {
	database.Exec(`
		CREATE TABLE IF NOT EXISTS tracked_positions (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			wallet        TEXT NOT NULL,
			market_slug   TEXT NOT NULL,
			market_title  TEXT NOT NULL,
			token_type    TEXT NOT NULL,
			tracked_amount REAL NOT NULL,
			entry_score   INTEGER DEFAULT 0,
			status        TEXT DEFAULT 'active',
			created_at    TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(wallet, market_slug, token_type)
		);
	`)
}

func insertTrackedPosition(wallet, marketSlug, marketTitle, tokenType string, amount float64, score int) (int64, error) {
	dbWriteMu.Lock()
	defer dbWriteMu.Unlock()

	result, err := db.Exec(
		`INSERT OR IGNORE INTO tracked_positions (wallet, market_slug, market_title, token_type, tracked_amount, entry_score)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		wallet, marketSlug, marketTitle, tokenType, amount, score,
	)
	if err != nil {
		return 0, err
	}

	id, _ := result.LastInsertId()
	if id == 0 {
		// Already exists (IGNORE'd), get existing ID
		db.QueryRow(
			`SELECT id FROM tracked_positions WHERE wallet=? AND market_slug=? AND token_type=?`,
			wallet, marketSlug, tokenType,
		).Scan(&id)
	}
	return id, nil
}

func getActiveTrackedPositions() []TrackedPosition {
	rows, err := db.Query(
		`SELECT id, wallet, market_slug, market_title, token_type, tracked_amount, entry_score, status, created_at, updated_at
		 FROM tracked_positions WHERE status='active' ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []TrackedPosition
	for rows.Next() {
		var p TrackedPosition
		rows.Scan(&p.ID, &p.Wallet, &p.MarketSlug, &p.MarketTitle, &p.TokenType,
			&p.TrackedAmount, &p.EntryScore, &p.Status, &p.CreatedAt, &p.UpdatedAt)
		results = append(results, p)
	}
	return results
}

func markPositionExited(id int64) {
	dbWriteMu.Lock()
	db.Exec(`UPDATE tracked_positions SET status='exited', updated_at=datetime('now') WHERE id=?`, id)
	dbWriteMu.Unlock()
}

func updateTrackedAmountDB(wallet, marketSlug, tokenType string, newAmount float64) {
	dbWriteMu.Lock()
	db.Exec(
		`UPDATE tracked_positions SET tracked_amount=?, updated_at=datetime('now') WHERE wallet=? AND market_slug=? AND token_type=? AND status='active'`,
		newAmount, wallet, marketSlug, tokenType,
	)
	dbWriteMu.Unlock()
}
