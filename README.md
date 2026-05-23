# Polygon Smart Money Listener

Go 编写的 Polygon 链上智能风控系统，双监听器并行运行，共享风险池。基于 WebSocket 事件驱动，15 分钟滑动窗口聚合，10 步评分规则，SQLite WAL 模式持久化，Telegram 实时推送。

## 一、系统概览

```
                   Infura WebSocket (Polygon)
                            │
         ┌──────────────────┴──────────────────┐
         │                                     │
    监听器 A: 巨鲸                        监听器 B: 聪明钱
    事件: Transfer                        事件: OrderFilled / OrdersMatched
    合约: USDT / USDC / USDC.e             合约: CTF Exchange + Neg Risk Exchange
    ┌───────────────────┐                 ┌───────────────────────┐
    │ 滑窗聚合 15min     │                 │ 风险池匹配              │
    │ ≥ $10K 触发评分    │                 │ Proxy→Owner 解析       │
    │ 8条规则打分        │                 │ 10步评分               │
    │ 写入 whale_alerts  │                 │ Telegram 推送          │
    └────────┬──────────┘                 └───────────┬───────────┘
             │                                       │
             └──────── 风险池 (riskEoaPool) ←────────┘
                      30天 TTL / 白名单过滤
                      双层架构:
                        表A proxyOwnerMap: Proxy → Owner EOA (7天GC)
                        表B riskEoaPool:   巨鲸 ≥70分 + 关联地址
                        反向索引 allLinkedAddresses
```

## 二、监听器 A：巨鲸

### 数据源
Infura WS 订阅 USDT/USDC/USDC.e 的 Transfer 事件。补块机制 `catchUpBlocks()` 用 HTTP RPC 补齐遗漏区块。

### 事件过滤 `main.go:parseTransferLog`

```
Transfer 事件
  ├─ 过滤: 只处理 3 个代币合约
  ├─ 跳过: from=0x0（铸币）或 to=0x0（销毁）
  ├─ 金额: 6位小数 → USD
  └─ 去重: isDuplicate(txHash_logIndex)，15分钟TTL，内联GC（>5000条清理过期）
```

### 滑窗聚合 `window.go`

```
key = 收款地址 (to)

AddressWindow {
    Transfers []           转账记录列表
    TotalUsd  float64      累计 USD
    Funders   map          每个转出地址贡献的金额
    TxCount   int          转账笔数
    FirstSeen/LastSeen int64
}

每次 addTransfer():
  1. 按 15 分钟窗口剔除过期记录
  2. 重新计算 TotalUsd、Funders、TxCount
  3. 加入新记录
```

### 触发条件

每 50 条事件或每 30 秒: `getExceededWindows()` → TotalUsd ≥ $10K 弹出 → `getNonce` + `isContract` → `scoreAddress()` → score ≥ 60 写入 `whale_alerts`。

### 巨鲸评分 `scorer.go:scoreAddress`（8 条规则）

| # | 规则 | 分数 | 说明 |
|---|---|---|---|
| 1 | 零地址 (0x0000...) | skip | Burn address |
| 2 | 合约地址 | 0 | 记录但标记 Contract |
| 3 | TotalUsd ≥ $10K | +60 | 基础阈值 |
| 4 | nonce ≤ 1 | +10 | 链上新钱包（资金刚到，尚未操作） |
| 5 | 首次见到 | +10 | 不在 seen_addresses 中 |
| 6 | 已见过 | -15 | 历史地址 |
| 7 | funder 是已知巨鲸 | +20 | funder 余额仍 > $10K |
| 8 | funder 是 CEX 热钱包 | -20 | 可能是正常提币 |
| 9 | 拆单累积 | +5 | ≥3笔 且 ≥2个不同来源 |
| 10 | 7天内已告警 | -10 | 去重惩罚 |

```
严重级别: ≥90 high | ≥70 normal | ≥60 watch | <60 skip
```

### 输出

JSON 打印到 stdout + 写入 `whale_alerts` 表。不推 Telegram。

## 三、风险池管理 `risk_wallet_links.go`

### 双层架构

