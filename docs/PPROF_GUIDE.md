# Go pprof 性能优化指南

## 优化循环

```
┌──────────────────────────────────────────────────────────────┐
│                    性能优化循环                               │
│                                                              │
│   ┌─────────┐     ┌─────────┐     ┌─────────┐     ┌────────┐│
│   │ 1.采集   │ ──► │ 2.分析   │ ──► │ 3.优化   │ ──► │ 4.验证 ││
│   │ Profile │     │ 热点    │     │ 代码    │     │ 效果   ││
│   └─────────┘     └─────────┘     └─────────┘     └────────┘│
│        │                                              │      │
│        └──────────────────────────────────────────────┘      │
│                         循环                                 │
└──────────────────────────────────────────────────────────────┘
```

---

## Step 1: 采集 Profile

### CPU Profile（分析哪些函数消耗 CPU）
```bash
go test -bench=BenchmarkXXX -benchtime=3s -cpuprofile=cpu.prof ./pkg/xxx/...
```

### 内存 Profile（分析内存分配热点）
```bash
go test -bench=BenchmarkXXX -benchtime=3s -memprofile=mem.prof ./pkg/xxx/...
```

### 阻塞 Profile（分析锁竞争）
```bash
go test -bench=BenchmarkXXX -blockprofile=block.prof ./pkg/xxx/...
```

### Mutex Profile（分析互斥锁争用）
```bash
go test -bench=BenchmarkXXX -mutexprofile=mutex.prof ./pkg/xxx/...
```

---

## Step 2: 分析热点

### 查看 Top 函数（按累计时间排序）
```bash
go tool pprof -top -cum cpu.prof | head -30
```

### 输出说明
| 列 | 含义 |
|----|------|
| flat | 该函数**自身**消耗的时间 |
| flat% | flat 占总时间百分比 |
| cum | 该函数**总共**消耗的时间（含子函数） |
| cum% | cum 占总时间百分比 |

### 查看具体代码行
```bash
go tool pprof -list="函数名" cpu.prof
```

### 生成可视化 PNG 图
```bash
go tool pprof -png cpu.prof > cpu_profile.png
```

### 交互式终端（支持更多命令）
```bash
go tool pprof cpu.prof
# 常用命令：
# top20      - 查看 Top 20 热点
# list XXX   - 查看函数源码
# web        - 浏览器打开可视化图
# pdf        - 生成 PDF
```

### 内存分析（按分配空间排序）
```bash
go tool pprof -top -alloc_space mem.prof | head -30
```

---

## Step 3: 常见优化手段

### 减少内存分配

| 问题 | 解决方案 | 示例 |
|------|---------|------|
| 频繁创建对象 | `sync.Pool` 对象池 | Map、Slice、Struct |
| 切片复制 | 返回只读指针/迭代器 | `GetAllReadOnly()` |
| Map 频繁创建 | 延迟初始化 / nil | `LiquidationPrices: nil` |
| 每次调用系统函数 | 批量获取 | `time.Now()` |

### sync.Pool 示例
```go
var pool = sync.Pool{
    New: func() interface{} {
        return make(map[int64]Data, 50000)
    },
}

// 获取
m := pool.Get().(map[int64]Data)

// 使用后归还
clear(m)  // Go 1.21+
pool.Put(m)
```

### 只读指针示例
```go
// 优化前：每次复制
func (m *CowMap) GetAll() []Data {
    result := make([]Data, 0, len(*m.data))  // 分配！
    for _, v := range *m.data {
        result = append(result, v)
    }
    return result
}

// 优化后：零分配
func (m *CowMap) GetAllReadOnly() *map[int64]Data {
    return m.data.Load()  // 直接返回指针
}
```

### 减少 GC 压力

| 方法 | 说明 |
|------|------|
| 栈变量 | 小对象用值类型，不用指针 |
| 预分配 | `make([]T, 0, capacity)` |
| 复用 Buffer | `bytes.Buffer` 复用 |
| 避免闭包 | 闭包会逃逸到堆 |

---

## Step 4: 验证效果

### 运行优化后的 Benchmark
```bash
go test -bench=BenchmarkXXX -benchtime=3s -benchmem ./pkg/xxx/...
```

### 关注指标
```
BenchmarkXXX-4    100    50000000 ns/op    19000000 B/op    200000 allocs/op
                  ↑         ↑                ↑                ↑
                迭代次数   每次耗时(ns)    每次内存(B)      每次分配次数
```

### 优化目标
- **ns/op** ↓ - 耗时减少
- **B/op** ↓ - 内存减少
- **allocs/op** ↓ - 分配次数减少（减少 GC 压力）

---

## 实战案例：强平引擎优化

### 优化前
```
65ms, 53MB, 400,848 allocs
```

### 优化措施
1. `time.Now()` 批量获取 → CPU -6%
2. `make(map)` 改 nil → CPU -5%
3. `GetByLevelReadOnly` → 零分配遍历
4. `sync.Pool` 对象池 → 内存 -47%

### 优化后
```
49ms, 19MB, 200,335 allocs
```

### 改进
- 耗时: ↓ 25%
- 内存: ↓ 64%
- 分配: ↓ 50%

