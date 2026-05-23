package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// OrderFilled event signature
var orderFilledTopic = common.HexToHash("0xa4f26b01428124668d5c13a09683562cb7d240e974ebc4c81b73093431b74be0")

// OrdersMatched event signature (from Neg Risk exchange)
var ordersMatchedTopic = common.HexToHash("0x9c0d3a22c1777c9b304099b2d225ccf7a3c4ef3d26ad6404acf71e2382fefec7")

var polymarketLastBlock uint64

func startPolymarketListener() {
	connectPolymarket := func() {
		backoff := 1 * time.Second
		for {
			err := runPolymarketListener()
			if err != nil {
				log.Printf("[polymarket] error: %v, reconnecting in %v...", err, backoff)
				time.Sleep(backoff)
				backoff *= 2
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			} else {
				backoff = 1 * time.Second
			}
		}
	}
	go connectPolymarket()
}

func runPolymarketListener() error {
	wsClient, err := ethclient.Dial("wss://1rpc.io/matic")
	if err != nil {
		return fmt.Errorf("polymarket ws: %w", err)
	}
	defer wsClient.Close()

	httpClient, err := ethclient.Dial("https://1rpc.io/matic")
	if err != nil {
		return fmt.Errorf("polymarket http: %w", err)
	}
	defer httpClient.Close()

	// Get current block (with nil check - 1RPC may return nil on error)
	polymarketLastBlock = 0
	header, err := wsClient.HeaderByNumber(context.Background(), nil)
	if err == nil && header != nil {
		polymarketLastBlock = header.Number.Uint64()
	}
	if polymarketLastBlock == 0 {
		polymarketLastBlock, _ = httpClient.BlockNumber(context.Background())
	}

	ctfAddr := common.HexToAddress(informedConfig.CtfExchange)
	negRiskAddr := common.HexToAddress(informedConfig.NegRiskExchange)

	query := ethereum.FilterQuery{
		Addresses: []common.Address{ctfAddr, negRiskAddr},
		Topics:    [][]common.Hash{{orderFilledTopic, ordersMatchedTopic}},
	}

	logsCh := make(chan types.Log)
	sub, err := wsClient.SubscribeFilterLogs(context.Background(), query, logsCh)
	if err != nil {
		return fmt.Errorf("polymarket subscribe: %w", err)
	}
	log.Printf("[polymarket] subscribed to Exchange events")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-sub.Err():
			return fmt.Errorf("polymarket sub error: %w", err)

		case vLog := <-logsCh:
			if vLog.Removed {
				continue
			}

			eventKey := fmt.Sprintf("%s_%d", vLog.TxHash.Hex(), vLog.Index)
			if isPolymarketEventSeen(eventKey) {
				continue
			}
			markPolymarketEventSeen(eventKey, vLog.TxHash.Hex(), vLog.Index, vLog.BlockNumber)

			trade := decodeTrade(vLog)
			if trade == nil {
				continue
			}

			// Match maker/funder against risk address set
			matched := matchTrade(trade)
			if matched == nil {
				continue
			}

			// Score and alert
			scored := scoreInformedEvent(matched)
			if scored.RiskScore >= informedConfig.AlertThreshold {
				outputInformedAlert(scored)
			}

		case <-ticker.C:
			polymarketLastBlock, _ = httpClient.BlockNumber(context.Background())
		}
	}
}

// matchTrade checks if maker or taker is in the risk address set
func matchTrade(trade *DecodedTrade) *MatchedTrade {
	// Check funder/taker wallet against risk pool
	// makerAssetId tells what the maker PAID → the maker RECEIVED takerAssetId
	// takerAssetId tells what the taker PAID → the taker RECEIVED makerAssetId

	// Check maker
	if entry := lookupRiskWallet(trade.Maker); entry != nil {
		lw := findMatchedWallet(entry, trade.Maker)
		return &MatchedTrade{
			DecodedTrade:      *trade,
			MatchedWallet:     lw.Address,
			MatchedWalletType: lw.Type,
			MatchedRole:       "maker",
			RootAddress:       entry.RootAddress,
			TokenOutcome:      lookupTokenOutcome(trade.TakerAssetID),
			Action:            "BUY",
			Direction:         determineDirection(trade.TakerAssetID, "BUY"),
		}
	}

	// Check taker
	if entry := lookupRiskWallet(trade.Taker); entry != nil {
		lw := findMatchedWallet(entry, trade.Taker)
		return &MatchedTrade{
			DecodedTrade:      *trade,
			MatchedWallet:     lw.Address,
			MatchedWalletType: lw.Type,
			MatchedRole:       "taker",
			RootAddress:       entry.RootAddress,
			TokenOutcome:      lookupTokenOutcome(trade.MakerAssetID),
			Action:            "BUY",
			Direction:         determineDirection(trade.MakerAssetID, "BUY"),
		}
	}

	return nil
}

func findMatchedWallet(entry *RiskWalletEntry, addr string) LinkedWallet {
	lower := strings.ToLower(addr)
	for _, lw := range entry.LinkedWallets {
		if strings.ToLower(lw.Address) == lower {
			return lw
		}
	}
	return LinkedWallet{Address: addr, Type: WalletEOA}
}

func determineDirection(tokenID, action string) string {
	outcome := lookupTokenOutcome(tokenID)
	if outcome == nil {
		return "unknown"
	}
	if outcome.Outcome == "YES" && action == "BUY" {
		return "bullish_yes"
	}
	if outcome.Outcome == "NO" && action == "BUY" {
		return "bearish_yes"
	}
	return "unknown"
}