```
┌─────────────────────────────────────────────────────┐
│ 表A: proxyOwnerMap (内存 + 持久化)                    │
│   Proxy地址 → Owner EOA                              │
│   来源:                                                │
│     - ProxyFactory.getProxy(eoa) 主动扫描 (30分钟)      │
│     - lazyResolveProxy() 实时 owner()/admin() 调用     │
│   GC: 每小时清理 7 天未更新条目                        │
├─────────────────────────────────────────────────────┤
│ 表B: riskEoaPool (内存)                              │
│   来源: whale_alerts 表 (score ≥ 70)                  │
│   过滤: 跳过白名单 (CEX/DEX/桥)                        │
│   展开:                                                │
│     - EOA 自身                                          │
│     - funder/funded 关联地址 (来自 whale_alerts)        │
│     - 对应 Proxy 地址 (来自表A)                         │
│   TTL: 30天过期自动移除                                 │
├─────────────────────────────────────────────────────┤
│ 反向索引: allLinkedAddresses                          │
│   任意地址 → []*RiskWalletEntry (多个根)               │
└─────────────────────────────────────────────────────┘
```

### 匹配逻辑 `lookupRiskWallet(address)`

```
输入: 交易地址 (maker 或 taker)

Step 1: 查表A proxyOwnerMap
  address → Proxy? → 解析为 Owner EOA

Step 2: 查反向索引 allLinkedAddresses
  EOA/Proxy → []*RiskWalletEntry

Step 3: 直接查表B riskEoaPool

返回: nil（未命中）或 []*RiskWalletEntry（命中）
```

### 懒加载解析 `lazyResolveProxy(addr)`

```
lookupRiskWallet 未命中时触发：
  1. 尝试 Gnosis Safe owner() → selector 0x8da5cb5b
  2. 尝试 PolyProxy admin()   → selector 0xf851a440
  3. 命中的缓存到 proxyOwnerMap
```

### 白名单与黑名单 `config.go`

```
CEX 热钱包: Binance, Bybit, OKX, Gate.io, Crypto.com, Coinbase
DEX/桥: Uniswap, 1inch, Paraswap, Polygon Bridge
混币器黑名单: Tornado Cash (4个地址)
```

## 四、监听器 B：聪明钱

### 数据源

Infura WS 订阅 CTF Exchange + Neg Risk Exchange 的 OrderFilled/OrdersMatched 事件。

### ABI 解码 `polymarket_decoder.go`

```
OrderFilled 字段:
  maker, taker, makerAssetId, takerAssetId, makerAmountFilled, takerAmountFilled, fee

OrdersMatched (Neg Risk) 字段:
  同上 + takerAmountPaid

金额: tokenAmountToFloat → 6位小数 → USD
```

### 主循环 `polymarket_listener.go`

```
对每笔事件:
  ├─ 去重: isPolymarketEventSeen(txHash_logIndex)
  ├─ ABI 解码: decodeTrade(vLog)
  ├─ 匹配: matchTrade(trade)
  │   ├─ 命中 → scoreInformedEvent(matched) → score ≥ 70 推 Telegram
  │   └─ 未命中 → scoreNativeDiscovery(trade) → 条件满足推 Telegram
  └─ 记录: saveWalletConditionActivity（用于对冲检测）
```

### 交易匹配 `matchTrade(trade)`

```
检查 maker:
  lookupRiskWallet(maker)
    → miss → lazyResolveProxy(maker) → 重试
    → 命中 → MatchedTrade{RootAddress, MatchedWallet, Direction, ...}

检查 taker: 同上

Direction 判定:
  YES + BUY → "bullish_yes"
  NO  + BUY → "bearish_yes"
```

## 五、聪明钱评分 `informed_event_scorer.go`

### 5.1 风险池路径 `scoreInformedEvent`（10 步评分）

