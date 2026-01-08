# max-man

> **High-Performance HFT Risk & Matching Engine** > 纯 Go 语言打造的高频、低延迟、生产级数字货币交易所核心系统。

## 📖 项目简介
**max-man** 是一个为了挑战极致性能而生的交易所后端核心。它包含风控引擎、撮合引擎以及链上衍生品同步器。
项目目标是对标头部交易所（Binance/Deribit）的性能标准，核心路径追求 **零内存分配 (Zero Allocation)** 和 **纳秒级 (ns)** 延迟。

---

## 🚀 核心技术标准
在本项目开发中，严格遵守以下 HFT 工程标准：
- **Zero Allocation**: 核心热路径（Hot Path）严禁 `new`/`make`，必须使用对象池或栈内存。
- **Lock-Free/Low-Lock**: 尽量使用 `atomic` 或无锁数据结构，减少 `Mutex` 竞争。
- **Memory Alignment**: 结构体字段按内存对齐排列，提高 CPU Cache 命中率。
- **Profile Driven**: 每一行关键代码都必须经过 `Benchmark` 和 `pprof` (CPU/Mem) 验证。

---

## 📅 开发路线图 (Roadmap)

### 第一阶段：基础搭建 (已完成)
- [x] **Day 1: 项目骨架与基础模型**
    - [x] 定义统一的 `Account`, `Position`, `RiskInput/Output` 结构。
    - [x] 实现基础的线性风控计算流。
- [x] **Day 2: 期权核心算法**
    - [x] Black-Scholes 定价模型实现。
    - [x] Greeks (Delta/Gamma/Vega/Theta) 计算。
- [x] **Day 3: 隐含波动率与情景分析**
    - [x] IV 反推算法 (Newton-Raphson)。
    - [x] 基础 PnL 情景模拟。

### 第二阶段：永续合约与高性能风控 (当前阶段)
- [ ] **Day 4: 永续合约核心 (Perp Core)**
    - [ ] 资金费率 (Funding Rate) 与 未实现盈亏 (uPnL) 核心算法。
    - [ ] **优化**: 结构体内存对齐优化。
    - [ ] **优化**: 核心计算函数的 Zero Allocation 实现。
    - [ ] **测试**: 编写 Benchmark 证明纳秒级性能。
- [ ] **Day 5: 实时数据流与并发架构 (The Loop)**
    - [ ] 模拟高频行情生成器 (Ticker)。
    - [ ] 实现无锁/低锁的行情分发系统 (Fan-out pattern)。
    - [ ] 解决 Channel 缓冲区阻塞问题。
- [ ] **Day 6: 高性能预警系统 (Alerting)**
    - [ ] Redis ZSet 实现价格区间订阅。
    - [ ] 解决“惊群效应”：使用 Pagination 分批拉取。
    - [ ] Lua 脚本封装原子操作。
- [ ] **Day 7: 强平引擎 (Liquidation Engine)**
    - [ ] **算法**: 反向索引推导强平价格 (Liq Price)。
    - [ ] **数据结构**: 内存 B-Tree / SkipList 维护强平线。
    - [ ] 实现毫秒级强平触发（非轮询模式）。
- [ ] **Day 8: 阶段性压测与 pprof 优化**
    - [ ] 全链路集成测试 (Mock Market -> Engine -> Alert/Liq)。
    - [ ] 压力测试 (10w 用户, 5000 TPS)。
    - [ ] 深度 pprof 分析 (CPU/Heap/Block/Trace) 并消除瓶颈。

### 第三阶段：撮合引擎与链上实战
- [ ] **Day 9: 撮合引擎 (Matching Engine)**
    - [ ] 实现 LOB (Limit Order Book) 订单簿。
    - [ ] 核心数据结构：红黑树 (Red-Black Tree) 或 跳表。
    - [ ] 实现 Maker/Taker 撮合逻辑。
    - [ ] 挑战：撮合路径零 GC。
