package exchange

import (
	"regexp"
	"strings"

	"github.com/quant1x/num"
	"github.com/quant1x/x/exception"
	"github.com/quant1x/x/logger"
)

// 正则表达式
var (
	// 值范围正则表达式
	valueRangePattern = "[~]\\s*"
	valueRangeRegexp  = regexp.MustCompile(valueRangePattern)

	// 数组正则表达式
	arrayPattern = "[,]\\s*"
	arrayRegexp  = regexp.MustCompile(arrayPattern)

	tradingTimeRangePattern       = "([A-Z]{1,4}\\|)?(\\d{2}:\\d{2}:\\d{2})[-~](\\d{2}:\\d{2}:\\d{2})"
	tradingTimeRangePatternRegexp = regexp.MustCompile(tradingTimeRangePattern)
)

// 错误信息
var (
	errnoConfig    = 0
	ErrRangeFormat = exception.New(errnoConfig+0, "数值范围格式错误")
)

type ValueType interface {
	~int | ~float64 | ~string
}

func ParseRange[T ValueType](text string) ValueRange[T] {
	text = strings.TrimSpace(text)
	arr := valueRangeRegexp.Split(text, -1)
	if len(arr) != 2 {
		logger.Fatalf("text=%s, %+v", text, ErrTimeFormat)
	}
	var begin, end T
	begin = num.GenericParse[T](strings.TrimSpace(arr[0]))
	end = num.GenericParse[T](strings.TrimSpace(arr[1]))
	if begin > end {
		begin, end = end, begin
	}
	r := ValueRange[T]{
		begin: begin,
		end:   end,
	}
	return r
}

// ValueRange 数值范围
type ValueRange[T ValueType] struct {
	begin T // 最小值
	end   T // 最大值
}

// In 检查是否包含在范围内
func (r ValueRange[T]) In(v T) bool {
	if v < r.begin || v > r.end {
		return false
	}
	return true
}