| 步 | 规则 | 分数 | 说明 |
|---|---|---|---|
| 1 | 混币器资金 | +50 | 查 whale_alerts 中 root 的 funder 是否在 Tornado Cash 名单 |
| 2 | 风险池匹配 | +40 | 基础分 |
| 3 | Proxy 确认 | +10 | POLY_PROXY / GNOSIS_SAFE / DEPOSIT_WALLET |
| 4 | 高信息市场 | +20 | 政治/宏观/法律/公司/地缘等类别 |
| 5 | 大单/实体聚合 | +20 | 单笔 ≥ $5K 或 5分钟内 root 聚合 ≥ $5K |
| 6 | 方向明确 | +10 | bullish_yes / bearish_yes |
| 7 | 对冲检测 | -50 | 同 condition 同时买 YES+NO，金额比 0.6~1.6 |
| 8 | 时间关联资金 | +25/+10 | 距上次巨鲸告警 < 2h / < 24h |
| 9 | 决议前下注 | +25/+15 | 距市场结束 < 2h / < 24h |
| 10 | 判定级别 | — | |

```
严重级别: ≥90 high | ≥70 normal | <70 watch（不告警）
```

### 5.2 原生发现 `scoreNativeDiscovery`（不依赖风险池）

```
三个条件全部满足:
  ① nonce ≤ 3（为 approve+deposit+trade 消耗预留余量）
  ② 高信息类别市场
  ③ 金额 ≥ $5K

→ 固定 70 分，"Polymarket Native Discovery"
→ ≥ 90 → high，≥ 70 → normal
```

### 5.3 辅助函数

```
hoursSinceLastWhaleAlert(addr) → 查询 whale_alerts 最近记录
  < 2h  → +25 "Time-Correlated Funding"
  < 24h → +10 "Recent Funding"
  -1    → 无记录，跳过

hoursUntilEnd(endDate) → 查询市场结束时间（来自 Gamma API 的 end_date）
  < 2h  → +25 "Imminent Resolution Entry"
  < 24h → +15 "Pre-Resolution Timing"
  -1    → 已结束或无法解析，跳过
```

### 5.4 对冲检测 `hedge_detector.go`

```
查 wallet_condition_activity:
  同一 root + 同一 condition_id 的历史记录
  ├─ 不存在 → 保存当前记录，返回 false
  ├─ 存在相反 outcome（YES vs NO）
  │   ├─ 两边金额都 ≥ $5K
  │   └─ 金额比 0.6~1.6
  │       → 返回 true（对冲/套利）
  └─ 无对冲 → 保存当前记录，返回 false
```

## 六、数据基础设施

### 6.1 SQLite WAL 模式 + 写入锁

```
DSN: sqlite3 + ?_journal_mode=WAL&_busy_timeout=5000
  WAL: 读不阻塞写，写不阻塞读
  busy_timeout: 并发写入等待5秒而非直接报错

dbWriteMu (sync.Mutex): 兜底保护所有写入函数
  保护: saveWhaleAlert, markAddressSeen, saveRuntimeState,
        saveRiskWalletLink, markPolymarketEventSeen,
        saveWalletConditionActivity, saveInformedAlert, saveInformedMarket
```

### 6.2 数据库表

| 表名 | 监听器 | 用途 |
|---|---|---|
| `seen_addresses` | 巨鲸 | 地址首次出现追踪 |
| `whale_alerts` | 巨鲸 | 巨鲸告警（address, score, tags, alerted_at） |
| `runtime_state` | 巨鲸 | last_processed_block 等状态 |
| `address_labels` | 共享 | 地址自定义标签 |
| `risk_wallet_links` | 聪明钱 | Proxy→Owner 持久化 |
| `informed_markets` | 聪明钱 | Token 缓存（category, end_date, liquidity, volume） |
| `polymarket_events_seen` | 聪明钱 | OrderFilled 去重 |
| `wallet_condition_activity` | 聪明钱 | 交易历史（对冲检测用） |
| `informed_event_alerts` | 聪明钱 | 告警记录 |

### 6.3 市场数据 `informed_markets.go`