---

## 关键原则

1. **先测量，再优化** - 不要凭直觉猜测瓶颈
2. **优化最热的点** - 80/20 法则，优化 Top 3 热点
3. **每次改一点** - 验证每个优化的效果
4. **保持代码可读** - 不要为了 1% 性能牺牲可维护性
5. **循环迭代** - 优化 → 验证 → 再优化

---

## 常用命令速查

```bash
# CPU 分析
go test -bench=. -cpuprofile=cpu.prof ./...
go tool pprof -top -cum cpu.prof

# 内存分析
go test -bench=. -memprofile=mem.prof ./...
go tool pprof -top -alloc_space mem.prof

# 查看具体函数
go tool pprof -list="FunctionName" xxx.prof

# 生成图片
go tool pprof -png xxx.prof > output.png

# 过滤日志运行
go test -bench=. ./... 2>&1 | grep -E "(Benchmark|ns/op|B/op)"
```

---

## 逃逸分析 (Escape Analysis)

逃逸分析决定变量分配在栈还是堆。**堆分配 = GC 压力 = 性能下降**。

### 运行逃逸分析

```bash
# 基本逃逸信息
go build -gcflags='-m' ./pkg/xxx/...

# 详细逃逸原因
go build -gcflags='-m -m' ./pkg/xxx/...

# 只看逃逸相关
go build -gcflags='-m -m' ./pkg/xxx/... 2>&1 | grep -E "(escapes|moved to heap)"
```

### 输出解读

| 关键词 | 含义 |
|--------|------|
| `escapes to heap` | 变量逃逸到堆（需要关注） |
| `moved to heap: xxx` | 变量 xxx 被移动到堆 |
| `does not escape` | 没有逃逸（好的！） |
| `can inline` | 可以内联（好的！） |

### 逃逸原因分类

| 场景 | 原因 | 示例 |
|------|------|------|
| **返回指针** | 栈帧销毁后仍需访问 | `return &obj` |
| **存入接口** | 接口需要类型信息 | `fmt.Println(x)`, `log.Printf` |
| **存入容器** | Map/Slice 可能超出栈 | `m[k] = v` |
| **闭包捕获** | 闭包生命周期超出函数 | `go func() { use(x) }()` |
| **大于栈限制** | 超出 64KB | `var arr [100000]int` |
| **sync.Pool** | 接口存储必须逃逸 | `pool.Put(x)` |

### 区分"好的逃逸" vs "坏的逃逸"

**必要的逃逸（设计需要）**：
```go
func NewEngine() *Engine {
    return &Engine{}  // 必须逃逸，API 需要返回指针
}
```

**可优化的逃逸（热路径）**：
```go
for _, user := range users {
    symbols := make([]string, 0, 8)     // 每次分配，可用对象池
    log.Printf("user %d", user.ID)      // int64 装箱逃逸
}
```

### 减少逃逸的技巧

#### 1. 返回值而非指针
```go
// 逃逸
func createData() *Data { return &Data{} }

// 不逃逸
func createData() Data { return Data{} }
```

#### 2. 避免热路径中的日志
```go
// 逃逸（userID 装箱）
log.Printf("user %d", userID)

// 不逃逸（条件日志）
if debug { log.Printf("user %d", userID) }
```

#### 3. 用固定数组替代切片
```go
// 逃逸
symbols := make([]string, 0, 8)

// 不逃逸（如果数量有上限）
var symbols [8]string
count := 0
```

#### 4. 对象池复用
```go
var symbolPool = sync.Pool{
    New: func() interface{} { return make([]string, 0, 8) },
}

// 获取
symbols := symbolPool.Get().([]string)
symbols = symbols[:0]  // 清空

// 归还
symbolPool.Put(symbols)
```

### 实战示例

```bash
$ go build -gcflags='-m -m' ./pkg/liquidation/scanner.go 2>&1 | grep escapes

scanner.go:346:34: make([]string, 0, len(input.Positions)) escapes to heap
scanner.go:334:69: userID escapes to heap  ← log.Printf 导致
```

**分析**：
- `make([]string)` 逃逸：symbols 被存入 UserRiskData，而 UserRiskData 存入 Map
- `userID` 逃逸：`log.Printf` 参数是 `...interface{}`，int64 装箱

---

## allocs/op 深度分析

### 查看分配次数热点

```bash
# 按分配次数排序（不是大小）
go tool pprof -top -alloc_objects mem.prof | head -20
```

### 输出示例

```
      flat  flat%   sum%        cum   cum%
  14581982 84.21% 84.21%   14581982 84.21%  (*Scanner).convertToUserRiskData
   2542786 14.69% 98.90%    2542786 14.69%  createMockRiskInputForBench
    114294  0.66% 99.56%     114294  0.66%  (*CowMap).GetAll
```

### 解读

- `flat`: 该函数直接产生的分配次数
- `cum`: 该函数及其调用链产生的总分配次数
- 上例中 `convertToUserRiskData` 产生 84% 的分配

### 优化思路

1. **减少分配频率** → 对象池、复用
2. **减少分配大小** → 预分配、固定容量
3. **避免分配** → 栈变量、值传递

