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
		CtfExchange:      getEnv("POLYMARKET_CTF_EXCHANGE", "0xE111180000d2663C0091e4f400237545B87B996B"),
		NegRiskExchange:  getEnv("POLYMARKET_NEG_RISK_EXCHANGE", "0xe2222d279d744050d28e00520010520000310F59"),
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
