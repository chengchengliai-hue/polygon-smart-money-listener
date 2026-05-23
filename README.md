# Polygon Smart Money & Polymarket Informed Event Listener

Go 编写的 Polygon 链上智能风控系统，双模块并行运行：

- **巨鲸监听**：实时监控 USDT/USDC 大额转账，识别新钱包、资金跳转、拆单聚合
- **聪明钱追踪**：将巨鲸地址与 Polymarket 交易关联，识别高信息差内幕下注

基于 WebSocket 事件驱动，15 分钟滑动窗口聚合，8+7 条评分规则，SQLite 持久化，Telegram 实时推送。

## 架构总览

```
Polygon WebSocket (Infura, 2 WS: 巨鲸+Polymarket)
    │
    ├── USDT/USDC Transfer 事件 ──→ 巨鲸监听模块
    │        │
    │        ├── parseTransferLog: 过滤非官方合约/零地址/重组
    │        ├── addTransfer: 15分钟滑窗聚合 TotalUsd + Funders
    │        ├── checkExceededWindows: ≥$10K触发
    │        ├── scoreAddress: 8条规则打分
    │        └── outputAlert: JSON→stdout + whale_alerts表
    │
    └── OrderFilled/OrdersMatched ──→ Polymarket 监听模块
             │
             ├── decodeTrade: ABI解码 maker/taker/assetId/amount
             ├── matchTrade: 双层架构匹配
             │      ├── 表A: ProxyOwnerMap (Proxy→Owner EOA)
             │      └── 表B: riskEoaPool (whale_alerts ≥70)
             ├── scoreInformedEvent: 7条规则+实体聚合+对冲检测
             └── outputInformedAlert: JSON+informed_event_alerts+Telegram

共享模块:
  ├── risk_wallet_links: 风险钱包池(白名单过滤/多根/30天TTL)
  ├── informed_markets: Gamma API 市场缓存(2796 token)
  ├── Telegram推送: 聪明钱报警实时推送
  └── SQLite 9张表
```

## 文件结构

```
polygon-listener/
├── main.go                    # 入口 + USDT/USDC监听主循环
├── window.go                  # 15分钟滑动窗口 + 去重缓存
├── scorer.go                  # 巨鲸8条评分规则
├── alerter.go                 # 巨鲸JSON输出 + 持久化
├── types.go                   # 巨鲸数据类型
├── config.go                  # 配置 + 白名单(CEX/DEX/Bridge) + 混币器黑名单
├── db.go                      # SQLite: whale_alerts/seen/runtime/labels
│
├── polymarket_listener.go     # Polymarket WS监听 + 匹配引擎
├── polymarket_decoder.go      # OrderFilled/OrdersMatched ABI解码
├── risk_wallet_links.go       # 双层架构: ProxyOwnerMap + riskEoaPool
├── informed_markets.go        # Gamma API + 链上CTF token映射
├── informed_event_scorer.go   # 聪明钱7条评分 + 实体5分钟聚合
├── informed_event_alerter.go  # 聪明钱JSON输出 + Telegram推送
├── hedge_detector.go          # YES/NO对冲检测(-50)
├── informed_event_types.go    # 聪明钱数据类型
├── informed_event_config.go   # Polymarket配置 + HTTP代理
└── informed_event_db.go       # SQLite: 5张聪明钱表
```

## 巨鲸评分规则

| # | 条件 | 分值 | 标签 |
|---|------|------|------|
| 1 | 零地址 | 跳过 | Burn Address |
| 2 | 合约地址 | 跳过 | Contract |
| 3 | ≥$10,000 | +60 | |
| 4 | Nonce≤1 | +10 | Fresh Wallet |
| 5 | 首次见 | +10 | New EOA |
| 5b| 已见过 | -15 | Known Address |
| 6 | 历史巨鲸funder(余额>$10K) | +20 | Fund Hopping |
| 7a| CEX→新钱包(Nonce≤1) | 不扣 | Whale Onboarding |
| 7b| CEX→老钱包(Nonce>50) | -20 | CEX Withdrawal |
| 8 | ≥3笔+≥2funder | +5 | Split Accumulation |
| 9 | 7天内报过 | -10 | Previously Alerted |

≥90 high | 70-89 normal | 60-69 watch | <60 skip

## 聪明钱评分规则

