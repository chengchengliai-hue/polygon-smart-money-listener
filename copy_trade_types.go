package main

// CopyPosition represents an auto-copied position with real trading tracking.
type CopyPosition struct {
	ID          int64
	Wallet      string
	MarketSlug  string
	MarketTitle string
	TokenID     string
	TokenType   string // YES or NO
	ConditionID string

	EntryPrice float64 // VWAP entry price
	Shares     float64 // planned shares
	TotalCost  float64 // planned USDC spent

	// Real order tracking
	RealOrderID      string
	RealFilledShares float64
	RealFilledPrice  float64
	RealSellOrderID  string
	RealSellPrice    float64

	AlertScore  int
	AlertSource string // "risk_pool" or "native_discovery"

	RealizedPnl float64
	Status      string // active / closed / resolved
	FinalPnl    float64

	CreatedAt  string
	UpdatedAt  string
	ResolvedAt string
}

// CopyTradeLog records each lifecycle action on a copy position
type CopyTradeLog struct {
	ID         int64
	PositionID int64
	Action     string  // open / close / resolve
	Price      float64
	Shares     float64
	Cost       float64 // positive=spent, negative=received
	Pnl        float64
	TriggerTx  string
	CreatedAt  string
}
