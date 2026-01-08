# 强平引擎 (Liquidation Engine) 技术文档

## 架构概览

```
┌─────────────────────────────────────────────────────────────┐
│                        Engine                               │
│                                                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │   Scanner   │  │  Checkers   │  │  Executor Workers   │ │
│  │  (全量扫描)  │  │ (分级检查)   │  │  (强平执行池)        │ │
│  └──────┬──────┘  └──────┬──────┘  └──────────┬──────────┘ │
│         │                │                     │            │
│         └────────────────┴─────────────────────┘            │
│                          │                                  │
│                          ▼                                  │
│              ┌───────────────────────┐                     │
│              │    RiskLevelIndex     │                     │
│              │  (CowMap 无锁读索引)   │                     │
│              │                       │                     │
│              │  Level 1: Warning     │  → 每 5 秒检查      │
│              │  Level 2: Danger      │  → 每 2 秒检查      │
│              │  Level 3: Critical    │  → 每 500ms 检查    │
│              └───────────────────────┘                     │
└─────────────────────────────────────────────────────────────┘
```

## 风险等级定义

| 等级 | 风险率范围 | 检查频率 | 触发动作 |
|------|-----------|---------|---------|
| Safe | < 70% | 不监控 | 无 |
| Warning | 70% - 80% | 5 秒 | 推送预警 |
| Danger | 80% - 90% | 2 秒 | 限制开仓 |
| Critical | 90% - 100% | 500ms | 价格触发器 |
| Liquidate | ≥ 100% | 立即 | 执行强平 |

---

## Benchmark 测试结果

### 测试环境
- **CPU**: Apple M1/M2 (4 核并行)
- **用户规模**: 20 万持仓用户，2 万高风险用户

### 性能数据

| Benchmark | 耗时 | 内存 | 分配次数 | 评价 |
|-----------|------|------|---------|------|
| Scanner_Scan_200K | 127ms | 53MB | 400,867 | 🟡 可优化 |
| Scanner_Scan_20K | 15ms | 18MB | 40,404 | ✅ 良好 |
| CowMap_ConcurrentRead | 11.6ns | 0 | 0 | 🟢 极佳 |
| CowMap_GetAll | 837μs | 1.6MB | 1 | 🟡 可优化 |
| CowMap_BatchUpdate | 855μs | 1.5MB | 35 | 🟡 预期内 |
| RiskLevelIndex_GetByLevel | 960μs | 1.6MB | 3 | 🔴 需优化 |
| Engine_TriggerLiquidation | 8μs | 130B | 2 | ✅ 良好 |
| Engine_FullCycle | 78ms | 53MB | 400,845 | 🟡 可优化 |

### 关键指标

| 指标 | 数值 |
|------|------|
| 全量扫描 200K 用户 | ~80-130ms |
| 吞吐量 | ~220 万用户/秒 |
| 每用户计算耗时 | ~0.45μs |
| 无锁读延迟 | 11.6ns |
| 强平触发延迟 | 8μs |

---

## 性能瓶颈分析

### 🔴 问题 1: GetAll/GetByLevel 内存分配过大

**现象**: 每次调用分配 1.6MB 内存

**原因**: 每次返回都创建新的 slice 复制数据

```go
// 当前实现
func (m *CowMap) GetAll() []UserRiskData {
    result := make([]UserRiskData, 0, len(*currentMap)) // ← 分配
    for _, v := range *currentMap {
        result = append(result, v)
    }
    return result
}
```

**优化方案**:
```go
// 方案1: 返回只读指针
func (m *CowMap) GetAllReadOnly() *map[int64]UserRiskData {
    return m.data.Load()
}

// 方案2: 使用对象池
var slicePool = sync.Pool{
    New: func() interface{} {
        return make([]UserRiskData, 0, 10000)
    },
}
```

---

### 🔴 问题 2: Scanner 分配过多

**现象**: 200K 用户扫描产生 40 万次内存分配

**热点**:
1. 每用户创建 `risk.RiskInput` (含 map/slice)
2. 每用户创建 `UserRiskData`
3. 结果合并时创建临时 slice

**优化方案**:
```go
// 1. RiskInput 对象池
var riskInputPool = sync.Pool{
    New: func() interface{} {
        return &risk.RiskInput{
            Prices: make(map[string]risk.PriceSnapshot, 8),
        }
    },
}

// 2. 预分配结果容器
results := make([]UserRiskData, 0, expectedUserCount)

// 3. 避免循环内 make
// Before: LiquidationPrices: make(map[string]float64)
// After:  延迟初始化或复用
```

---

### 🟡 问题 3: BatchUpdate CoW 复制开销

**现象**: 每次更新复制整个 map (~855μs)

**这是 CoW 的固有特性**，权衡点：
- ✅ 读操作完全无锁
- ❌ 写操作需要全量复制

**如果写性能要求高，可考虑**:
- 分片 map 降低单次复制规模
- 或改用 `sync.Map`（但读性能会下降）

---

## 优化优先级

| 优先级 | 优化项 | 预期收益 | 复杂度 |
|--------|--------|----------|--------|
| **P0** | GetByLevel 返回只读视图 | 减少 1.6MB/次 | 低 |
| **P1** | RiskInput 对象池 | 减少 30% 分配 | 中 |
| **P2** | 预分配结果 slice | 减少内存碎片 | 低 |
| **P3** | UserRiskData 对象池 | 减少 GC 压力 | 中 |

---

## 生产化 TODO

### Phase 1: 基础生产化
- [ ] PositionCache: 内存缓存 + Kafka 事件驱动同步
- [ ] LiquidationExecutor: 对接撮合引擎发送强平单
- [ ] 分布式锁: Redis/etcd 防止多节点重复强平
- [ ] 审计日志: 记录所有强平操作
- [ ] 幂等性保证: 防止重复强平

### Phase 2: 资金安全
- [ ] 保险基金模块
- [ ] ADL 自动减仓
- [ ] 部分强平逻辑
- [ ] 强平订单优先级

### Phase 3: 可观测性
- [ ] Prometheus 指标
- [ ] Grafana 仪表盘
- [ ] 告警规则
- [ ] 熔断机制

---

## 核心文件说明

| 文件 | 职责 |
|------|------|
| `model.go` | 风险等级、用户数据结构、强平任务定义 |
| `index.go` | CowMap 无锁读索引、RiskLevelIndex |
| `scanner.go` | 全量扫描器、分片并行处理 |
| `engine.go` | 引擎入口、检查器、Worker Pool |
| `*_test.go` | 单元测试 |
| `bench_test.go` | 性能测试 (200K 用户) |
