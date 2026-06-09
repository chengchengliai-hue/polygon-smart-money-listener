package main

import "database/sql"

func initCopyTradeTables(database *sql.DB) {
	database.Exec(`
		CREATE TABLE IF NOT EXISTS copy_positions (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			wallet        TEXT NOT NULL,
			market_slug   TEXT,
			market_title  TEXT,
			token_id      TEXT NOT NULL,
			token_type    TEXT NOT NULL,
			condition_id  TEXT,
			entry_price   REAL NOT NULL,
			shares        REAL NOT NULL,
			total_cost    REAL NOT NULL,
			alert_score   INTEGER,
			alert_source  TEXT,
			realized_pnl  REAL DEFAULT 0,
			status        TEXT DEFAULT 'active',
			final_pnl     REAL,
			created_at    TEXT DEFAULT (datetime('now')),
			updated_at    TEXT DEFAULT (datetime('now')),
			resolved_at   TEXT,
			real_order_id       TEXT DEFAULT '',
			real_filled_shares  REAL DEFAULT 0,
			real_filled_price   REAL DEFAULT 0,
			real_sell_order_id  TEXT DEFAULT '',
			real_sell_price     REAL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS copy_trade_logs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			position_id INTEGER NOT NULL,
			action      TEXT NOT NULL,
			price       REAL NOT NULL,
			shares      REAL NOT NULL,
			cost        REAL NOT NULL,
			pnl         REAL,
			trigger_tx  TEXT,
			created_at  TEXT DEFAULT (datetime('now'))
		);
	`)

	// Migrate existing tables that may lack real order columns
	database.Exec(`ALTER TABLE copy_positions ADD COLUMN real_order_id TEXT DEFAULT ''`)
	database.Exec(`ALTER TABLE copy_positions ADD COLUMN real_filled_shares REAL DEFAULT 0`)
	database.Exec(`ALTER TABLE copy_positions ADD COLUMN real_filled_price REAL DEFAULT 0`)
	database.Exec(`ALTER TABLE copy_positions ADD COLUMN real_sell_order_id TEXT DEFAULT ''`)
	database.Exec(`ALTER TABLE copy_positions ADD COLUMN real_sell_price REAL DEFAULT 0`)
}

func insertCopyPosition(p *CopyPosition) (int64, error) {
	dbWriteMu.Lock()
	defer dbWriteMu.Unlock()
	result, err := db.Exec(
		`INSERT INTO copy_positions (wallet, market_slug, market_title, token_id, token_type, condition_id,
		 entry_price, shares, total_cost, alert_score, alert_source,
		 real_order_id, real_filled_shares, real_filled_price)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Wallet, p.MarketSlug, p.MarketTitle, p.TokenID, p.TokenType, p.ConditionID,
		p.EntryPrice, p.Shares, p.TotalCost, p.AlertScore, p.AlertSource,
		p.RealOrderID, p.RealFilledShares, p.RealFilledPrice,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func getActiveCopyPositions() []CopyPosition {
	rows, err := db.Query(
		`SELECT id, wallet, market_slug, market_title, token_id, token_type, condition_id,
		 entry_price, shares, total_cost, alert_score, alert_source,
		 realized_pnl, status, COALESCE(final_pnl,0), created_at, updated_at, COALESCE(resolved_at,''),
		 COALESCE(real_order_id,''), COALESCE(real_filled_shares,0), COALESCE(real_filled_price,0),
		 COALESCE(real_sell_order_id,''), COALESCE(real_sell_price,0)
		 FROM copy_positions WHERE status='active' ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanCopyPositions(rows)
}

func getAllCopyPositions() []CopyPosition {
	rows, err := db.Query(
		`SELECT id, wallet, market_slug, market_title, token_id, token_type, condition_id,
		 entry_price, shares, total_cost, alert_score, alert_source,
		 realized_pnl, status, COALESCE(final_pnl,0), created_at, updated_at, COALESCE(resolved_at,''),
		 COALESCE(real_order_id,''), COALESCE(real_filled_shares,0), COALESCE(real_filled_price,0),
		 COALESCE(real_sell_order_id,''), COALESCE(real_sell_price,0)
		 FROM copy_positions ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanCopyPositions(rows)
}

func scanCopyPositions(rows *sql.Rows) []CopyPosition {
	var results []CopyPosition
	for rows.Next() {
		var p CopyPosition
		rows.Scan(&p.ID, &p.Wallet, &p.MarketSlug, &p.MarketTitle,
			&p.TokenID, &p.TokenType, &p.ConditionID,
			&p.EntryPrice, &p.Shares, &p.TotalCost, &p.AlertScore, &p.AlertSource,
			&p.RealizedPnl, &p.Status, &p.FinalPnl, &p.CreatedAt, &p.UpdatedAt, &p.ResolvedAt,
			&p.RealOrderID, &p.RealFilledShares, &p.RealFilledPrice,
			&p.RealSellOrderID, &p.RealSellPrice)
		results = append(results, p)
	}
	return results
}

func addToCopyPosition(id int64, additionalShares float64, additionalCost float64) {
	dbWriteMu.Lock()
	db.Exec(`UPDATE copy_positions SET
		shares = shares + ?,
		total_cost = total_cost + ?,
		entry_price = CASE WHEN shares + ? > 0 THEN (total_cost + ?) / (shares + ?) ELSE entry_price END,
		updated_at = datetime('now')
		WHERE id = ?`,
		additionalShares, additionalCost,
		additionalShares, additionalCost, additionalShares,
		id)
	dbWriteMu.Unlock()
}

func closeCopyPosition(id int64, sellPrice float64, pnl float64) {
	dbWriteMu.Lock()
	db.Exec(`UPDATE copy_positions SET status='closed', realized_pnl=?, final_pnl=?, updated_at=datetime('now') WHERE id=?`,
		pnl, pnl, id)
	dbWriteMu.Unlock()
}

func updateCopySellOrder(id int64, sellOrderID string, sellPrice float64) {
	dbWriteMu.Lock()
	db.Exec(`UPDATE copy_positions SET real_sell_order_id=?, real_sell_price=?, updated_at=datetime('now') WHERE id=?`,
		sellOrderID, sellPrice, id)
	dbWriteMu.Unlock()
}

func resolveCopyPosition(id int64, winner bool, pnl float64) {
	dbWriteMu.Lock()
	db.Exec(`UPDATE copy_positions SET status='resolved', final_pnl=?, resolved_at=datetime('now'), updated_at=datetime('now') WHERE id=?`,
		pnl, id)
	dbWriteMu.Unlock()
}

func insertCopyTradeLog(log *CopyTradeLog) {
	dbWriteMu.Lock()
	db.Exec(
		`INSERT INTO copy_trade_logs (position_id, action, price, shares, cost, pnl, trigger_tx) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		log.PositionID, log.Action, log.Price, log.Shares, log.Cost, log.Pnl, log.TriggerTx,
	)
	dbWriteMu.Unlock()
}
