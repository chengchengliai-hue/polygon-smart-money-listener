package main

// CopyPosition represents an auto-copied virtual position for backtesting
type CopyPosition struct {
	ID          int64
	Wallet      string
	MarketSlug  string
	MarketTitle string
	TokenID     string
	TokenType   string // YES or NO
	ConditionID string

	EntryPrice float64 // price per share at entry
	Shares     float64 // number of shares bought
	TotalCost  float64 // total USDC spent

	AlertScore  int    // original alert score
	AlertSource string // "risk_pool" or "native_discovery"

	RealizedPnl float64 // P&L from partial/full exits before resolution
	Status      string  // active / closed / resolved
	FinalPnl    float64 // final P&L after resolution

	CreatedAt  string
	UpdatedAt  string
	ResolvedAt string
}

// CopyTradeLog records each lifecycle action on a copy position
type CopyTradeLog struct {
	ID         int64
	PositionID int64
	Action     string  // open / close / resolve
	Price      float64 // trade price
	Shares     float64
	Cost       float64 // positive=spent, negative=received
	Pnl        float64 // realized P&L for this action
	TriggerTx  string  // source wallet tx that triggered this action
	CreatedAt  string
}
