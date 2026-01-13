# 交易页面实现需求分析

基于提供的界面截图（Gate.io 风格），我们需要实现一个高性能、实时的合约交易界面。以下是详细的功能模块、数据需求和 API 设计分析。

## 1. 页面布局与组件拆解

页面主要分为 5 个核心区域：

### A. 顶部行情栏 (Ticker Header)
- **展示内容**:
  - 交易对 (Symbol): BTC/USDT
  - 最新价 (Last Price) & 涨跌幅 (24h Change)
  - 标记价格 (Mark Price) & 指数价格 (Index Price)
  - 资金费率 (Funding Rate) & 倒计时 (Countdown)
  - 24h 高/低价 (High/Low)
  - 24h 成交量 (Volume)

### B. K线图表区 (Chart Area)
- **功能**:
  - 展示价格走势 (Candlestick)
  - 时间周期切换 (1m, 15m, 1h, 4h, 1d)
  - 技术指标 (MA, VOL, MACD 等)
  - **实现方案**: 通常集成 TradingView Charting Library。

### C. 盘口与成交 (Order Book & Trades)
- **订单薄 (Order Book)**:
  - 卖盘 (Asks): 红色，价格从低到高
  - 买盘 (Bids): 绿色，价格从高到低
  - 中间显示最新价及价差
  - 深度图 (Depth Chart)
- **最新成交 (Recent Trades)**:
  - 实时滚动的成交记录 (价格, 数量, 时间, 方向)

### D. 下单交易区 (Order Entry Panel)
- **功能**:
  - 仓位模式: 全仓/逐仓, 杠杆倍数调节
  - 订单类型: 限价 (Limit), 市价 (Market), 条件单 (Stop)
  - 输入: 价格, 数量 (BTC/USDT), 比例滑竿 (0-100%)
  - 资产显示: 可用余额 (Available Balance)
  - 操作按钮: 开多 (Open Long), 开空 (Open Short)
  - 选项: 只减仓 (Reduce Only), Post-Only (被动委托)

### E. 底部资产与订单管理 (Bottom Panel)
- **Tabs**:
  - 当前持仓 (Positions): 显示未实现盈亏, 强平价, 保证金等
  - 当前委托 (Open Orders): 未成交订单, 可撤单
  - 历史委托 (Order History)
  - 交易历史 (Trade History)

---

## 2. 后端 API 与 WebSocket 需求

为了支持上述界面，后端需要提供 HTTP API (用于快照和操作) 和 WebSocket (用于实时推送)。

### 2.1 WebSocket 实时推送 (Public)

| 频道 (Channel) | 频率 | 描述 | 数据内容 |
| :--- | :--- | :--- | :--- |
| `ticker.BTCUSDT` | 100ms | 顶部行情更新 | 最新价, 涨跌幅, 24h高低, 成交量 |
| `depth.BTCUSDT` | 100ms | 盘口深度更新 | 增量更新 (Update) 或 快照 (Snapshot) |
| `trade.BTCUSDT` | 实时 | 最新成交推送 | 价格, 数量, 方向, 时间 |
| `kline.BTCUSDT.1m` | 实时 | K线柱更新 | Open, High, Low, Close, Volume |
| `markPrice.BTCUSDT` | 1s | 标记/指数价格 | 标记价格, 指数价格, 资金费率, 下次资金费时间 |

### 2.2 WebSocket 实时推送 (Private/User)

| 频道 (Channel) | 描述 | 数据内容 |
| :--- | :--- | :--- |
| `order` | 订单状态变更 | 订单ID, 状态(New/Filled/Canceled), 成交量, 剩余量 |
| `position` | 持仓变更 | 仓位大小, 均价, 未实现盈亏, 强平价, 保证金 |
| `balance` | 余额变更 | 钱包余额, 可用余额, 冻结金额 |

### 2.3 HTTP API 接口

#### 市场数据 (Market Data)
- `GET /api/v1/market/ticker?symbol=BTCUSDT` - 获取 24h 聚合行情
- `GET /api/v1/market/depth?symbol=BTCUSDT&limit=50` - 获取初始盘口快照
- `GET /api/v1/market/kline?symbol=BTCUSDT&interval=1h` - 获取 K 线历史数据
- `GET /api/v1/market/trades?symbol=BTCUSDT` - 获取近期成交历史

#### 交易接口 (Trade)
- `POST /api/v1/order` - 下单
  - 参数: `symbol`, `side` (buy/sell), `type` (limit/market), `price`, `quantity`, `leverage`
- `DELETE /api/v1/order/{order_id}` - 撤单
- `DELETE /api/v1/orders` - 撤销全部订单

#### 账户接口 (Account)
- `GET /api/v1/account/balance` - 获取账户余额
- `GET /api/v1/account/positions` - 获取当前持仓
- `GET /api/v1/account/orders` - 获取当前委托

---

## 3. 核心难点与实现建议

### 3.1 盘口 (Order Book) 性能优化
- **问题**: 高频交易下，盘口数据量大且更新极快，DOM 频繁更新会导致页面卡顿。
- **方案**:
  - 使用 **WebSocket 增量更新** 维护本地 OrderBook 状态，而不是每次全量刷新。
  - 前端使用 **虚拟列表 (Virtual List)** 技术，只渲染可见区域的 DOM。
  - 限制渲染频率 (Throttle)，例如每 100ms 渲染一次，而不是每收到一个包就渲染。

