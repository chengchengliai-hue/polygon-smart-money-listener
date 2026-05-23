package main

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	WsRpcUrl           string
	HttpRpcUrl         string
	BalanceThresholdUsd float64
	WindowSeconds      int
	ConfirmationBlocks int
	SqlitePath         string
}

var config Config

// Known CEX hot wallets on Polygon (lowercase)
var knownCex = map[string]bool{
	"0x21a31ee1afc51d94c2efccaa2092ad1028285549": true, // Binance
	"0xf89d7b9c864f589bbf53a82105107622b35eaa40": true, // Bybit
	"0x0639556f03714a0af48f0e7b205375fbb2ec3e4c": true, // OKX
	"0x5f65f7b609678448494de4c87521cdf6cef1e532": true, // Gate.io
	"0x6262998ced04146fa42253a5c0af90ca02dfd2a3": true, // Crypto.com
	"0xe7804c37c13166ff0b37f5ae0bb07a3aebb6e245": true, // Coinbase
}

func loadConfig() {
	_ = godotenv.Load(".env")

	config.WsRpcUrl = getEnv("POLYGON_WS_RPC_URL", "wss://polygon-mainnet.g.alchemy.com/v2/demo")
	config.HttpRpcUrl = getEnv("POLYGON_HTTP_RPC_URL", "https://polygon-mainnet.g.alchemy.com/v2/demo")
	config.BalanceThresholdUsd = getEnvFloat("BALANCE_THRESHOLD_USD", 10000)
	config.WindowSeconds = getEnvInt("WINDOW_SECONDS", 900)
	config.ConfirmationBlocks = getEnvInt("CONFIRMATION_BLOCKS", 16)
	config.SqlitePath = getEnv("SQLITE_PATH", "polygon_smart_money_watch.db")
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return defaultVal
}

func getEnvFloat(key string, defaultVal float64) float64 {
	if v := os.Getenv(key); v != "" {
		v = strings.TrimSpace(v)
		n, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return n
		}
	}
	return defaultVal
}

func init() {
	loadConfig()
	log.SetFlags(log.Ltime)
}

var globalWhitelist = map[string]bool{
	"0x21a31ee1afc51d94c2efccaa2092ad1028285549": true,
	"0xf89d7b9c864f589bbf53a82105107622b35eaa40": true,
	"0x0639556f03714a0af48f0e7b205375fbb2ec3e4c": true,
	"0x5f65f7b609678448494de4c87521cdf6cef1e532": true,
	"0x6262998ced04146fa42253a5c0af90ca02dfd2a3": true,
	"0xe7804c37c13166ff0b37f5ae0bb07a3aebb6e245": true,
	"0x68b3465833fb72a70ecdf485e0e4c7bd8665fc45": true,
	"0x1111111254eeb25477b68fb85ed929f73a960582": true,
	"0xdef171fe48cf0115b1d80b88dc8eab59176fee57": true,
	"0xa0c68c638235ee32657e8f720a23cec1bfc77c77": true,
	"0x7a4b5a56256163f07b2c80a7ca55abe66c4ec4d7": true,
	"0xa160cdab225685da1d56aa342ad8841c3b53f291": true,
}

func isWhitelisted(addr string) bool {
	return globalWhitelist[addr]
}
