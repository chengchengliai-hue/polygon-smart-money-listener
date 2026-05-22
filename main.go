package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	processedCount  int64
	alertCount      int64
	lastProcessedBlock uint64

	// Token addresses (lowercase)
	usdtAddr = common.HexToAddress("0xc2132D05D31c914a87C6611C10748AEb04B58e8F")
	usdcAddr     = common.HexToAddress("0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359")
	usdcEAddr    = common.HexToAddress("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174") // USDC.e bridged
	transferTopic = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
)

func main() {
	log.SetFlags(log.Ltime)
	log.Println("[boot] Polygon Smart Money Listener starting...")

	loadConfig()
	initDB()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("[shutdown] saving state...")
		saveRuntimeState("last_processed_block", fmt.Sprintf("%d", lastProcessedBlock))
		os.Exit(0)
	}()

	run()
}

func run() {
	backoff := 1 * time.Second
	for {
		err := startListener()
		if err != nil {
			log.Printf("[error] %v, reconnecting in %v...", err, backoff)
			saveRuntimeState("last_processed_block", fmt.Sprintf("%d", lastProcessedBlock))
			time.Sleep(backoff)
			// Exponential backoff: 1s → 2s → 4s → 8s → 16s → max 60s
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		} else {
			// Successful connection, reset backoff
			backoff = 1 * time.Second
		}
	}
}

func startListener() error {
	// Connect to WebSocket
	wsClient, err := ethclient.Dial(config.WsRpcUrl)
	if err != nil {
		return fmt.Errorf("ws connect: %w", err)
	}
	defer wsClient.Close()

	// Get current block
	header, err := wsClient.HeaderByNumber(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("get header: %w", err)
	}
	currentBlock := header.Number.Uint64()
	log.Printf("[init] connected, current block: %d", currentBlock)

	// Load last processed block
	saved := getRuntimeState("last_processed_block")
	if saved != "" {
		fmt.Sscanf(saved, "%d", &lastProcessedBlock)
	} else {
		lastProcessedBlock = currentBlock - uint64(config.ConfirmationBlocks)
		saveRuntimeState("last_processed_block", fmt.Sprintf("%d", lastProcessedBlock))
	}

	// Catch up if behind
	catchUpTo := currentBlock - uint64(config.ConfirmationBlocks)
	if lastProcessedBlock < catchUpTo {
		catchUpBlocks(lastProcessedBlock+1, catchUpTo)
	}

	// Subscribe to Transfer events
	query := ethereum.FilterQuery{
		Addresses: []common.Address{usdtAddr, usdcAddr, usdcEAddr},
		Topics:    [][]common.Hash{{transferTopic}},
	}

	logsCh := make(chan types.Log)
	sub, err := wsClient.SubscribeFilterLogs(context.Background(), query, logsCh)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	log.Println("[ws] subscribed to USDT/USDC Transfer events")

	// HTTP client for nonce/code checks
	httpClient, err := ethclient.Dial(config.HttpRpcUrl)
	if err != nil {
		log.Printf("[warn] HTTP RPC unavailable: %v", err)
	}

	// Periodic tasks
	gcTicker := time.NewTicker(60 * time.Second)
	windowTicker := time.NewTicker(30 * time.Second)
	statusTicker := time.NewTicker(300 * time.Second)
	gapTicker := time.NewTicker(120 * time.Second)
	defer gcTicker.Stop()
	defer windowTicker.Stop()
	defer statusTicker.Stop()
	defer gapTicker.Stop()

	for {
		select {
		case err := <-sub.Err():
			return fmt.Errorf("subscription error: %w", err)

		case vLog := <-logsCh:
			// Handle block reorganization: if removed=true, reverse the transfer
			if vLog.Removed {
				event := parseTransferLog(vLog)
				if event != nil {
					event.ValueUsd = tokenValueToUsd(httpClient, event.Token, event.Value)
					event.ValueUsd = -event.ValueUsd
					addTransfer(event)
					removeProcessedLog(vLog.TxHash.Hex(), vLog.Index)
				}
				continue
			}

			// Dedup: skip already-processed logs
			if isDuplicate(vLog.TxHash.Hex(), vLog.Index) {
				continue
			}

			event := parseTransferLog(vLog)
			if event == nil {
				continue
			}
			event.ValueUsd = tokenValueToUsd(httpClient, event.Token, event.Value)
			addTransfer(event)
			processedCount++

			if vLog.BlockNumber > lastProcessedBlock {
				lastProcessedBlock = vLog.BlockNumber
			}

			if processedCount%100 == 0 {
				saveRuntimeState("last_processed_block", fmt.Sprintf("%d", lastProcessedBlock))
			}
			if processedCount%50 == 0 {
				checkExceededWindows(httpClient)
			}

		case <-gcTicker.C:
			collectGarbage()

		case <-windowTicker.C:
			checkExceededWindows(httpClient)
			saveRuntimeState("last_processed_block", fmt.Sprintf("%d", lastProcessedBlock))

		case <-statusTicker.C:
			outputSummary(processedCount, alertCount)

		case <-gapTicker.C:
			if lastProcessedBlock < catchUpTo-10 {
				catchUpBlocks(lastProcessedBlock+1, catchUpTo)
			}
		}
	}
}

