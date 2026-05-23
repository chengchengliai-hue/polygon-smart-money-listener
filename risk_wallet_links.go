package main

import (
	"log"
	"strings"
	"context"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	// Table A: Proxy → current Owner EOA (real-time, event-driven)
	proxyOwnerMap   = make(map[string]*ProxyOwner)
	proxyOwnerMapMu sync.RWMutex

	// Table B: Risk EOA Pool (from whale_alerts, with TTL)
	riskEoaPool   = make(map[string]*RiskWalletEntry)
	riskEoaPoolMu sync.RWMutex

	// Cache: all linked addresses → risk entries (multi-root)
	allLinkedAddresses   = make(map[string][]*RiskWalletEntry)
	allLinkedAddressesMu sync.RWMutex
)

// ═══════════════════════════════════════════
// Table A: Proxy → Owner mapping
// ═══════════════════════════════════════════

func addProxyOwner(proxy, owner string) {
	proxyOwnerMapMu.Lock()
	proxyOwnerMap[strings.ToLower(proxy)] = &ProxyOwner{
		ProxyAddress: strings.ToLower(proxy),
		OwnerEOA:     strings.ToLower(owner),
		LastUpdated:  time.Now().Unix(),
	}
	proxyOwnerMapMu.Unlock()
}

func getProxyOwner(proxy string) string {
	proxyOwnerMapMu.RLock()
	defer proxyOwnerMapMu.RUnlock()
	entry := proxyOwnerMap[strings.ToLower(proxy)]
	if entry == nil {
		return ""
	}
	return entry.OwnerEOA
}

// ═══════════════════════════════════════════
// Table B: Risk EOA Pool
// ═══════════════════════════════════════════

const riskTTL = 30 * 24 * 3600 // 30 days

func refreshRiskPool() {
	entries := loadWhaleAddresses()
	now := time.Now().Unix()

	riskEoaPoolMu.Lock()
	newPool := make(map[string]*RiskWalletEntry)
	newLinked := make(map[string][]*RiskWalletEntry)
	riskEoaPoolMu.Unlock()

	for _, entry := range entries {
		// Filter: skip whitelisted (CEX/DEX/Bridge/Mixer)
		if isWhitelisted(strings.ToLower(entry.RootAddresses[0])) {
			continue
		}

		// TTL check: skip expired
		if entry.LastActive > 0 && now-entry.LastActive > riskTTL {
			log.Printf("[wallet] expired: %s (inactive %d days)", entry.RootAddresses[0][:14], (now-entry.LastActive)/86400)
			continue
		}

		eoa := strings.ToLower(entry.RootAddresses[0])
		newPool[eoa] = &entry

		// Add EOA itself to linked map
		newLinked[eoa] = append(newLinked[eoa], &entry)

		// Add from whale_alerts funder/funded links (filtered by whitelist)
		func() {
			rows, err := db.Query(`SELECT DISTINCT primary_funder_address FROM whale_alerts WHERE address = ?`, eoa)
			if err != nil {
				return
			}
			defer rows.Close()
			for rows.Next() {
				var funder string
				rows.Scan(&funder)
				funder = strings.ToLower(funder)
				if funder != "" && funder != eoa && !isWhitelisted(funder) {
					newLinked[funder] = append(newLinked[funder], &entry)
				}
			}
		}()

		func() {
			rows, err := db.Query(`SELECT DISTINCT address FROM whale_alerts WHERE primary_funder_address = ?`, eoa)
			if err != nil {
				return
			}
			defer rows.Close()
			for rows.Next() {
				var funded string
				rows.Scan(&funded)
				funded = strings.ToLower(funded)
				if funded != "" && funded != eoa && !isWhitelisted(funded) {
					newLinked[funded] = append(newLinked[funded], &entry)
				}
			}
		}()

		// Discover Proxy addresses for this EOA (from local cache)
		proxyOwnerMapMu.RLock()
		for proxy, po := range proxyOwnerMap {
			if po.OwnerEOA == eoa {
				newLinked[proxy] = append(newLinked[proxy], &entry)
			}
		}
		proxyOwnerMapMu.RUnlock()
	}

	riskEoaPoolMu.Lock()
	riskEoaPool = newPool
	riskEoaPoolMu.Unlock()

	allLinkedAddressesMu.Lock()
	allLinkedAddresses = newLinked
	allLinkedAddressesMu.Unlock()

	log.Printf("[wallet] pool: %d risk EOAs, %d linked addresses (with whitelist+TTL)", len(newPool), len(newLinked))
}

// ═══════════════════════════════════════════
// Unified matching: Proxy→Owner→RiskPool
// ═══════════════════════════════════════════

func lookupRiskWallet(address string) []*RiskWalletEntry {
	addr := strings.ToLower(address)

	// Step 1: Check if this address is a Proxy → resolve to Owner EOA
	owner := getProxyOwner(addr)
	if owner != "" {
		addr = owner
	}

	// Step 2: Check all linked addresses (multi-root)
	allLinkedAddressesMu.RLock()
	defer allLinkedAddressesMu.RUnlock()

	if entries := allLinkedAddresses[addr]; len(entries) > 0 {
		return entries
	}

	// Step 3: Direct EOA pool check
	riskEoaPoolMu.RLock()
	defer riskEoaPoolMu.RUnlock()
	if entry := riskEoaPool[addr]; entry != nil {
		return []*RiskWalletEntry{entry}
	}

	return nil
}

