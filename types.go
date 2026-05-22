package main

import "math/big"

type TransferEvent struct {
	TxHash      string
	BlockNumber uint64
	From        string
	To          string
	Value       *big.Int
	ValueUsd    float64
	Token       string
	Timestamp   int64
}

type AddressWindow struct {
	Address     string
	Transfers   []TransferEvent
	TotalUsd    float64 // Net inflow (in - out)
	GrossInflow float64 // Total positive inflow only
	TxCount     int
	Funders     map[string]float64
	FirstSeen   int64
	LastSeen    int64
}

type FundersEntry struct {
	Address string  `json:"address"`
	Usd     float64 `json:"usd"`
}

type ScoredAddress struct {
	Address             string
	TotalUsd            float64
	TxCount             int
	Funders             []FundersEntry
	PrimaryFunder       string
	WindowSeconds       int64
	Score               int
	Tags                []string
	IsNewWallet         bool
	Nonce               *int64
	IsContract          bool
	FromWhale           bool
	FromKnownCex        bool
	IsSplitAccumulation bool
}

type WhaleAlert struct {
	SchemaVersion  string       `json:"schema_version"`
	EventType      string       `json:"event_type"`
	Severity       string       `json:"severity"`
	ConfidenceLevel string      `json:"confidence_level"`
	Chain          string       `json:"chain"`
	Data           WhaleAlertData `json:"data"`
}

type WhaleAlertData struct {
	TargetAddress          string         `json:"target_address"`
	PrimaryFunderAddress   string         `json:"primary_funder_address"`
	Funders                []FundersEntry `json:"funders"`
	TotalUsdAccumulated    float64        `json:"total_usd_accumulated"`
	AccumulationWindowSec  int64          `json:"accumulation_window_seconds"`
	TxCountInWindow        int            `json:"tx_count_in_window"`
	RiskScore              int            `json:"risk_score"`
	Tags                   []string       `json:"tags"`
	DetectedAt             string         `json:"detected_at"`
}

type StatusMessage struct {
	SchemaVersion string         `json:"schema_version"`
	EventType     string         `json:"event_type"`
	Timestamp     string         `json:"timestamp"`
	Data          StatusData     `json:"data"`
}

type StatusData struct {
	Processed  int64 `json:"processed"`
	Alerts     int64 `json:"alerts"`
	WindowSize int   `json:"window_size"`
}
