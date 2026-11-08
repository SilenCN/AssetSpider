// Package utils 是横切工具层，只放与业务无关的纯函数。
//
// 它不属于 router→controller→service→repository 调用链，任何一层都可以引用，
// 但它自身不允许依赖本项目任何 internal 包（由 architecture_test.go 强制校验）。
package utils

import (
	"errors"
	"strconv"
)

// ParseUintParam 解析路径中的正整数 ID，空值、0 与非数字都视为非法。
func ParseUintParam(raw string) (uint, error) {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New("无效的 id")
	}
	return uint(n), nil
}

// ParseUintQuery 解析可选的无符号 query 参数，非法值按 0 处理。
func ParseUintQuery(raw string) uint {
	n, _ := strconv.ParseUint(raw, 10, 64)
	return uint(n)
}

// ParseIntQuery 解析可选的有符号 query 参数，非法值按 0 处理。
func ParseIntQuery(raw string) int {
	n, _ := strconv.Atoi(raw)
	return n
}
