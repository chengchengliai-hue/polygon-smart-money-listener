package main

import (
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// CTF Exchange OrderFilled ABI
var orderFilledABI = mustParseABI(`[{
	"anonymous": false,
	"inputs": [
		{"indexed": true, "name": "orderHash", "type": "bytes32"},
		{"indexed": true, "name": "maker", "type": "address"},
		{"indexed": false, "name": "taker", "type": "address"},
		{"indexed": false, "name": "makerAssetId", "type": "uint256"},
		{"indexed": false, "name": "takerAssetId", "type": "uint256"},
		{"indexed": false, "name": "makerAmountFilled", "type": "uint256"},
		{"indexed": false, "name": "takerAmountFilled", "type": "uint256"},
		{"indexed": false, "name": "fee", "type": "uint256"}
	],
	"name": "OrderFilled",
	"type": "event"
}]`)

// Neg Risk Exchange OrdersMatched ABI
var ordersMatchedABI = mustParseABI(`[{
	"anonymous": false,
	"inputs": [
		{"indexed": true, "name": "orderHash", "type": "bytes32"},
		{"indexed": true, "name": "maker", "type": "address"},
		{"indexed": false, "name": "taker", "type": "address"},
		{"indexed": false, "name": "makerAssetId", "type": "uint256"},
		{"indexed": false, "name": "takerAssetId", "type": "uint256"},
		{"indexed": false, "name": "makerAmountFilled", "type": "uint256"},
		{"indexed": false, "name": "takerAmountFilled", "type": "uint256"},
		{"indexed": false, "name": "takerAmountPaid", "type": "uint256"},
		{"indexed": false, "name": "fee", "type": "uint256"}
	],
	"name": "OrdersMatched",
	"type": "event"
}]`)

// OrderFilled event signature
var orderFilledTopic = common.HexToHash("0xa4f26b01428124668d5c13a09683562cb7d240e974ebc4c81b73093431b74be0")

// OrdersMatched event signature (from Neg Risk exchange)
var ordersMatchedTopic = common.HexToHash("0x9c0d3a22c1777c9b304099b2d225ccf7a3c4ef3d26ad6404acf71e2382fefec7")

func mustParseABI(jsonABI string) abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(jsonABI))
	if err != nil {
		panic(err)
	}
	return parsed
}

func decodeTrade(vLog types.Log) *DecodedTrade {
	// Try OrderFilled
	if vLog.Topics[0] == orderFilledTopic {
		return decodeOrderFilled(vLog)
	}
	// Try OrdersMatched
	if vLog.Topics[0] == ordersMatchedTopic {
		return decodeOrdersMatched(vLog)
	}
	return nil
}

func decodeOrderFilled(vLog types.Log) *DecodedTrade {
	event, err := orderFilledABI.Unpack("OrderFilled", vLog.Data)
	if err != nil || len(event) < 7 {
		return nil
	}

	taker := event[0].(common.Address)
	makerAssetId := event[1].(*big.Int)
	takerAssetId := event[2].(*big.Int)
	makerAmt := event[3].(*big.Int)
	takerAmt := event[4].(*big.Int)
	fee := event[5].(*big.Int)
	_ = fee
	_ = event[6]

	return &DecodedTrade{
		TxHash:       vLog.TxHash.Hex(),
		LogIndex:     vLog.Index,
		BlockNumber:  vLog.BlockNumber,
		OrderHash:    common.BytesToHash(vLog.Topics[1].Bytes()).Hex(),
		Maker:        common.BytesToAddress(vLog.Topics[2].Bytes()).Hex(),
		Taker:        taker.Hex(),
		MakerAssetID: makerAssetId.String(),
		TakerAssetID: takerAssetId.String(),
		MakerAmount:  tokenAmountToFloat(makerAmt),
		TakerAmount:  tokenAmountToFloat(takerAmt),
	}
}

func decodeOrdersMatched(vLog types.Log) *DecodedTrade {
	event, err := ordersMatchedABI.Unpack("OrdersMatched", vLog.Data)
	if err != nil || len(event) < 7 {
		return nil
	}

	taker := event[0].(common.Address)
	makerAssetId := event[1].(*big.Int)
	takerAssetId := event[2].(*big.Int)
	makerAmt := event[3].(*big.Int)
	takerAmt := event[4].(*big.Int)
	fee := event[6].(*big.Int)
	_ = fee

	return &DecodedTrade{
		TxHash:       vLog.TxHash.Hex(),
		LogIndex:     vLog.Index,
		BlockNumber:  vLog.BlockNumber,
		OrderHash:    common.BytesToHash(vLog.Topics[1].Bytes()).Hex(),
		Maker:        common.BytesToAddress(vLog.Topics[2].Bytes()).Hex(),
		Taker:        taker.Hex(),
		MakerAssetID: makerAssetId.String(),
		TakerAssetID: takerAssetId.String(),
		MakerAmount:  tokenAmountToFloat(makerAmt),
		TakerAmount:  tokenAmountToFloat(takerAmt),
	}
}

// Polymarket CTF tokens use 6 decimals (like USDC)
func tokenAmountToFloat(val *big.Int) float64 {
	divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil))
	result, _ := new(big.Float).Quo(new(big.Float).SetInt(val), divisor).Float64()
	return result
}

