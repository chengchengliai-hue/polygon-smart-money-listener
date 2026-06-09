package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	gethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

var (
	clobAPIKey     string
	clobSecret     string
	clobPassPhrase string
	clobBaseURL    string

	proxyWallet common.Address
	signerKey   string // hex-encoded private key, without 0x
	liveTrading bool
)

func initClob() {
	clobAPIKey = os.Getenv("CLOB_API_KEY")
	clobSecret = os.Getenv("CLOB_SECRET")
	clobPassPhrase = os.Getenv("CLOB_PASS_PHRASE")
	clobBaseURL = getEnv("POLYMARKET_CLOB_BASE", "https://clob.polymarket.com")

	proxyStr := os.Getenv("POLYMARKET_PROXY")
	signerKey = os.Getenv("POLYMARKET_PRIVATE_KEY")

	// Always try to auto-derive fresh credentials from private key.
	// Falls back to env vars if derivation fails.
	if signerKey != "" && proxyStr != "" {
		log.Printf("[clob] auto-deriving API credentials from private key...")
		key, secret, passphrase, err := deriveClobCredentials(signerKey, proxyStr)
		if err != nil {
			log.Printf("[clob] credential derivation failed: %v, falling back to env vars", err)
		} else {
			clobAPIKey = key
			clobSecret = secret
			clobPassPhrase = passphrase
			log.Printf("[clob] credentials derived successfully")
		}
	}

	liveTrading = clobAPIKey != "" && clobSecret != "" && clobPassPhrase != "" &&
		signerKey != "" && proxyStr != ""

	if proxyStr != "" {
		proxyWallet = common.HexToAddress(proxyStr)
	}
	if liveTrading {
		log.Printf("[clob] live trading ENABLED proxy=%s", proxyWallet.Hex())
	} else {
		missing := []string{}
		if clobAPIKey == "" { missing = append(missing, "CLOB_API_KEY") }
		if clobSecret == "" { missing = append(missing, "CLOB_SECRET") }
		if clobPassPhrase == "" { missing = append(missing, "CLOB_PASS_PHRASE") }
		if signerKey == "" { missing = append(missing, "POLYMARKET_PRIVATE_KEY") }
		if proxyStr == "" { missing = append(missing, "POLYMARKET_PROXY") }
		log.Printf("[clob] live trading DISABLED — missing: %v", missing)
	}
}

// ── Order request/response types ──

type clobOrderRequest struct {
	Order     clobSignedOrder `json:"order"`
	Owner     string          `json:"owner"`
	OrderType string          `json:"orderType"` // FOK
}

type clobSignedOrder struct {
	Salt          string `json:"salt"`
	Maker         string `json:"maker"`
	Signer        string `json:"signer"`
	Taker         string `json:"taker"`
	TokenID       string `json:"tokenId"`
	MakerAmount   string `json:"makerAmount"`
	TakerAmount   string `json:"takerAmount"`
	Expiration    string `json:"expiration"`
	Nonce         string `json:"nonce"`
	FeeRateBps    string `json:"feeRateBps"`
	Side          string `json:"side"`
	SignatureType string `json:"signatureType"`
	Signature     string `json:"signature"`
}

type clobOrderResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Size      string `json:"size"`
	Price     string `json:"price"`
	Side      string `json:"side"`
	Outcome   string `json:"outcome"`

	// Polymarket CLOB uses various field names for fill data.
	// We try all known variants.
	MatchedSize    string `json:"matched_size"`
	SizeMatched    string `json:"size_matched"`
	TakingAmount   string `json:"takingAmount"`
	MakingAmount   string `json:"makingAmount"`
	TakingAmount2  string `json:"taking_amount"`
	MakingAmount2  string `json:"making_amount"`
	AvgPrice       string `json:"avg_price"`
	AvgPrice2      string `json:"averagePrice"`
	PriceAvg       string `json:"price_avg"`
	FilledSize     string `json:"filledSize"`
	FilledAmount   string `json:"filledAmount"`
}

type clobFillResult struct {
	OrderID      string
	Filled       bool
	FilledShares float64
	FilledPrice  float64
}

// ── Public API ──

