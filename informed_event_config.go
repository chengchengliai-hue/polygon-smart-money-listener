package main

type InformedConfig struct {
	CtfExchange      string
	NegRiskExchange  string
	WalletRefreshSec int
	LinkRefreshSec   int
	MarketRefreshSec int
	AlertThreshold   int
	HighThreshold    int
	MinTradeUsdc     float64
	WindowSeconds    int
	MinLiquidity     float64
	HedgePenalty     int
}

var informedConfig InformedConfig

func loadInformedConfig() {
	informedConfig = InformedConfig{
		CtfExchange:      getEnv("POLYMARKET_CTF_EXCHANGE", "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E"),
		NegRiskExchange:  getEnv("POLYMARKET_NEG_RISK_EXCHANGE", "0xC5d563A36AE78145C45a50134d48A1215220f80a"),
		WalletRefreshSec: getEnvInt("RISK_WALLET_REFRESH_SECONDS", 60),
		LinkRefreshSec:   getEnvInt("RISK_WALLET_LINK_REFRESH_SECONDS", 600),
		MarketRefreshSec: getEnvInt("INFORMED_MARKET_REFRESH_SECONDS", 300),
		AlertThreshold:   getEnvInt("INFORMED_ALERT_THRESHOLD", 70),
		HighThreshold:    getEnvInt("INFORMED_HIGH_ALERT_THRESHOLD", 90),
		MinTradeUsdc:     getEnvFloat("INFORMED_MIN_TRADE_USDC", 5000),
		WindowSeconds:    getEnvInt("INFORMED_WINDOW_SECONDS", 900),
		MinLiquidity:     getEnvFloat("INFORMED_MARKET_MIN_LIQUIDITY", 1000),
		HedgePenalty:     getEnvInt("HEDGE_PENALTY", -50),
	}
}

func init() {
	loadInformedConfig()
}