func catchUpBlocks(from, to uint64) {
	log.Printf("[catchup] fetching blocks %d → %d", from, to)
	httpClient, err := ethclient.Dial(config.HttpRpcUrl)
	if err != nil {
		log.Printf("[catchup] HTTP error: %v", err)
		return
	}
	defer httpClient.Close()

	chunkSize := uint64(10) // Alchemy free tier limit
	for start := from; start <= to; start += chunkSize {
		end := start + chunkSize - 1
		if end > to {
			end = to
		}
		logs, err := httpClient.FilterLogs(context.Background(), ethereum.FilterQuery{
			Addresses: []common.Address{usdtAddr, usdcAddr, usdcEAddr},
			Topics:    [][]common.Hash{{transferTopic}},
			FromBlock: new(big.Int).SetUint64(start),
			ToBlock:   new(big.Int).SetUint64(end),
		})
		if err != nil {
			log.Printf("[catchup] filter error %d-%d: %v", start, end, err)
			continue
		}
		for _, vLog := range logs {
			if vLog.Removed {
				continue
			}
			if isDuplicate(vLog.TxHash.Hex(), vLog.Index) {
				continue
			}
			event := parseTransferLog(vLog)
			if event != nil {
				event.ValueUsd = tokenValueToUsd(httpClient, event.Token, event.Value)
				addTransfer(event)
			}
		}
		checkExceededWindows(httpClient)
		if end < to {
			time.Sleep(200 * time.Millisecond)
		}
	}

	saveRuntimeState("last_processed_block", fmt.Sprintf("%d", to))
	lastProcessedBlock = to
}

func checkExceededWindows(httpClient *ethclient.Client) {
	exceeded := getExceededWindows()
	for _, win := range exceeded {
		nonce := getNonce(httpClient, win.Address)
		isContract := isContract(httpClient, win.Address)
		scored := scoreAddress(win, nonce, isContract)

		if scored.Score >= 60 {
			outputAlert(scored)
			alertCount++
		}
	}
}

func tokenValueToUsd(client *ethclient.Client, token string, value *big.Int) float64 {
	// USDT and USDC both have 6 decimals on Polygon
	decimals := 6
	divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	val := new(big.Float).SetInt(value)
	result, _ := new(big.Float).Quo(val, divisor).Float64()
	return result
}

func parseTransferLog(vLog types.Log) *TransferEvent {
	// Must be from USDT or USDC
	token := ""
	switch strings.ToLower(vLog.Address.Hex()) {
	case strings.ToLower(usdtAddr.Hex()):
		token = "USDT"
	case strings.ToLower(usdcAddr.Hex()):
		token = "USDC"
	case strings.ToLower(usdcEAddr.Hex()):
		token = "USDC"
	default:
		return nil
	}

	if len(vLog.Topics) != 3 {
		return nil
	}

	from := common.BytesToAddress(vLog.Topics[1].Bytes()).Hex()
	to := common.BytesToAddress(vLog.Topics[2].Bytes()).Hex()
	zeroAddr := "0x0000000000000000000000000000000000000000"
	// Skip zero address (mint/burn)
	if to == zeroAddr || from == zeroAddr {
		return nil
	}

	return &TransferEvent{
		TxHash:      vLog.TxHash.Hex(),
		BlockNumber: vLog.BlockNumber,
		From:        from,
		To:          to,
		Value:       new(big.Int).SetBytes(vLog.Data),
		Token:       token,
		Timestamp:   time.Now().Unix(),
	}
}

// checkFunderBalance queries current USDT+USDC balance of an address
// Used to verify a historical whale still has funds before granting bonus score
func checkFunderBalance(addr string) float64 {
	client, err := ethclient.Dial(config.HttpRpcUrl)
	if err != nil {
		return 0
	}
	defer client.Close()

	address := common.HexToAddress(addr)
	usdtContract := common.HexToAddress("0xc2132D05D31c914a87C6611C10748AEb04B58e8F")
	usdcContract := common.HexToAddress("0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359")
	balanceOf := common.HexToHash("0x70a0823100000000000000000000000000000000000000000000000000000000")

	// USDT
	usdtData := append(balanceOf.Bytes()[:4], common.LeftPadBytes(address.Bytes(), 32)...)
	usdtResult, _ := client.CallContract(context.Background(), ethereum.CallMsg{To: &usdtContract, Data: usdtData}, nil)
	usdtBal := new(big.Int).SetBytes(usdtResult)

	// USDC
	usdcData := append(balanceOf.Bytes()[:4], common.LeftPadBytes(address.Bytes(), 32)...)
	usdcResult, _ := client.CallContract(context.Background(), ethereum.CallMsg{To: &usdcContract, Data: usdcData}, nil)
	usdcBal := new(big.Int).SetBytes(usdcResult)

	divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil))
	usdtFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(usdtBal), divisor).Float64()
	usdcFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(usdcBal), divisor).Float64()

	return usdtFloat + usdcFloat
}

func getNonce(client *ethclient.Client, addr string) *int64 {
	if client == nil {
		return nil
	}
	address := common.HexToAddress(addr)
	nonce, err := client.NonceAt(context.Background(), address, nil)
	if err != nil {
		return nil
	}
	n := int64(nonce)
	return &n
}

func isContract(client *ethclient.Client, addr string) bool {
	if client == nil {
		return false
	}
	address := common.HexToAddress(addr)
	code, err := client.CodeAt(context.Background(), address, nil)
	if err != nil {
		return false
	}
	return len(code) > 0
}
