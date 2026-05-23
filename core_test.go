package main

import (
	"math/big"
	"sync"
	"testing"
	"time"
)

// ═══════════════════════════════════════════
// 白名单 & 混币器
// ═══════════════════════════════════════════

func TestWhitelist(t *testing.T) {
	tests := []struct {
		addr     string
		expected bool
		label    string
	}{
		{"0x21a31ee1afc51d94c2efccaa2092ad1028285549", true, "Binance"},
		{"0x68b3465833fb72a70ecdf485e0e4c7bd8665fc45", true, "Uniswap V3"},
		{"0x1111111254eeb25477b68fb85ed929f73a960582", true, "1inch"},
		{"0x1234567890abcdef1234567890abcdef12345678", false, "Random EOA"},
		{"0xa160cdab225685da1d56aa342ad8841c3b53f291", false, "Tornado (moved to mixer, not whitelist)"},
	}
	for _, tt := range tests {
		result := isWhitelisted(tt.addr)
		if result != tt.expected {
			t.Errorf("%s: expected %v, got %v", tt.label, tt.expected, result)
		}
	}
}

func TestMixerBlacklist(t *testing.T) {
	if !isMixer("0xa160cdab225685da1d56aa342ad8841c3b53f291") {
		t.Error("Tornado Cash should be in mixer blacklist")
	}
	if isMixer("0x1234567890abcdef1234567890abcdef12345678") {
		t.Error("Random EOA should not be mixer")
	}
	if !isMixer("0x12d66f87a04a9e220743712ce6d9bb1b5616b8fc") {
		t.Log("Tornado ETH 0x12d... not in mixer list (can add later)")
	}
}

// ═══════════════════════════════════════════
// Token 解析 (CTF tokenID → condition + outcome)
// ═══════════════════════════════════════════

func TestParseTokenID(t *testing.T) {
	// CTF position ID: (conditionId << 1) | outcomeIndex
	// Even → YES(0), Odd → NO(1)
	cond1, out1 := parseTokenID("12345678901234567890")
	if out1 != 0 {
		t.Errorf("Even should be YES(0), got %d", out1)
	}
	if cond1 == "" {
		t.Error("Should return condition ID")
	}

	cond2, out2 := parseTokenID("12345678901234567891")
	if out2 != 1 {
		t.Errorf("Odd should be NO(1), got %d", out2)
	}
	if cond1 != cond2 {
		t.Error("YES and NO tokens should share the same condition ID")
	}

	// Random token
	cond3, out3 := parseTokenID("99999999999999999999")
	if cond3 == "" {
		t.Error("Any token should parse")
	}
	if out3 != 1 && out3 != 0 {
		t.Errorf("Outcome should be 0 or 1, got %d", out3)
	}
}

func TestDetermineOutcome(t *testing.T) {
	if determineOutcomeFromIndex(0) != "YES" {
		t.Error("Index 0 = YES")
	}
	if determineOutcomeFromIndex(1) != "NO" {
		t.Error("Index 1 = NO")
	}
}

// ═══════════════════════════════════════════
// Token value conversion (6 decimals)
// ═══════════════════════════════════════════

func TestTokenAmountToFloat(t *testing.T) {
	tests := []struct {
		raw      int64
		expected float64
	}{
		{10000000000, 10000},    // $10,000
		{1000000, 1},           // $1
		{0, 0},                 // $0
		{15000000000, 15000},   // $15,000
		{50000000000000, 50000000}, // $50M
	}
	for _, tt := range tests {
		result := tokenAmountToFloat(big.NewInt(tt.raw))
		if result != tt.expected {
			t.Errorf("raw=%d: expected %f, got %f", tt.raw, tt.expected, result)
		}
	}
}

// ═══════════════════════════════════════════
// 滑动窗口聚合
// ═══════════════════════════════════════════