| # | 条件 | 分值 | 标签 |
|---|------|------|------|
| 1 | 混币器入金(Tornado) | +50 | Mixer Funded |
| 2 | 风险钱包匹配Polymarket | +40 | Polymarket Trade |
| 3 | Proxy/Safe确认 | +10 | Proxy Wallet Match |
| 4 | 高信息差市场 | +20 | High Info Market |
| 5 | 实体聚合(5min)≥$5K | +20 | Large Buy / Split Order |
| 6 | 方向明确 | +10 | |
| 7 | YES/NO对冲 | -50 | Hedged/Arbitrage |

≥90 high | 70-89 normal | 60-69 watch

## 配置

```env
# RPC (Infura)
POLYGON_WS_RPC_URL=wss://polygon-mainnet.infura.io/ws/v3/YOUR_KEY
POLYGON_HTTP_RPC_URL=https://polygon-mainnet.infura.io/v3/YOUR_KEY

# 阈值
BALANCE_THRESHOLD_USD=10000
WINDOW_SECONDS=900
CONFIRMATION_BLOCKS=16

# Polymarket
POLYMARKET_GAMMA_BASE=https://gamma-api.polymarket.com
POLYMARKET_CTF_EXCHANGE=0xE111180000d2663C0091e4f400237545B87B996B
POLYMARKET_NEG_RISK_EXCHANGE=0xe2222d279d744050d28e00520010520000310F59
INFORMED_ALERT_THRESHOLD=70
INFORMED_MIN_TRADE_USDC=5000
HEDGE_PENALTY=-50

# Telegram (Telegram)
TG_BOT_TOKEN=your_bot_token
TG_CHAT_ID=your_chat_id
```

## 部署

```bash
# 本地编译
go build -o polygon-listener .

# 服务器
scp polygon-listener .env root@server:/opt/listener/
ssh root@server
chmod +x /opt/listener/polygon-listener
nohup /opt/listener/polygon-listener > /opt/listener/alerts.log 2>&1 &

# systemd 保活
cp polygon-listener.service /etc/systemd/system/
systemctl enable --now polygon-listener
```

## JSON 报警格式

### 巨鲸报警 (`smart_money_detected`)
```json
{
  "schema_version": "1.0",
  "event_type": "smart_money_detected",
  "severity": "high",
  "chain": "polygon",
  "data": {
    "target_address": "0x...",
    "primary_funder_address": "0x...",
    "funders": [{"address": "0x...", "usd": 50000}],
    "total_usd_accumulated": 50000,
    "tx_count_in_window": 1,
    "risk_score": 90,
    "tags": ["New EOA", "Fund Hopping"],
    "detected_at": "2026-05-23T12:00:00Z"
  }
}
```

### 聪明钱报警 (`informed_event_activity`)
```json
{
  "schema_version": "1.1",
  "event_type": "informed_event_activity",
  "severity": "high",
  "source": "polymarket",
  "data": {
    "root_wallet_address": "0x...",
    "matched_wallet_address": "0x...",
    "matched_wallet_type": "GNOSIS_SAFE",
    "event_category": "macro",
    "market_question": "Will the Fed cut rates in June?",
    "outcome": "YES",
    "direction": "bullish_yes",
    "estimated_usdc": 15000,
    "risk_score": 100,
    "tags": ["Mixer Funded", "Proxy Wallet Match"],
    "detected_at": "2026-05-23T12:00:00Z"
  }
}
```

## 数据表

| 表 | 用途 |
|---|------|
| `whale_alerts` | 巨鲸报警记录 |
| `seen_addresses` | 已观察地址防重复 |
| `runtime_state` | 运行状态(last_block) |
| `address_labels` | 地址标签(CEX/Proxy) |
| `risk_wallet_links` | EOA/Proxy关联映射 |
| `informed_markets` | token→YES/NO缓存(2796条) |
| `polymarket_events_seen` | 事件去重 |
| `wallet_condition_activity` | 对冲检测历史 |
| `informed_event_alerts` | 聪明钱报警记录 |

## 风险钱包双层架构

```
表A: proxyOwnerMap
  监听 ProxyCreation 事件 → 实时更新 Proxy→Owner 映射
  30分钟全量刷新兜底

表B: riskEoaPool
  来源: whale_alerts ≥70分
  过滤: 白名单(CEX/DEX/桥)排除
  展开: funder/funded关联 + Proxy映射
  TTL: 30天无活动自动移除

匹配: OrderFilled(Proxy_X)
  → 表A: Proxy_X → Owner EOA_Y
  → 表B: EOA_Y是否在风险池
  → 多根支持: 一个地址关联多个根
```

## Stack

- Go 1.24
- go-ethereum (WebSocket + HTTP RPC)
- SQLite (better-sqlite3 / CGO)
- Polymarket Gamma API
- Telegram Bot API
