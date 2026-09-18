package tools

import (
	"testing"
)

// retVolMaxDD：无波动时应0波动/0回撤；强下行序列应得到显著负最大回撤与正波动率。
func TestRetVolMaxDD(t *testing.T) {
	// 恒定1%上涨：波动率~0，回撤~0
	up := make([]float64, 50)
	for i := range up {
		up[i] = 0.01
	}
	v, dd := retVolMaxDD(up)
	if v > 0.0001 {
		t.Fatalf("恒定上涨波动率应≈0，实际 %.6f", v)
	}
	if dd < -0.0001 {
		t.Fatalf("恒定上涨不应有回撤，实际 %.6f", dd)
	}

	// 先涨后断崖下杀：应捕获显著负回撤
	series := append(make([]float64, 20), 0.01, 0.02, 0.01, -0.05, -0.10, -0.25, -0.30, -0.15, 0.01, 0.02, 0.03, 0.05, 0.01, 0.02)
	_, dd2 := retVolMaxDD(series)
	if dd2 > -20 {
		t.Fatalf("断崖下杀应产生<-20%%回撤，实际 %.2f%%", dd2)
	}

	// 极端振幅应体现为高年化波动率
	vol, _ := retVolMaxDD([]float64{0.1, -0.1, 0.1, -0.1, 0.1, -0.1, 0.1, -0.1, 0.1, -0.1})
	if vol < 100 {
		t.Fatalf("±10%%日收益年化波动率应>100%%，实际 %.1f%%", vol)
	}
}

// toStringSlice：只能提取字符串元素，忽略其他类型与空串。
func TestToStringSlice(t *testing.T) {
	got := toStringSlice([]interface{}{"600519", 123, "sz000858", "", true})
	if len(got) != 2 || got[0] != "600519" || got[1] != "sz000858" {
		t.Fatalf("toStringSlice 结果异常: %#v", got)
	}
	if toStringSlice("not-a-slice") != nil {
		t.Fatal("非切片输入应返回 nil")
	}
}

// countStatusOk：统计状态为 ok 的条目数。
func TestCountStatusOk(t *testing.T) {
	in := []map[string]interface{}{
		{"status": "ok"},
		{"status": "insufficient_data"},
		{"status": "ok"},
	}
	if n := countStatusOk(in); n != 2 {
		t.Fatalf("ok 计数应为2，实际 %d", n)
	}
}
