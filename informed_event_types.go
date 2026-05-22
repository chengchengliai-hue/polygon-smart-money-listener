package main

// ═══════════════════════════════════════════
// Wallet linking
// ═══════════════════════════════════════════

type WalletType string

const (
	WalletEOA            WalletType = "EOA"
	WalletPolyProxy      WalletType = "POLY_PROXY"
	WalletGnosisSafe     WalletType = "GNOSIS_SAFE"
	WalletDeposit        WalletType = "DEPOSIT_WALLET"
	WalletSessionSigner  WalletType = "SESSION_SIGNER"
)

type LinkedWallet struct {
	Address    string     `json:"address"`
	Type       WalletType `json:"type"`
}

type RiskWalletEntry struct {
	RootAddress    string         `json:"root_eoa"`
	RiskScore      int            `json:"risk_score"`
	LinkedWallets  []LinkedWallet `json:"linked_addresses"`
	Tags           []string       `json:"tags"`
}

// ═══════════════════════════════════════════
// Token outcome mapping
// ═══════════════════════════════════════════

type TokenOutcome struct {
	TokenID      string `json:"token_id"`
	ConditionID  string `json:"condition_id"`
	MarketSlug   string `json:"market_slug"`
	Question     string `json:"market_question"`
	Outcome      string `json:"outcome"`
	OutcomeIndex int    `json:"outcome_index"`
	Category     string `json:"event_category"`
	Liquidity    float64 `json:"liquidity"`
	Volume       float64 `json:"volume"`
	EndDate      string `json:"end_date"`
}

// ═══════════════════════════════════════════
// Decoded Polymarket trade
// ═══════════════════════════════════════════

type DecodedTrade struct {
	TxHash       string
	LogIndex     uint
	BlockNumber  uint64
	OrderHash    string
	Maker        string
	Taker        string
	MakerAssetID string
	TakerAssetID string
	MakerAmount  float64
	TakerAmount  float64
	Fee          float64
}

// ═══════════════════════════════════════════
// Matched informed event
// ═══════════════════════════════════════════

type MatchedTrade struct {
	DecodedTrade
	MatchedWallet     string
	MatchedWalletType WalletType
	MatchedRole       string
	RootAddress       string
	TokenOutcome      *TokenOutcome
	Action            string // BUY / SELL
	Direction         string // bullish_yes / bearish_yes / unknown
}

// ═══════════════════════════════════════════
// Scored informed event
// ═══════════════════════════════════════════

type InformedScoredEvent struct {
	MatchedTrade
	RiskScore int
	Tags      []string
	Severity  string
	IsHedged  bool
}

// ═══════════════════════════════════════════
// Alert output
// ═══════════════════════════════════════════

type InformedEventAlert struct {
	SchemaVersion   string              `json:"schema_version"`
	EventType       string              `json:"event_type"`
	Severity        string              `json:"severity"`
	ConfidenceLevel string              `json:"confidence_level"`
	Chain           string              `json:"chain"`
	Source          string              `json:"source"`
	Data            InformedEventData   `json:"data"`
}

type InformedEventData struct {
	RootWalletAddress    string   `json:"root_wallet_address"`
	MatchedWalletAddress string   `json:"matched_wallet_address"`
	MatchedWalletType    string   `json:"matched_wallet_type"`
	MatchedRole          string   `json:"matched_role"`
	EventCategory        string   `json:"event_category"`
	MarketQuestion       string   `json:"market_question"`
	ConditionID          string   `json:"condition_id"`
	TokenID              string   `json:"token_id"`
	Outcome              string   `json:"outcome"`
	OutcomeIndex         int      `json:"outcome_index"`
	Action               string   `json:"action"`
	Direction            string   `json:"direction"`
	EstimatedUsdc        float64  `json:"estimated_usdc"`
	AvgPrice             float64  `json:"avg_price"`
	RiskScore            int      `json:"risk_score"`
	Tags                 []string `json:"tags"`
	TxHash               string   `json:"tx_hash"`
	LogIndex             uint     `json:"log_index"`
	BlockNumber          uint64   `json:"block_number"`
	DetectedAt           string   `json:"detected_at"`
}

// ═══════════════════════════════════════════
// Gamma API market
// ═══════════════════════════════════════════

type GammaEvent struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Slug        string         `json:"slug"`
	Category    string         `json:"category"`
	Liquidity   string         `json:"liquidity"`
	Volume      string         `json:"volume"`
	EndDate     string         `json:"end_date"`
	Markets     []GammaMarket  `json:"markets"`
}

type GammaMarket struct {
	ID                string           `json:"id"`
	Question          string           `json:"question"`
	ConditionID       string           `json:"condition_id"`
	Outcomes          []string         `json:"outcomes"`
	OutcomePrices     []string         `json:"outcome_prices"`
	ClobTokenIDs      []string         `json:"clob_token_ids"`
}
