package main

import (
	"net/http"
	"net/url"
	"time"
)

type InformedConfig struct {
	CtfExchange      string
	NegRiskExchange  string
	GammaBaseURL     string
	ClobBaseURL      string
	WalletRefreshSec int
	LinkRefreshSec   int
	MarketRefreshSec int
	AlertThreshold   int
	HighThreshold    int
	MinTradeUsdc     float64
	WindowSeconds    int
	MinLiquidity     float64
	HedgePenalty     int
	HttpProxy        string
}

var informedConfig InformedConfig
var httpClient *http.Client

func loadInformedConfig() {
	proxyURL := getEnv("HTTP_PROXY", getEnv("HTTPS_PROXY", getEnv("http_proxy", "")))
	if proxyURL == "" {
		proxyURL = getEnv("POLYMARKET_PROXY", "")
	}

	informedConfig = InformedConfig{
		CtfExchange:      getEnv("POLYMARKET_CTF_EXCHANGE", "0xE111180000d2663C0091e4f400237545B87B996B"),
		NegRiskExchange:  getEnv("POLYMARKET_NEG_RISK_EXCHANGE", "0xe2222d279d744050d28e00520010520000310F59"),
		GammaBaseURL:     getEnv("POLYMARKET_GAMMA_BASE", "https://gamma-api.polymarket.com"),
		ClobBaseURL:      getEnv("POLYMARKET_CLOB_BASE", "https://clob.polymarket.com"),
		WalletRefreshSec: getEnvInt("RISK_WALLET_REFRESH_SECONDS", 60),
		LinkRefreshSec:   getEnvInt("RISK_WALLET_LINK_REFRESH_SECONDS", 600),
		MarketRefreshSec: getEnvInt("INFORMED_MARKET_REFRESH_SECONDS", 300),
		AlertThreshold:   getEnvInt("INFORMED_ALERT_THRESHOLD", 70),
		HighThreshold:    getEnvInt("INFORMED_HIGH_ALERT_THRESHOLD", 90),
		MinTradeUsdc:     getEnvFloat("INFORMED_MIN_TRADE_USDC", 5000),
		WindowSeconds:    getEnvInt("INFORMED_WINDOW_SECONDS", 900),
		MinLiquidity:     getEnvFloat("INFORMED_MARKET_MIN_LIQUIDITY", 1000),
		HedgePenalty:     getEnvInt("HEDGE_PENALTY", -50),
		HttpProxy:        proxyURL,
	}

	// Build HTTP client with proxy
	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err == nil {
			httpClient = &http.Client{
				Transport: &http.Transport{Proxy: http.ProxyURL(proxy)},
				Timeout:   30 * time.Second,
			}
			return
		}
	}
	httpClient = &http.Client{Timeout: 30 * time.Second}
}

func init() {
	loadInformedConfig()
}
