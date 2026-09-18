// Package version 提供应用版本号的单一权威来源与语义化版本比较。
package version

import (
	"strconv"
	"strings"
)

// 应用元信息（唯一权威来源，避免多处硬编码导致不一致）
const (
	AppName = "QuantBot AI"
	Version = "1.6.0" // 语义化版本 major.minor.patch
	Channel = "stable"
)

// Compare 比较两个语义化版本号 a、b（自动忽略前导 v 与 pre-release 后缀）。
// 返回 -1: a < b；0: a == b；1: a > b。
func Compare(a, b string) (int, error) {
	an, err := parse(a)
	if err != nil {
		return 0, err
	}
	bn, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if an[i] < bn[i] {
			return -1, nil
		}
		if an[i] > bn[i] {
			return 1, nil
		}
	}
	return 0, nil
}

// parse 解析 major.minor.patch 三元组，缺省位视为 0
func parse(v string) ([3]int, error) {
	var out [3]int
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	main := strings.SplitN(v, "-", 2)[0] // 忽略 pre-release 后缀
	nums := strings.Split(main, ".")
	if len(nums) > 3 {
		nums = nums[:3]
	}
	for i := 0; i < 3; i++ {
		if i < len(nums) && nums[i] != "" {
			n, err := strconv.Atoi(nums[i])
			if err != nil {
				return out, err
			}
			out[i] = n
		}
	}
	return out, nil
}