```
Gamma API (gamma-api.polymarket.com/events?limit=200&closed=false):
  → 获取市场元数据: 类别标签、结束时间(endDate)、流动性、成交量
  → 通过 HTTP_PROXY 代理（韩国直连不了时使用）
  → 仅缓存高信息类别市场
  → 每 300 秒刷新

链上 fallback:
  → 从风险池地址的近期 OrderFilled 中解析未知 token ID
  → conditionId + outcomeIndex → 构造基本 TokenOutcome

normalizeCategory() 映射:
  Gamma标签 → 标准化类别:
    geopolitical, political, macro, legal_regulatory,
    corporate, sports_injury, entertainment_leak,
    crypto_regulatory, tech_release, market_resolution
  全部为高信息类别
```

### 6.4 内存管理

```
proxyOwnerMap:
  GC: 每小时清理 7 天未更新条目 (LastUpdated < now - 7d)

riskEoaPool:
  TTL: 30天（refreshRiskPool 时检查 LastActive）

rootTradeWindows:
  GC: 每次 scoreInformedEvent 末尾清理 5 分钟未活动窗口

processedLogs:
  GC: >5000 条时清理 15 分钟过期条目
```

## 七、告警输出

### 巨鲸告警 `alerter.go`
JSON 打印到 stdout + 写入 whale_alerts 表。不推 Telegram。

### 聪明钱告警 `informed_event_alerter.go`
JSON 打印到 stdout + 写入 informed_event_alerts 表 + Telegram 推送（唯一推送通道）。

Telegram 格式（中文）:

```
🔴 聪明钱报警 — 高危

市场：Will Trump win 2024?
分类：political
下注金额：$20,000 USDC
方向：买入YES（看多）
交易角色：挂单成交

根钱包（巨鲸）：0xabc...
匹配钱包（代理）：0xdef...
钱包类型：Polymarket 代理合约

来源：历史巨鲸池命中
风险评分：85 分
标签：Polymarket Trade、High Information Market、Time-Correlated Funding
交易哈希：0x...
区块号：12345678
发现时间：2026-05-23 20:05:00（北京时间）

[💡 聪明钱报警] [🔗 Polygonscan]
```

## 八、后台任务

| goroutine | 间隔 | 功能 |
|---|---|---|
| runPolymarketListener | 持续 | 聪明钱 WS 监听 |
| whale run() | 持续 | 巨鲸 WS 监听 |
| gcTicker | 60s | 巨鲸滑窗 GC |
| windowTicker | 30s | 巨鲸窗口检查 |
| statusTicker | 300s | 巨鲸状态打印 |
| gapTicker | 120s | 补块检查 |
| refreshRiskPool | 60s | 风险池刷新 |
| refreshProxyOwnersFromChain | 30min | Proxy 主动扫描 |
| proxyOwnerMap GC | 1h | Proxy 内存清理 |
| refreshMarketsFromChain | 300s | Gamma API 刷新 |

## 九、配置

```env
# RPC (Infura)
POLYGON_WS_RPC_URL=wss://polygon-mainnet.infura.io/ws/v3/YOUR_KEY
POLYGON_HTTP_RPC_URL=https://polygon-mainnet.infura.io/v3/YOUR_KEY

# 巨鲸阈值
BALANCE_THRESHOLD_USD=10000
WINDOW_SECONDS=900
CONFIRMATION_BLOCKS=16
SQLITE_PATH=polygon_smart_money_watch.db

# Polymarket
POLYMARKET_GAMMA_BASE=https://gamma-api.polymarket.com
POLYMARKET_CTF_EXCHANGE=0xE111180000d2663C0091e4f400237545B87B996B
POLYMARKET_NEG_RISK_EXCHANGE=0xe2222d279d744050d28e00520010520000310F59
INFORMED_ALERT_THRESHOLD=70
INFORMED_HIGH_ALERT_THRESHOLD=90
INFORMED_MIN_TRADE_USDC=5000
INFORMED_WINDOW_SECONDS=900
HEDGE_PENALTY=-50

# 代理
HTTP_PROXY=http://your-proxy:port

# Telegram
TG_BOT_TOKEN=your_bot_token
TG_CHAT_ID=your_chat_id
```

## 十、技术栈

- Go 1.24
- go-ethereum (WebSocket + HTTP RPC)
- SQLite (mattn/go-sqlite3, WAL 模式)
- Polymarket Gamma API
- Polymarket CLOB API
- Telegram Bot API
- systemd (进程守护)