// clobBuy places a FOK BUY order. limitPrice is the worst acceptable price per share.
func clobBuy(tokenID string, limitPrice float64, budgetUSDC float64, _ float64) clobFillResult {
	if !liveTrading {
		return clobFillResult{}
	}

	maker := proxyWallet.Hex()
	signerAddr := crypto.PubkeyToAddress(privateKey().PublicKey).Hex()

	// Round to nearest 0.001 (CLOB tick requirement), cap at max/min
	limitPrice = math.Ceil(limitPrice*1000) / 1000
	if limitPrice > 0.999 {
		limitPrice = 0.999
	}

	// BUY: makerAmount = USDC spent, takerAmount = minimum shares expected
	makerAmount := uint64(budgetUSDC * 1e6)
	takerAmount := uint64(budgetUSDC / limitPrice * 1e6)

	order := buildOrder(maker, signerAddr, tokenID, makerAmount, takerAmount, "BUY")

	reqBody := clobOrderRequest{Order: order, Owner: maker, OrderType: "FOK"}
	result := submitOrder(reqBody, "BUY")

	if result.Filled {
		log.Printf("[clob] BUY filled: order=%s token=%s price=%.4f shares=%.1f cost=$%.2f",
			result.OrderID, tokenID[:10], result.FilledPrice, result.FilledShares,
			result.FilledPrice*result.FilledShares)
	} else {
		log.Printf("[clob] BUY unfilled: token=%s limit=%.4f budget=$%.2f",
			tokenID[:10], limitPrice, budgetUSDC)
	}
	return result
}

// clobSell places a FOK SELL order at the given limit price.
func clobSell(tokenID string, limitPrice float64, shares float64) clobFillResult {
	if !liveTrading {
		return clobFillResult{}
	}

	maker := proxyWallet.Hex()
	signerAddr := crypto.PubkeyToAddress(privateKey().PublicKey).Hex()

	// Round to nearest 0.001 (CLOB tick requirement), cap at min
	limitPrice = math.Ceil(limitPrice*1000) / 1000
	if limitPrice < 0.001 {
		limitPrice = 0.001
	}

	// SELL: makerAmount = shares, takerAmount = USDC to receive
	makerAmount := uint64(shares * 1e6)
	takerAmount := uint64(shares * limitPrice * 1e6)

	order := buildOrder(maker, signerAddr, tokenID, makerAmount, takerAmount, "SELL")

	reqBody := clobOrderRequest{Order: order, Owner: maker, OrderType: "FOK"}
	result := submitOrder(reqBody, "SELL")

	if result.Filled {
		log.Printf("[clob] SELL filled: order=%s token=%s price=%.4f shares=%.1f proceeds=$%.2f",
			result.OrderID, tokenID[:10], result.FilledPrice, result.FilledShares,
			result.FilledPrice*result.FilledShares)
	} else {
		log.Printf("[clob] SELL unfilled: token=%s limit=%.4f shares=%.1f",
			tokenID[:10], limitPrice, shares)
	}
	return result
}

// fetchClobBestBid returns the highest bid price from the order book.
func fetchClobBestBid(tokenID string) (float64, bool) {
	book := fetchOrderBook(tokenID)
	if book == nil || len(book.Bids) == 0 {
		return 0, false
	}
	var price float64
	fmt.Sscanf(book.Bids[0].Price, "%f", &price)
	if price <= 0 {
		return 0, false
	}
	return price, true
}

// ── Order building & submission ──

func buildOrder(maker, signerAddr, tokenID string, makerAmount, takerAmount uint64, side string) clobSignedOrder {
	expiration := strconv.FormatInt(time.Now().Add(5*time.Minute).Unix(), 10)
	salt := strconv.FormatInt(time.Now().UnixMilli(), 10)

	// Polymarket CLOB: 0=EOA(maker==signer), 1=POLY_PROXY(maker!=signer)
	sigType := "0"
	if strings.EqualFold(maker, signerAddr) {
		sigType = "0"
	} else {
		sigType = "1"
	}

	return clobSignedOrder{
		Salt:          salt,
		Maker:         maker,
		Signer:        signerAddr,
		Taker:         "0x0000000000000000000000000000000000000000",
		TokenID:       tokenID,
		MakerAmount:   strconv.FormatUint(makerAmount, 10),
		TakerAmount:   strconv.FormatUint(takerAmount, 10),
		Expiration:    expiration,
		Nonce:         "0",
		FeeRateBps:    "0",
		Side:          side,
		SignatureType: sigType,
	}
}

