package data

import (
	"math/rand"
	"testing"
)

// boardOf 板块分类：沪主板/深主板/创业板/科创板/北交所按代码前缀判定。
func TestBoardOf(t *testing.T) {
	cases := []struct {
		symbol string
		want   string
	}{
		{"sh600519", "sh"},
		{"sh601318", "sh"},
		{"sh688981", "kc"},
		{"sz000001", "sz"},
		{"sz002594", "sz"},
		{"sz300059", "cy"},
		{"sz301236", "cy"},
		{"bj832566", "bj"},
		{"bj920002", "bj"},
		{"sz399001", "other"}, // 指数应归 other
		{"sh000001", "other"}, // 上证指数归 other
	}
	for _, c := range cases {
		if got := boardOf(c.symbol); got != c.want {
			t.Errorf("boardOf(%s) 应=%s 实际=%s", c.symbol, c.want, got)
		}
	}
}

// shuffleSymbols 固定种子可复现，且洗牌保持元素集不变。
func TestShuffleSymbolsDeterministic(t *testing.T) {
	mk := func() []StockSymbolInfo {
		return []StockSymbolInfo{
			{Symbol: "sh600001"}, {Symbol: "sz000001"}, {Symbol: "sz300001"},
			{Symbol: "sz300002"}, {Symbol: "sh600002"},
		}
	}
	run := func(s int64) []StockSymbolInfo {
		in := mk()
		return shuffleSymbols(rand.New(rand.NewSource(s)), in)
	}
	a := run(20240901)
	b := run(20240901)
	c := run(999999)

	if !sameSymbolSet(a, mk()) || !sameSymbolSet(b, mk()) {
		t.Fatalf("洗牌改变了原集合")
	}
	if !sameOrder(a, b) {
		t.Fatalf("相同种子应产生相同结果：a=%v b=%v", a, b)
	}
	if sameOrder(a, c) {
		t.Fatalf("不同种子不应产生相同结果")
	}
}

func sameSymbolSet(a, b []StockSymbolInfo) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s.Symbol]++
	}
	for _, s := range b {
		m[s.Symbol]--
		if m[s.Symbol] < 0 {
			return false
		}
	}
	return true
}

func sameOrder(a, b []StockSymbolInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Symbol != b[i].Symbol {
			return false
		}
	}
	return true
}