func lookupRiskEoa(eoa string) bool {
	riskEoaPoolMu.RLock()
	defer riskEoaPoolMu.RUnlock()
	_, ok := riskEoaPool[strings.ToLower(eoa)]
	return ok
}

// ═══════════════════════════════════════════
// Lifecycle
// ═══════════════════════════════════════════

func startWalletLinkRefresher() {
	refreshRiskPool()
	go func() {
		ticker := time.NewTicker(time.Duration(informedConfig.WalletRefreshSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			refreshRiskPool()
		}
	}()

	// Proxy cleanup: refresh every 30 min (full scan of Proxy Factory for active EOAs)
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			refreshProxyOwnersFromChain()
		}
	}()
}

// refreshProxyOwnersFromChain queries Polymarket ProxyFactory for each risk EOA

// lazyResolveProxy attempts to find the owner of an unknown address in real-time
// Tries Gnosis Safe owner() and PolyProxy patterns, caches result in proxyOwnerMap
func lazyResolveProxy(addr string) string {
	addrLower := strings.ToLower(addr)
	// Already resolved during this session
	proxyOwnerMapMu.RLock()
	if po, ok := proxyOwnerMap[addrLower]; ok {
		proxyOwnerMapMu.RUnlock()
		return po.OwnerEOA
	}
	proxyOwnerMapMu.RUnlock()

	client, err := ethclient.Dial(config.HttpRpcUrl)
	if err != nil {
		return ""
	}
	defer client.Close()

	// Try Gnosis Safe owner(): selector = 0x8da5cb5b
	safeOwner := callOwner(client, addr)
	if safeOwner != "" && !isWhitelisted(safeOwner) {
		addProxyOwner(addr, safeOwner)
		return safeOwner
	}

	// Try PolyProxy admin/owner: common pattern
	// PolyProxy typically has owner() with same selector
	// Already tried above, if empty try admin(): 0xf851a440
	polyOwner := callAdmin(client, addr)
	if polyOwner != "" && !isWhitelisted(polyOwner) {
		addProxyOwner(addr, polyOwner)
		return polyOwner
	}

	return ""
}

// callOwner tries Gnosis Safe/PolyProxy owner() view function
func callOwner(client *ethclient.Client, proxyAddr string) string {
	// owner() → 0x8da5cb5b
	selector := common.FromHex("0x8da5cb5b")
	to := common.HexToAddress(proxyAddr)
	data := selector // owner() takes no arguments

	result, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil || len(result) < 32 {
		return ""
	}
	owner := common.BytesToAddress(result[12:])
	if owner.Hex() == "0x0000000000000000000000000000000000000000" {
		return ""
	}
	return strings.ToLower(owner.Hex())
}

// callAdmin tries admin() view function (some PolyProxy contracts)
func callAdmin(client *ethclient.Client, proxyAddr string) string {
	// admin() → 0xf851a440
	selector := common.FromHex("0xf851a440")
	to := common.HexToAddress(proxyAddr)

	result, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &to, Data: selector}, nil)
	if err != nil || len(result) < 32 {
		return ""
	}
	admin := common.BytesToAddress(result[12:])
	if admin.Hex() == "0x0000000000000000000000000000000000000000" {
		return ""
	}
	return strings.ToLower(admin.Hex())
}

func refreshProxyOwnersFromChain() {
	riskEoaPoolMu.RLock()
	eoas := make([]string, 0, len(riskEoaPool))
	for eoa := range riskEoaPool {
		eoas = append(eoas, eoa)
	}
	riskEoaPoolMu.RUnlock()

	count := 0
	for _, eoa := range eoas {
		// Call Polymarket ProxyFactory.getProxy(eoa) via RPC
		proxy := discoverProxyForEOA(eoa)
		if proxy != "" {
			addProxyOwner(proxy, eoa)
			count++
		}
	}
	if count > 0 {
		log.Printf("[proxy] refreshed %d EOA→Proxy mappings", count)
	}
}

// Polymarket ProxyFactory address (deploys user proxy wallets)
var polyProxyFactory = common.HexToAddress("0xaB45c5A4B0c941a2F231C04C3f49182e1A254052")

// ProxyFactory.getProxy(address) function signature
var getProxySelector = common.HexToHash("0x") // will be filled below

// discoverProxyForEOA queries Polymarket ProxyFactory.getProxy(eoa) via eth_call
func discoverProxyForEOA(eoa string) string {
	client, err := ethclient.Dial(config.HttpRpcUrl)
	if err != nil {
		return ""
	}
	defer client.Close()

	// getProxy(address) → 0x64ad1e65 (keccak256 first 4 bytes)
	selector := common.FromHex("0x64ad1e65")
	addr := common.HexToAddress(eoa)
	data := append(selector, common.LeftPadBytes(addr.Bytes(), 32)...)

	to := polyProxyFactory
	result, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil || len(result) < 32 {
		return ""
	}

	proxy := common.BytesToAddress(result[12:]) // address is right-aligned in 32 bytes
	if proxy.Hex() == "0x0000000000000000000000000000000000000000" {
		return ""
	}
	return strings.ToLower(proxy.Hex())
}
