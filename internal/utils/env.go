package utils

import (
	"fmt"
	"os"
	"strconv"
)

// EnvInt 读取整数型环境变量并写入 dst；变量为空时保持不变。
func EnvInt(name string, dst *int) error {
	v := os.Getenv(name)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("环境变量 %s 需要整数值，实际 %q", name, v)
	}
	*dst = n
	return nil
}
