# Polygon Smart Money Listener — 系统状态文档
## 最后更新: 2026-05-26

---

## 一、部署信息

```
服务器:  阿里云韩国 2核2G Ubuntu (43.108.37.104)
二进制:  /opt/listener/polygon-listener
源码:    /opt/listener-src/
配置:    /opt/listener/.env
日志:    /opt/listener/alerts.log (stdout)
DB:      /opt/listener/polygon_smart_money_watch.db (SQLite WAL)
服务:    systemctl status/restart polygon-listener
部署:    scp → go build -o /opt/listener/polygon-listener . → systemctl restart
```

### 关键环境变量 (.env)
```
POLYGON_WS_RPC_URL=wss://polygon-mainnet.g.alchemy.com/v2/YOUR_KEY
POLYGON_WS_RPC_URL_FALLBACK=wss://polygon.drpc.org
POLYGON_HTTP_RPC_URL=https://polygon-mainnet.g.alchemy.com/v2/YOUR_KEY
POLYGON_HTTP_RPC_URL_FALLBACK=https://polygon.drpc.org
BALANCE_THRESHOLD_USD=10000
INFORMED_MIN_TRADE_USDC=2000
INFORMED_ALERT_THRESHOLD=70
INFORMED_HIGH_ALERT_THRESHOLD=90
POLYMARKET_DATA_API_KEY=your_polymarket_data_api_key
TG_BOT_TOKEN=your_telegram_bot_token
TG_CHAT_ID=your_telegram_chat_id
SQLITE_PATH=/opt/listener/polygon_smart_money_watch.db
```

---

## 二、系统架构 (双轨)

### 轨道1: 巨鲸监控 (EOM)
```
drpc WS → USDT/USDC Transfer 事件 (Polygon链上)
  → 15min滑窗聚合 → 8条评分 → whale_alerts表
  → 每60s refreshRiskPool: ≥70分入池 + funder/funded展开
  → 497+ EOA, 1014+ 关联地址
  → 白名单过滤CEX/DEX/桥/Poly官方合约
  → 30天TTL
  → 产出: smart_money_detected (2546条累计)
```

### 轨道2: 聪明钱匹配 (Data API)
```
Data API /trades → limit=1000, 30s轮询, cache-busting _t
  → hash去重 (polymarket_events_seen表)
  → BUY过滤 → ≥$2K过滤
  → 路径A: EOM风险池匹配 → 8条评分 → 报警
  → 路径B: Native Discovery → 新钱包+高信息+≥$2K → 报警
  → 产出: informed_event_activity (268条累计)
```

### 轨道3: 吸筹检测 (crypto短线)
```
Data API每笔BUY → isCryptoShortTerm → ≥$10
  → hasPosition(立即查仓位,5min窗口)
  → isMarketMakerForSlug(SELL>20%毙)
  → 累加≥3笔 → 4维评分(时间窗口+小额+不追高+价格背离)
  → ≥30分 → 报警 (Telegram已接)
  → 产出: accumulation_detected (0条, 未触发过)
```

### 辅助: Binance价格
```
每30s: GET /api/v3/ticker/price (BTC/ETH/SOL/XRP)
  → 存内存, 吸筹维度4使用
```

---

## 三、评分规则

### 聪明钱 EOM (8条)
| 标签 | 分数 | 条件 |
|------|------|------|
| 匹配交易 | +40 | 风险池命中 |
| 高信息市场 | +20 | political/crypto/geopolitical/legal/corporate/tech/entertainment |
| 大额定向 | +20 | 单笔≥$2K 或 5min实体累计≥$2K |
| 方向明确 | +10 | bullish/bearish可判定 |
| 对冲套利 | -50 | 5min同条件YES+NO双向 |
| 近期巨鲸拨款 | +25 | 巨鲸报警<2h |
| 关联资金 | +10 | 巨鲸报警2-24h |
| 临期入场 | +25 | 距结束<2h |
| 临近结算 | +15 | 距结束2-24h |
| 混币器打款 | +50 | funder∈TornadoCash |

阈值: ≥90 high, 70-89 normal, <70 skip

