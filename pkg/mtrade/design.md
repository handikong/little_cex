# 撮合引擎 (Matching Engine) 架构设计

## 一、核心数据结构

### 1.1 数据结构对比

| 数据结构 | 插入 | 删除 | 最优价 | 优势 | 劣势 |
|----------|------|------|--------|------|------|
| **红黑树** | O(log n) | O(log n) | O(1)* | 稳定、成熟 | 指针多，cache 不友好 |
| **跳表** | O(log n) | O(log n) | O(1) | 实现简单、并发友好 | 空间开销 |
| **Hash + 堆** | O(log n) | O(n) | O(1) | 简单 | 堆删除是短板 |

### 1.2 工业选择

| 交易所/系统 | 数据结构 | 语言 |
|-------------|----------|------|
| LMAX Exchange | 红黑树 | Java |
| Nasdaq INET | 红黑树变体 | C++ |
| Redis | 跳表 | C |

**结论**：工业主流是红黑树/跳表，我们选择**跳表**（实现简单，面试常考）

### 1.3 两层结构设计
Level 1: 跳表 (按价格排序) │ ┌────┴────┬─────────┬─────────┐ ▼ ▼ ▼ ▼ 49800 49900 50000 50100 │ │ │ │ ▼ ▼ ▼ ▼ Level 2: 链表/队列 (同价格 FIFO) [O1,O2] [O3] [O4,O5,O6] [O7]


---

## 二、订单簿设计

### 2.1 核心结构

```go
// 订单
type Order struct {
    ID        int64
    UserID    int64
    Side      Side      // Buy/Sell
    Price     int64     // 定点数，避免浮点
    Qty       int64
    FilledQty int64
    Status    OrderStatus
    CreatedAt int64
}

// 价格档位
type PriceLevel struct {
    Price    int64
    TotalQty int64
    Orders   []*Order  // FIFO 队列
}

// 订单簿
type OrderBook struct {
    Symbol     string
    Bids       *SkipList        // 买盘：价格降序
    Asks       *SkipList        // 卖盘：价格升序
    OrderIndex map[int64]*Order // O(1) 订单查找
}

2.2 订单类型
类型	说明
Limit	限价单，能成交就成交，否则挂单
Market	市价单，吃掉对手盘
IOC	立即成交或取消
FOK	全部成交或取消
GTC	一直有效直到取消
Post Only	仅做 Maker
三、撮合流程
┌─────────────┐
│  新订单到达  │
└──────┬──────┘
       ▼
┌─────────────────┐
│ 1. 风控前置检查  │ → 余额/持仓/限流
└───────┬─────────┘
        ▼
┌─────────────────┐
│ 2. 尝试撮合      │ ◀──▶ 对手盘 OrderBook
└───────┬─────────┘
        │
   ┌────┴────┐
   ▼         ▼
┌──────┐  ┌──────┐
│ 成交  │  │ 挂单  │
└──┬───┘  └──┬───┘
   │         │
   ▼         ▼
┌─────────────────┐
│ 3. 事件发布      │ → Trade / OrderBook Update
└─────────────────┘
3.1 撮合优先级
价格优先 (Price Priority)
时间优先 (Time Priority, FIFO)
四、并发模型
4.1 单线程撮合（推荐）
订单入口 (多线程) → 订单队列 (Lock-free) → 撮合线程 (单线程) → 事件分发
为什么单线程？

无锁，极致性能
订单顺序性保证
状态一致性简单
调试测试容易
五、持久化与恢复
5.1 WAL + Snapshot
Order → WAL Writer → Memory Engine → Event
            │
            ▼
      [WAL Log] + [Snapshot 每 5 分钟]
恢复流程：

加载最近 Snapshot
重放 WAL
Ready
六、性能优化要点
优化点	方案
零分配	订单对象池、预分配切片
无锁读	订单簿快照、CoW
减少系统调用	避免热路径 time.Now()、log
CPU 亲和性	单线程撮合，绑定 CPU
内存对齐	结构体字段对齐
七、待讨论问题
问题	方案
价格精度	定点数 int64，price * 10^8
订单 ID	Snowflake ID
自成交	取消新单
订单状态机	New → Filled / Canceled
八、目录结构
pkg/matching/
├── DESIGN.md           # 架构设计
├── order.go            # 订单结构
├── orderbook.go        # 订单簿
├── pricelevel.go       # 价格档位
├── skiplist.go         # 跳表实现
├── engine.go           # 撮合引擎
├── matcher.go          # 撮合逻辑
├── event.go            # 事件定义
└── *_test.go           # 测试
面试高频题
数据结构
问题	答案要点
订单簿用什么数据结构？	两层：跳表/红黑树 + FIFO 队列 + HashMap 索引
红黑树 vs 跳表？	红黑树稳定，跳表简单并发友好
如何 O(1) 取消订单？	HashMap: OrderID → Order
撮合逻辑
问题	答案要点
Maker vs Taker？	Maker 挂单提供流动性，Taker 吃单消耗流动性
IOC/FOK/GTC？	IOC 立即成交或取消，FOK 全部或取消，GTC 一直有效
撮合流程？	验证 → 匹配对手盘 → 生成成交 → 挂单/取消 → 发布事件
性能优化
问题	答案要点
如何零 GC？	对象池、预分配、避免闭包
为什么单线程？	无锁、顺序性、一致性、简单
如何微秒延迟？	CPU 亲和、无锁队列、批量处理
系统设计
问题	答案要点
重启如何恢复？	WAL + Snapshot 重放
高可用架构？	主从复制、故障转移
100 万 TPS？	多交易对分片、批量撮合、异步事件
手撕代码
实现 OrderBook (AddOrder / CancelOrder / Match)
实现 SkipList (Insert / Delete / Search)
实现 Snowflake ID 生成器