func submitOrder(reqBody clobOrderRequest, side string) clobFillResult {
	sig, err := signClobOrder(reqBody.Order)
	if err != nil {
		log.Printf("[clob] sign error: %v", err)
		return clobFillResult{}
	}
	reqBody.Order.Signature = sig

	bodyBytes, _ := json.Marshal(reqBody)
	url := clobBaseURL + "/order"

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		req, _ := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		setClobAuth(req, bodyBytes)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[clob] order req error (attempt %d): %v", attempt+1, err)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return parseClobResponse(respBody, side)
		}
		if resp.StatusCode == 400 {
			// Client error — don't retry
			log.Printf("[clob] order rejected: status=%d body=%s",
				resp.StatusCode, safeTruncate(string(respBody), 300))
			return clobFillResult{}
		}
		log.Printf("[clob] order failed (attempt %d): status=%d body=%s",
			attempt+1, resp.StatusCode, safeTruncate(string(respBody), 200))
	}
	log.Printf("[clob] order failed after 3 attempts")
	return clobFillResult{}
}

// parseClobResponse parses the CLOB order response with side-aware field resolution.
func parseClobResponse(respBody []byte, side string) clobFillResult {
	var o clobOrderResponse
	if err := json.Unmarshal(respBody, &o); err != nil {
		log.Printf("[clob] parse error: %v body=%s", err, safeTruncate(string(respBody), 200))
		return clobFillResult{}
	}

	result := clobFillResult{OrderID: o.ID}

	// ── Status: FOK fills are typically MATCHED or CLOSED immediately ──
	status := strings.ToUpper(o.Status)
	// FOK: filled = MATCHED or FILLED. CLOSED_CANCELED means NOT filled.
	isFilled := status == "MATCHED" || status == "FILLED" || status == "MINED" || status == "CONFIRMED"

	// ── Side-aware fill parsing ──
	// makerAmount / takerAmount semantics differ by side.
	// We resolve outcome-token amount (→ shares) and USDC amount (→ cost/proceeds)
	// using the known CLOB conventions: maker=USDC,taker=shares for BUY; reverse for SELL.

	var usdcAmount, outcomeAmount float64
	var priceFromResp float64

	// Direct size fields
	sizeFields := []string{o.MatchedSize, o.SizeMatched, o.FilledSize, o.FilledAmount}
	for _, s := range sizeFields {
		if s != "" && s != "0" {
			fmt.Sscanf(s, "%f", &outcomeAmount)
			break
		}
	}

	// Amount-based fields: resolve which is USDC vs shares by side
	makingRaw := firstNonZero(o.MakingAmount, o.MakingAmount2)
	takingRaw := firstNonZero(o.TakingAmount, o.TakingAmount2, o.TakingAmount2)

	var making, taking float64
	fmt.Sscanf(makingRaw, "%f", &making)
	fmt.Sscanf(takingRaw, "%f", &taking)

	if side == "BUY" {
		// maker=USDC, taker=outcome shares
		usdcAmount = firstNonZeroFloat(making, 0)
		outcomeAmount = firstNonZeroFloat(outcomeAmount, taking)
	} else {
		// SELL: maker=outcome shares, taker=USDC
		outcomeAmount = firstNonZeroFloat(outcomeAmount, making)
		usdcAmount = firstNonZeroFloat(0, taking)
	}

	// Price: from response or derived
	priceFields := []string{o.AvgPrice, o.AvgPrice2, o.PriceAvg, o.Price}
	for _, s := range priceFields {
		if s != "" && s != "0" {
			fmt.Sscanf(s, "%f", &priceFromResp)
			break
		}
	}
	if priceFromResp <= 0 && usdcAmount > 0 && outcomeAmount > 0 {
		priceFromResp = usdcAmount / outcomeAmount
	}

	if outcomeAmount > 0 && priceFromResp > 0 {
		result.Filled = true
		result.FilledShares = outcomeAmount / 1e6
		result.FilledPrice = priceFromResp
	}
	if !result.Filled && isFilled && o.Size != "" {
		// Only trust fallback on explicit MATCHED/FILLED, not on ambiguous CLOSED states
		var sz float64
		fmt.Sscanf(o.Size, "%f", &sz)
		if sz > 0 {
			result.Filled = true
			result.FilledShares = sz / 1e6
			if o.Price != "" {
				fmt.Sscanf(o.Price, "%f", &result.FilledPrice)
			}
			log.Printf("[clob] using order size fallback: status=%s size=%.6f price=%s", status, sz, o.Price)
		}
	}

	return result
}