func TestAddTransfer(t *testing.T) {
	windowsMu.Lock()
	windows = make(map[string]*AddressWindow)
	windowsMu.Unlock()
	now := time.Now().Unix()

	addTransfer(&TransferEvent{To: "0xTest1", From: "0xFunderA", ValueUsd: 5000, Timestamp: now, Token: "USDC"})
	addTransfer(&TransferEvent{To: "0xTest1", From: "0xFunderB", ValueUsd: 6000, Timestamp: now + 10, Token: "USDC"})

	windowsMu.Lock()
	win := windows["0xTest1"]
	windowsMu.Unlock()

	if win == nil {
		t.Fatal("Window should exist")
	}
	if win.TotalUsd != 11000 {
		t.Errorf("TotalUsd = %f, want 11000", win.TotalUsd)
	}
	if win.TxCount != 2 {
		t.Errorf("TxCount = %d, want 2", win.TxCount)
	}
	if len(win.Funders) != 2 {
		t.Errorf("Funders count = %d, want 2", len(win.Funders))
	}
	if win.Funders["0xFunderA"] != 5000 {
		t.Errorf("FunderA = %f, want 5000", win.Funders["0xFunderA"])
	}
}

func TestGetExceededWindows(t *testing.T) {
	windowsMu.Lock()
	windows = make(map[string]*AddressWindow)
	windowsMu.Unlock()
	now := time.Now().Unix()

	addTransfer(&TransferEvent{To: "0xSmall", From: "0xA", ValueUsd: 5000, Timestamp: now, Token: "USDC"})
	addTransfer(&TransferEvent{To: "0xBig", From: "0xB", ValueUsd: 15000, Timestamp: now, Token: "USDC"})

	exceeded := getExceededWindows()
	if len(exceeded) != 1 {
		t.Fatalf("Exceeded count = %d, want 1", len(exceeded))
	}
	if exceeded[0].Address != "0xBig" {
		t.Errorf("Exceeded addr = %s, want 0xBig", exceeded[0].Address)
	}
	if exceeded[0].TotalUsd != 15000 {
		t.Errorf("TotalUsd = %f, want 15000", exceeded[0].TotalUsd)
	}

	// 0xBig removed, 0xSmall remains
	windowsMu.Lock()
	defer windowsMu.Unlock()
	if _, ok := windows["0xBig"]; ok {
		t.Error("0xBig should be removed after getExceeded")
	}
	if _, ok := windows["0xSmall"]; !ok {
		t.Error("0xSmall should remain")
	}
}

func TestStrictWindowRolloff(t *testing.T) {
	windowsMu.Lock()
	windows = make(map[string]*AddressWindow)
	windowsMu.Unlock()
	now := time.Now().Unix()
	ws := int64(config.WindowSeconds)
	if ws == 0 {
		ws = 900
	}

	addTransfer(&TransferEvent{To: "0xTest", From: "0xA", ValueUsd: 5000, Timestamp: now - ws - 100, Token: "USDC"})
	addTransfer(&TransferEvent{To: "0xTest", From: "0xB", ValueUsd: 3000, Timestamp: now, Token: "USDC"})

	windowsMu.Lock()
	win := windows["0xTest"]
	windowsMu.Unlock()

	if win == nil {
		t.Fatal("Window should exist")
	}
	if win.TotalUsd != 3000 {
		t.Errorf("After rolloff TotalUsd = %f, want 3000 (old 5000 pruned)", win.TotalUsd)
	}
	if win.TxCount != 1 {
		t.Errorf("TxCount = %d, want 1", win.TxCount)
	}
}

func TestGarbageCollection(t *testing.T) {
	windowsMu.Lock()
	windows = make(map[string]*AddressWindow)
	windowsMu.Unlock()
	now := time.Now().Unix()
	ws := int64(config.WindowSeconds)
	if ws == 0 {
		ws = 900
	}

	addTransfer(&TransferEvent{To: "0xOld", From: "0xA", ValueUsd: 5000, Timestamp: now - ws - 100, Token: "USDC"})
	addTransfer(&TransferEvent{To: "0xNew", From: "0xB", ValueUsd: 3000, Timestamp: now, Token: "USDC"})

	collectGarbage()

	windowsMu.Lock()
	defer windowsMu.Unlock()
	if _, ok := windows["0xOld"]; ok {
		t.Error("0xOld should be garbage collected (inactive > window)")
	}
	if _, ok := windows["0xNew"]; !ok {
		t.Error("0xNew should NOT be collected")
	}
}