- [ ] **Day 10: 组合保证金与交割合约**
    - [ ] 实现交割日期限结构。
    - [ ] 实现 Portfolio Margin (跨币种/跨期对冲减免)。
    - [ ] 矩阵相关性计算优化 (SIMD/Assembly)。
- [ ] **Day 11: 链上数据同步 (On-Chain Indexer)**
    - [ ] Geth 客户端集成与 WebSocket 监听。
    - [ ] 处理区块重组 (Reorg) 与回滚。
    - [ ] 高速 Log 解析 (Ring Buffer + ABI Cache)。
- [ ] **Day 12: 链上永续模型 (vAMM)**
    - [ ] 模拟 vAMM (x*y=k) 定价模型。
    - [ ] 预言机 (Oracle) 价格清洗与防抢跑设计。
- [ ] **Day 13: 灾难工程 (Chaos Engineering)**
    - [ ] 引入 Chaos Mesh 模拟网络抖动、Redis 宕机。
    - [ ] 实现熔断 (Circuit Breaker) 与优雅降级机制。
- [ ] **Day 14: 终极验收**
    - [ ] 模拟 "312" 极端行情压测。
    - [ ] 产出最终性能报告 (TPS, Latency P99)。

---

## 🛠 技术栈
- **Language**: Go (Golang) 1.21+
- **Data Structure**: Redis (ZSet for Indexing), B-Tree (Memory OrderBook)
- **Blockchain**: Geth (go-ethereum)
- **Monitoring**: Prometheus, Grafana, pprof
- **Architecture**: Microservices (Simulation), Event-Driven

## ⚡ 性能基准 (Target)
- **Risk Calculation**: < 500 ns/op
- **Matching Latency**: < 10 µs/op
- **System Throughput**: > 50,000 TPS (Single Core)
- **GC Pressure**: Zero allocs/op in hot loop

---

## 📂 目录结构
```text
max-man/
├── cmd/                # 启动入口 (Engine, Simulation, Matching)
├── pkg/                # 公共库 (Logger, Math)
├── internal/
│   ├── risk/           # 风控核心 (BS, Perp, Margin)
│   ├── matching/       # 撮合核心 (OrderBook)
│   ├── market/         # 行情分发
│   ├── liquidation/    # 强平逻辑
│   └── onchain/        # 链上同步
├── tests/              # 集成测试与 Benchmark
└── README.md

---

## 🔥 强平引擎 (Liquidation Engine) 迭代计划

### 已完成 ✅
- [x] 风险等级分层 (Safe/Warning/Danger/Critical/Liquidate)
- [x] CowMap 无锁读索引
- [x] 分片并行扫描 (200K 用户 ~90ms)
- [x] Worker Pool 强平执行
- [x] 价格触发机制 (Level 3 实时检查)
- [x] 完整单元测试 + Benchmark

### Phase 1: 基础生产化
- [ ] **PositionCache**: 内存缓存 + Kafka 事件驱动同步
- [ ] **LiquidationExecutor**: 对接撮合引擎发送强平单
- [ ] **分布式锁**: Redis/etcd 防止多节点重复强平
- [ ] **审计日志**: 记录所有强平操作
- [ ] **幂等性保证**: 防止重复强平

### Phase 2: 资金安全
- [ ] **保险基金模块**: 穿仓时兜底用户损失
- [ ] **ADL 自动减仓**: 保险基金不足时盈利用户分摊
- [ ] **部分强平**: 先平一部分仓位，可能回到安全区
- [ ] **强平订单优先级**: 强平单优先成交

### Phase 3: 可观测性
- [ ] **Prometheus 指标**: 强平次数、延迟、队列长度
- [ ] **Grafana 仪表盘**: 实时监控面板
- [ ] **告警规则**: 异常强平量告警
- [ ] **熔断机制**: 极端行情保护

### 性能基准 (已验证)
| 指标 | 数值 |
|------|------|
| 全量扫描 200K 用户 | ~90ms |
| 吞吐量 | ~220 万用户/秒 |
| 每用户计算耗时 | ~0.45μs |
| 高风险用户索引 | 无锁读 O(1) |