func firstNonZero(vals ...string) string {
	for _, v := range vals {
		if v != "" && v != "0" {
			return v
		}
	}
	return ""
}

func firstNonZeroFloat(a, b float64) float64 {
	if a > 0 {
		return a
	}
	return b
}

// ── EIP-712 signing ──

var clobDomain = apitypes.TypedDataDomain{
	Name:              "Polymarket CTF Exchange",
	Version:           "1",
	ChainId:           gethmath.NewHexOrDecimal256(137),
	VerifyingContract: "0xE111180000d2663C0091e4f400237545B87B996B",
}

var clobOrderTypes = apitypes.Types{
	"EIP712Domain": {
		{Name: "name", Type: "string"},
		{Name: "version", Type: "string"},
		{Name: "chainId", Type: "uint256"},
		{Name: "verifyingContract", Type: "address"},
	},
	"Order": {
		{Name: "salt", Type: "uint256"},
		{Name: "maker", Type: "address"},
		{Name: "signer", Type: "address"},
		{Name: "taker", Type: "address"},
		{Name: "tokenId", Type: "uint256"},
		{Name: "makerAmount", Type: "uint256"},
		{Name: "takerAmount", Type: "uint256"},
		{Name: "expiration", Type: "uint256"},
		{Name: "nonce", Type: "uint256"},
		{Name: "feeRateBps", Type: "uint256"},
		{Name: "side", Type: "uint8"},
		{Name: "signatureType", Type: "uint8"},
	},
}

func signClobOrder(order clobSignedOrder) (string, error) {
	// EIP-712: side is uint8 (0=BUY, 1=SELL)
	sideVal := 0
	if strings.EqualFold(order.Side, "SELL") {
		sideVal = 1
	}
	sigTypeVal, _ := strconv.Atoi(order.SignatureType)

	msg := apitypes.TypedDataMessage{
		"salt":          order.Salt,
		"maker":         order.Maker,
		"signer":        order.Signer,
		"taker":         order.Taker,
		"tokenId":       order.TokenID,
		"makerAmount":   order.MakerAmount,
		"takerAmount":   order.TakerAmount,
		"expiration":    order.Expiration,
		"nonce":         order.Nonce,
		"feeRateBps":    order.FeeRateBps,
		"side":          fmt.Sprintf("%d", sideVal),
		"signatureType": fmt.Sprintf("%d", sigTypeVal),
	}

	typedData := apitypes.TypedData{
		Types:       clobOrderTypes,
		PrimaryType: "Order",
		Domain:      clobDomain,
		Message:     msg,
	}

	hash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return "", fmt.Errorf("hash: %w", err)
	}

	key := privateKey()
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	sig[64] += 27
	return "0x" + common.Bytes2Hex(sig), nil
}

func privateKey() *ecdsa.PrivateKey {
	key, err := crypto.HexToECDSA(signerKey)
	if err != nil {
		log.Fatalf("[clob] invalid private key: %v", err)
	}
	return key
}

// ── API key derivation ──

var clobAuthDomain = apitypes.TypedDataDomain{
	Name:    "ClobAuthDomain",
	Version: "1",
	ChainId: gethmath.NewHexOrDecimal256(137),
}

var clobAuthTypes = apitypes.Types{
	"EIP712Domain": {
		{Name: "name", Type: "string"},
		{Name: "version", Type: "string"},
		{Name: "chainId", Type: "uint256"},
	},
	"ClobAuth": {
		{Name: "address", Type: "address"},
		{Name: "timestamp", Type: "string"},
		{Name: "nonce", Type: "uint256"},
		{Name: "message", Type: "string"},
	},
}

type clobCredsResponse struct {
	APIKey        string `json:"apiKey"`
	Secret        string `json:"secret"`
	Passphrase    string `json:"passphrase"`
	Error         string `json:"error"`
	AlreadyExists bool   `json:"alreadyExists"`
}

