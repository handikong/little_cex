package tests

import (
	"fmt"
	"testing"
	"unsafe"
)

// 内存对齐 按照从大到小的顺序排列
// 乱序：小 -> 大 -> 小
type BadPosition struct {
	IsIsolated bool
	Qty        float64
	IsLong     bool
}

// 优化：大 -> 小
type GoodPosition struct {
	Qty        float64
	IsIsolated bool
	IsLong     bool
}

func TestMemory(t *testing.T) {
	fmt.Printf("BadStruct size:  %d bytes\n", unsafe.Sizeof(BadPosition{}))
	fmt.Printf("GoodStruct size: %d bytes\n", unsafe.Sizeof(GoodPosition{}))
}