### 吸筹 (4维)
| 维度 | 分数 | 条件 |
|------|------|------|
| 时间窗口 | +35/25/15/5/0 | >60/30-60/10-30/5-10/<5min |
| 小额分散 | +15 | maxSingle/totalUsd < 0.5 |
| 不追高 | +15 | std(prices)/avg < 0.4 |
| 价格背离 | +20 | Binance价可用 |

阈值: ≥30 报警

---

## 四、关键API和数据流

### Data API (Builder Key: 019e5ceb-...)
- `GET /trades?limit=1000` — 全局成交 (30s轮询, 有30-40s缓存)
- `GET /activity?user=X` — 钱包交易历史
- `GET /positions?user=X` — 钱包仓位 (5min盘仅在结算前30s可查)
- `GET /closed-positions?user=X` — 已平仓

### CLOB API (公开)
- `GET /book?token_id=X` → market hash
- `GET /markets/{hash}` → question, end_date_iso, tags
- `GET /simplified-markets` → 1000条/页

### Binance API (公开)
- `GET /api/v3/ticker/price?symbol=BTCUSDT` → 现货价

---

## 五、Server端检查命令

```bash
# 服务状态
ssh root@43.108.37.104 systemctl status polygon-listener

# 实时日志
ssh root@43.108.37.104 tail -50 /opt/listener/alerts.log

# 聪明钱预警
ssh root@43.108.37.104 grep -c informed_event_activity /opt/listener/alerts.log

# 吸筹日志
ssh root@43.108.37.104 grep 'accum.*checked' /opt/listener/alerts.log | tail -5

# 管道速率
ssh root@43.108.37.104 grep 'raw=\|events=' /opt/listener/alerts.log | tail -3

# 风险池大小
ssh root@43.108.37.104 grep 'pool:' /opt/listener/alerts.log | tail -1

# 重启服务
ssh root@43.108.37.104 systemctl restart polygon-listener

# 部署代码
cd ~/Desktop/bot/polygon-listener
go build -o /dev/null . && \
scp *.go root@43.108.37.104:/opt/listener-src/ && \
ssh root@43.108.37.104 "cd /opt/listener-src && go build -o /opt/listener/polygon-listener . && systemctl restart polygon-listener"
```

---

## 六、待解决问题

1. **timestamp污染** — 已修复，改用纯hash去重
2. **API缓存** — Data API 30-40s缓存，30s轮询已最大化覆盖
3. **limit上限** — API最多返回1000笔/次
4. **吸筹0触发** — `hasPosition`可用但钱包极少买≥3次同一crypto短线
5. **聪明钱0产出** — 市场≥$2K BUY极少（千笔2笔）
6. **频繁重启** — 每次部署清零所有内存计数器
7. **排行榜抓取** — Playwright可抓70个地址，RSC限制无法突破
8. **做市商评分** — 已开发但未接入系统

---

## 七、DB表结构 (SQLite)

```
whale_alerts — 巨鲸预警 (address, funder, total_usd, score, severity)
seen_addresses — 已见地址
polymarket_events_seen — hash去重 (5600条)
informed_event_alerts — 聪明钱预警 (268条)
informed_markets — 市场缓存
runtime_state — 持久状态
proxy_owners — 空表 (已废弃)
```

---

## 八、文件职责

```
main.go (341行)           — 入口 + 巨鲸WS监听 + 补块
polymarket_listener.go    — Data API poller + 聪明钱匹配 + 原生发现
accumulation_detector.go  — 吸筹检测 + hasPosition + 做市商过滤
maker_scorer.go           — 做市商评分 (独立模块,未接入)
risk_wallet_links.go      — 风险池 (allLinkedAddresses + riskEoaPool)
informed_event_scorer.go  — 8条评分规则
informed_event_alerter.go — JSON输出 + Telegram推送
informed_event_types.go   — 全类型定义
informed_event_config.go  — 配置加载
informed_event_db.go      — DB操作
informed_markets.go       — CLOB市场缓存 + token解析
hedge_detector.go         — YES/NO对冲检测
config.go                 — RPC配置 + 白名单 + 黑名单
db.go                     — DB初始化 + 基础操作
types.go                  — 巨鲸类型
window.go                 — 15min滑窗聚合
scorer.go                 — 巨鲸评分
alerter.go                — 巨鲸输出
```