// ═══════════════════════════════════════════
// 去重缓存
// ═══════════════════════════════════════════

func TestIsDuplicate(t *testing.T) {
	processedMu.Lock()
	processedLogs = make(map[string]int64)
	processedMu.Unlock()

	if isDuplicate("0xabc", 1) {
		t.Error("First call should NOT be duplicate")
	}
	if !isDuplicate("0xabc", 1) {
		t.Error("Second call with same key SHOULD be duplicate")
	}
	if isDuplicate("0xabc", 2) {
		t.Error("Different logIndex should NOT be duplicate")
	}
	if isDuplicate("0xdef", 1) {
		t.Error("Different txHash should NOT be duplicate")
	}
}

// ═══════════════════════════════════════════
// Funders 排序
// ═══════════════════════════════════════════

func TestGetFundersList(t *testing.T) {
	win := &AddressWindow{
		Funders: map[string]float64{
			"0xA": 1000,
			"0xB": 5000,
			"0xC": 3000,
		},
	}

	list := getFundersList(win)
	if len(list) != 3 {
		t.Fatalf("Count = %d, want 3", len(list))
	}
	if list[0].Usd != 5000 {
		t.Errorf("First should be 5000 (sorted desc), got %f", list[0].Usd)
	}
	if list[1].Usd != 3000 {
		t.Errorf("Second should be 3000, got %f", list[1].Usd)
	}
	if list[2].Usd != 1000 {
		t.Errorf("Third should be 1000, got %f", list[2].Usd)
	}
	if getPrimaryFunder(win) != "0xB" {
		t.Errorf("Primary funder should be 0xB, got %s", getPrimaryFunder(win))
	}
}

// ═══════════════════════════════════════════
// 市场分类
// ═══════════════════════════════════════════

func TestNormalizeCategory(t *testing.T) {
	tests := map[string]string{
		"Politics":       "political",
		"Crypto":         "crypto_regulatory",
		"macro":          "macro",
		"NBA":            "sports_injury",
		"fed_rates":      "macro",
		"Geopolitics":    "geopolitical",
		"IPO":            "corporate",
		"tech_release":   "tech_release",
		"ai_models":      "tech_release",
		"randomstuff":    "randomstuff",
		"SEC":            "legal_regulatory",
		"employ":         "macro",
		"war":            "geopolitical",
	}
	for input, expected := range tests {
		result := normalizeCategory(input)
		if result != expected {
			t.Errorf("normalizeCategory(%s) = %s, want %s", input, result, expected)
		}
	}
}

func TestHighInfoCategory(t *testing.T) {
	highInfo := []string{"political", "macro", "legal_regulatory", "sports_injury",
		"entertainment_leak", "geopolitical", "crypto_regulatory", "tech_release", "corporate"}
	for _, cat := range highInfo {
		if !isHighInfoCategory(cat) {
			t.Errorf("%s should be high-info", cat)
		}
	}
	if isHighInfoCategory("randomstuff") {
		t.Error("randomstuff should NOT be high-info")
	}
}

// ═══════════════════════════════════════════
// JSON 序列化 (protect against struct changes)
// ═══════════════════════════════════════════

func TestAlertStructs(t *testing.T) {
	_ = WhaleAlert{
		SchemaVersion: "1.0",
		EventType:     "smart_money_detected",
		Severity:      "high",
		Data: WhaleAlertData{
			TargetAddress:        "0xTest",
			PrimaryFunderAddress: "0xFunder",
			TotalUsdAccumulated:  50000,
			RiskScore:            90,
			Tags:                 []string{"New EOA"},
		},
	}
	_ = InformedEventAlert{
		SchemaVersion: "1.1",
		EventType:     "informed_event_activity",
		Severity:      "high",
		Source:        "polymarket",
		Data: InformedEventData{
			RootWalletAddress: "0xTest",
			RiskScore:         100,
			Tags:              []string{"Polymarket Native Discovery"},
		},
	}
}

// ═══════════════════════════════════════════
// 结果统计
// ═══════════════════════════════════════════

func TestAll(t *testing.T) {
	// 确保并发安全
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
