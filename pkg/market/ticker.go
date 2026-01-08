package market

import (
	"math"
	"math/rand"
	"time"

	"max.com/pkg/risk"
)

// Ticker 模拟高频行情生成器
// 设计目标：
// 1. 高吞吐：每秒生成数万次价格更新
// 2. 非阻塞：不能因为下游慢而卡住
// 3. 逼真数据：使用几何布朗运动 (GBM) 生成价格
type Ticker struct {
	// ========== 基础配置字段 ==========
	Symbol   string        // 交易对标识，如 "BTC_USDT"
	Price    float64       // 当前价格（会动态变化）
	Interval time.Duration // 生成频率，如 100*time.Millisecond

	// ========== 并发控制字段 ==========
	// 停止信号：使用 chan struct{} 而不是 chan bool 的原因：
	// 1. struct{} 是零大小类型，不占内存
	// 2. close(stopChan) 本身就是广播信号，不需要传值
	// 3. 所有监听 stopChan 的 Goroutine 都会立即收到信号
	stopChan chan struct{}

	// 输出通道：带缓冲的原因：
	// 1. 无缓冲 Channel 会导致发送方必须等接收方（同步操作），性能极差
	// 2. 带缓冲可以抵抗接收方的短暂停顿（如 GC pause 10ms）
	// 3. Buffer 大小计算公式：预期最大延迟(ms) × 生成频率(次/ms)
	//    例如：10ms 延迟 × 10 次/ms = 100
	outChan chan risk.PriceSnapshot

	// ========== 状态字段 ==========
	lastUpdated time.Time // 上次更新时间，用于计算 GBM 的时间步长 dt
	Volatility  float64   // 年化波动率，用于 GBM 计算（如 0.5 代表 50%）
}

// NewTicker 创建一个新的行情生成器
// 参数：
//
//	symbol: 交易对标识
//	startPrice: 初始价格
//	interval: 生成频率
func NewTicker(symbol string, startPrice float64, interval time.Duration) *Ticker {
	return &Ticker{
		Symbol:      symbol,
		Price:       startPrice,
		Volatility:  0.5, // 默认 50% 年化波动率（加密货币典型值）
		Interval:    interval,
		stopChan:    make(chan struct{}),                // 无缓冲，仅用于广播停止信号
		outChan:     make(chan risk.PriceSnapshot, 100), // 缓冲 100，可抵抗 10ms 的延迟
		lastUpdated: time.Now(),
	}
}

// Start 启动行情生成器
// 返回一个只读 Channel，调用者可以从中接收价格快照
// 为什么返回 <-chan 而不是 chan？
// 答：类型安全，防止调用者误写入 Channel
func (t *Ticker) Start() <-chan risk.PriceSnapshot {
	// 在后台 Goroutine 中运行核心循环
	// 不能直接调用 t.loop()，那会阻塞当前线程
	go t.loop()
	return t.outChan
}

// Stop 优雅停止行情生成器
// 通过关闭 stopChan 来广播停止信号
func (t *Ticker) Stop() {
	close(t.stopChan)
}

// loop 核心循环（Hot Path）
// 这是整个 Ticker 最关键的部分，性能至关重要
func (t *Ticker) loop() {
	// 创建定时器
	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()    // 释放定时器资源，防止泄漏
	defer close(t.outChan) // 关闭输出通道，通知下游"无更多数据"

	// 【性能优化1】创建独立的随机源
	// 为什么不用全局 rand.Float64()？
	// 答：全局 rand 包内部有一个 Mutex，高并发下会成为瓶颈
	// 独立随机源性能提升：~500ns/op -> ~50ns/op（提升 10 倍）
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		select {
		// 收到停止信号，退出循环
		case <-t.stopChan:
			return

		// 定时器触发，生成新价格
		case now := <-ticker.C:
			// ========== 第一步：计算时间步长 ==========
			// GBM 需要知道距离上次更新过了多久
			// dt 单位是"年"（年化）
			dt := now.Sub(t.lastUpdated).Hours() / 24 / 365
			if dt <= 0 {
				dt = 1e-9 // 防止除 0 或负数
			}

			// ========== 第二步：几何布朗运动 (GBM) ==========
			// 公式推导：
			//   dS = S * (μ*dt + σ*dW)
			//   其中 dW = sqrt(dt) * Z，Z ~ N(0,1)
			//   简化为：S_new = S * exp((μ - 0.5*σ²)*dt + σ*sqrt(dt)*Z)
			//   我们设 μ=0（无漂移），所以：
			//   S_new = S * exp(-0.5*σ²*dt + σ*sqrt(dt)*Z)
			//
			// 为什么用 GBM 而不是简单的随机游走？
			// 1. GBM 保证价格永远是正数（乘法而非加法）
			// 2. 符合真实资产价格的对数正态分布
			// 3. 波动率可控（通过 sigma 参数）

			sigma := t.Volatility
			z := r.NormFloat64() // 生成标准正态分布随机数 N(0,1)

			// 计算价格变动因子
			change := math.Exp(-0.5*sigma*sigma*dt + sigma*math.Sqrt(dt)*z)
			t.Price *= change
			t.lastUpdated = now

			// ========== 第三步：构造快照 ==========
			// 这个结构体很小（几个 float64 + time.Time），大概率在栈上分配
			// 不会产生堆内存分配（Zero Allocation）
			snap := risk.PriceSnapshot{
				Price:       t.Price,
				MarkPrice:   t.Price, // 简化：标记价 = 最新价
				FundingRate: 0.0001,  // 假定资金费率 0.01%
				Ts:          now,
			}

			// ========== 第四步：非阻塞发送（背压处理）==========
			// 【性能优化2】使用 select default 实现非阻塞发送
			//
			// 为什么不能直接 t.outChan <- snap？
			// 答：如果 outChan 满了（下游处理慢），发送方会阻塞
			//     一个慢消费者会拖累整个系统
			//
			// Drop Strategy（丢包策略）的哲学：
			// 在高频交易系统中，旧数据没有价值
			// 宁可丢弃最新数据，也不能让生产者卡死
			//
			// 其他策略对比：
			// - Block（阻塞）：等待消费者，适合订单系统（不能丢单）
			// - Drop（丢包）：丢弃数据，适合行情系统（旧价格无用）
			// - Overwrite（覆盖）：覆盖最旧数据，Ring Buffer 常用
			select {
			case t.outChan <- snap:
				// 发送成功，继续下一轮
			default:
				// Channel 满了，丢弃这条行情
				// 生产环境应该在这里记录 metrics: ticker_dropped_count++
				// 用于监控系统健康度
			}
		}
	}
}