func deriveClobCredentials(pkHex string, proxyStr string) (string, string, string, error) {
	key, err := crypto.HexToECDSA(strings.TrimPrefix(pkHex, "0x"))
	if err != nil {
		return "", "", "", fmt.Errorf("parse pk: %w", err)
	}
	signerAddr := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())

	ts := fmt.Sprintf("%d", time.Now().Unix())
	msg := apitypes.TypedDataMessage{
		"address":   signerAddr,
		"timestamp": ts,
		"nonce":     big.NewInt(0),
		"message":   "This message attests that I control the given wallet",
	}
	typedData := apitypes.TypedData{
		Types:       clobAuthTypes,
		PrimaryType: "ClobAuth",
		Domain:      clobAuthDomain,
		Message:     msg,
	}
	hash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return "", "", "", fmt.Errorf("hash: %w", err)
	}
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		return "", "", "", fmt.Errorf("sign: %w", err)
	}
	sig[64] += 27
	signature := "0x" + common.Bytes2Hex(sig)

	// Try POST /auth/api-key first, fall back to GET /auth/derive-api-key
	creds, err := tryDeriveAPIKey(signerAddr, ts, signature)
	if err != nil {
		return "", "", "", err
	}

	log.Printf("[clob] derived credentials: key=%s...", creds.APIKey[:10])
	return creds.APIKey, creds.Secret, creds.Passphrase, nil
}

func tryDeriveAPIKey(signerAddr, ts, signature string) (*clobCredsResponse, error) {
	// First: POST /auth/api-key (create new)
	req, _ := http.NewRequest("POST", clobBaseURL+"/auth/api-key", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("POLY_ADDRESS", signerAddr)
	req.Header.Set("POLY_SIGNATURE", signature)
	req.Header.Set("POLY_TIMESTAMP", ts)
	req.Header.Set("POLY_NONCE", "0")

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[clob] POST /auth/api-key: %s", safeTruncate(string(respBody), 150))
		if resp.StatusCode == 200 {
			var creds clobCredsResponse
			if err := json.Unmarshal(respBody, &creds); err == nil && creds.APIKey != "" {
				return &creds, nil
			}
		}
	}

	// Fallback: GET /auth/derive-api-key (derive existing)
	req2, _ := http.NewRequest("GET", clobBaseURL+"/auth/derive-api-key", nil)
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("POLY_ADDRESS", signerAddr)
	req2.Header.Set("POLY_SIGNATURE", signature)
	req2.Header.Set("POLY_TIMESTAMP", ts)
	req2.Header.Set("POLY_NONCE", "0")

	resp2, err2 := http.DefaultClient.Do(req2)
	if err2 != nil {
		return nil, fmt.Errorf("derive-api-key: %w", err2)
	}
	defer resp2.Body.Close()
	respBody2, _ := io.ReadAll(resp2.Body)
	log.Printf("[clob] GET /auth/derive-api-key: %s", safeTruncate(string(respBody2), 150))

	if resp2.StatusCode != 200 {
		return nil, fmt.Errorf("status %d: %s", resp2.StatusCode, safeTruncate(string(respBody2), 200))
	}

	var creds clobCredsResponse
	if err := json.Unmarshal(respBody2, &creds); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if creds.APIKey == "" {
		return nil, fmt.Errorf("empty apiKey in response: %s", string(respBody2))
	}
	return &creds, nil
}

// ── HMAC auth ──

func setClobAuth(req *http.Request, body []byte) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	method := req.Method
	path := req.URL.Path

	message := timestamp + method + path + string(body)

	decodedSecret, err := base64.URLEncoding.DecodeString(clobSecret)
	if err != nil {
		decodedSecret = []byte(clobSecret)
	}
	mac := hmac.New(sha256.New, decodedSecret)
	mac.Write([]byte(message))
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	req.Header.Set("POLY_ADDRESS", proxyWallet.Hex())
	req.Header.Set("POLY_API_KEY", clobAPIKey)
	req.Header.Set("POLY_SIGNATURE", sig)
	req.Header.Set("POLY_TIMESTAMP", timestamp)
	req.Header.Set("POLY_PASSPHRASE", clobPassPhrase)
}