### 3.2 K线图 (Chart)
- **方案**: 推荐使用 **TradingView Charting Library** (标准版需申请，轻量版 Lightweight Charts 开源可用)。
- **数据**: 需要实现 Datafeed 接口，对接后端的 K 线 API 和 WebSocket。

### 3.3 资金费率与倒计时
- **逻辑**: 资金费率通常每 8 小时结算一次。
- **展示**: 需要显示当前周期的预测费率，以及距离下次结算的倒计时 (HH:MM:SS)。

### 3.4 价格精度与计算
- **前端**: 所有金额计算必须使用 `Decimal` 库 (如 `decimal.js` 或 `bignumber.js`)，严禁使用 JavaScript 原生 `number` 进行加减乘除，防止精度丢失。
- **后端**: 数据库存储使用 `DECIMAL` 或 `Int64` (存聪/微阶)。

---

## 4. 开发路线图 (Roadmap)

1.  **Phase 1: 基础框架与行情**
    - 搭建前端项目 (React/Vue)。
    - 对接 WebSocket `ticker` 和 `trade` 频道。
    - 实现顶部行情栏和最新成交列表。

2.  **Phase 2: K线与盘口**
    - 集成 TradingView 图表。
    - 实现 OrderBook 组件，处理增量更新逻辑。

3.  **Phase 3: 交易功能**
    - 实现下单面板 (表单验证、计算最大可买/可卖)。
    - 对接下单 API 和 订单推送 WebSocket。
    - 实现底部持仓和订单列表。

4.  **Phase 4: 完善与优化**
    - 响应式布局调整。
    - 性能优化 (Canvas 渲染盘口等)。
    - 异常处理 (断网重连)。

---

## 5. 高性能架构与 DevOps (进阶学习目标)

为了支撑上述交易页面的高并发、低延迟需求，我们将采用以下微服务架构和 DevOps 流程。这也是本项目的核心学习目标。

### 5.1 高性能网关 (API Gateway)
- **选型**: APISIX 或 Kong (基于 Nginx/OpenResty) 或 Go 自研网关。
- **职责**:
  - **统一接入**: 处理所有 HTTP/WebSocket 流量。
  - **鉴权 (Auth)**: 统一校验 JWT，解析 UserID 透传给后端。
  - **限流 (Rate Limiting)**: 针对 IP 或 UserID 进行限流，防止 DDoS。
  - **路由转发**: 将 `/api/v1/trade/*` 转发给交易服务，`/api/v1/market/*` 转发给行情服务。

### 5.2 内部通信 (gRPC)
- **设计**: 内部微服务之间严禁使用 HTTP JSON，必须使用 **gRPC (Protobuf)**。
- **优势**:
  - **高性能**: 二进制序列化，比 JSON 快 5-10 倍。
  - **强类型**: `.proto` 文件定义接口，自动生成代码，避免字段拼写错误。
  - **多路复用**: HTTP/2 协议，连接复用。
- **服务划分**:
  - `Matching Engine`: 撮合引擎 (核心，纯内存，极速)。
  - `Risk Engine`: 风控引擎 (强平、保证金检查)。
  - `Account Service`: 账户服务 (充提、划转)。
  - `Market Service`: 行情服务 (K线聚合、推送)。

### 5.3 WebSocket 推送网关 (Push Gateway)
- **挑战**: 单机需支持 10万+ 并发连接，广播行情数据（如 BTC 价格变动需同时推给 10万人）。
- **架构**:
  - **Go 语言实现**: 利用 `epoll` (Netpoll) 和 Goroutine 实现高并发。
  - **架构模式**:
    - `Kafka` (消息源) -> `Push Gateway` (集群) -> `User` (WebSocket)。
  - **优化**:
    - **压缩**: 使用 Gzip/Snappy 压缩消息。
    - **合并**: 100ms 内的多次变动合并为一次推送。
    - **零拷贝**: 广播消息时避免重复内存分配。

### 5.4 Kubernetes (K8s) 容器化部署
- **目标**: 实现服务的自动扩缩容和高可用。
- **组件**:
  - **Deployment**: 部署无状态服务 (API, Web)。
  - **StatefulSet**: 部署有状态服务 (Redis, MySQL - 仅测试环境，生产通常用云服务)。
  - **Service/Ingress**: 暴露服务到外网。
  - **ConfigMap/Secret**: 管理配置和密钥。
  - **HPA (Horizontal Pod Autoscaler)**: 根据 CPU/内存自动增加 Pod 数量。

### 5.5 CI/CD (持续集成/持续部署)
- **流程**:
  1.  **Code**: 代码提交到 GitHub/GitLab。
  2.  **CI (Build & Test)**:
      - 触发 GitHub Actions / GitLab CI。
      - 运行单元测试 (`go test`)。
      - 编译 Docker 镜像并推送到镜像仓库 (Registry)。
  3.  **CD (Deploy)**:
      - 更新 K8s YAML 文件中的镜像版本。
      - 使用 **ArgoCD** (GitOps) 自动同步状态到 K8s 集群。
      - 实现灰度发布 (Canary Deployment)。
