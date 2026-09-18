package exchange

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/quant1x/num"
	"github.com/quant1x/pkg/yaml"
	"github.com/quant1x/x/api"
	"github.com/quant1x/x/exception"
)

// 数值范围正则表达式
var (
	numberRangePattern = "[~]\\s*"
	numberRangeRegexp  = regexp.MustCompile(numberRangePattern)
)

var (
	ErrNumberFormat = exception.New(errnoConfig+1, "数值范围格式错误")
)

// NumberRange 数值范围
//
//	支持:
//	1) "1~2",   最小值1, 最大值2
//	2) "",      最小值默认, 最大值默认
//	3) "3.82",  最小值, 最大值默认
//	4) "3.82~", 最小值, 最大值默认
//	5) "~3.82", 最小值默认, 最大值
type NumberRange struct {
	min float64
	max float64
}

func (this NumberRange) String() string {
	return fmt.Sprintf("{min: %f, max: %f}", this.min, this.max)
}

func (this *NumberRange) setMinDefault() {
	this.min = num.MinFloat64
}

func (this *NumberRange) setMaxDefault() {
	this.max = num.MaxFloat64
}

func (this *NumberRange) init() {
	this.setMinDefault()
	this.setMaxDefault()
}

func (this *NumberRange) Max() float64 {
	return this.max
}

func (this *NumberRange) Min() float64 {
	return this.min
}

func (this *NumberRange) Parse(text string) error {
	text = strings.TrimSpace(text)
	if len(text) == 0 {
		// 如果字符串为空, 设置默认值
		this.init()
		return ErrNumberFormat
	}
	arr := numberRangeRegexp.Split(text, -1)
	if len(arr) > 2 {
		// 如果字符串拆分超过2个元素,格式错误,设置默认值
		this.init()
		return ErrNumberFormat
	}
	var begin, end string
	switch len(arr) {
	case 1: // 如果只有1个值,只是最小
		begin = strings.TrimSpace(arr[0])
	case 2: // 如果有2个值, 分最大值和最小值
		begin = strings.TrimSpace(arr[0])
		end = strings.TrimSpace(arr[1])
	default:
		// 如果字符串拆分超过2个元素,格式错误,设置默认值
		// 按说流程是不会走到这里的
		this.init()
		return ErrRangeFormat
	}
	// 修订最大最小值
	if len(begin) == 0 {
		// 如果begin为空, 设置最小默认值
		this.setMinDefault()
	} else {
		this.min = num.AnyToFloat64(begin)
	}
	if len(end) == 0 {
		// 如果end为空, 设置最大默认值
		this.setMaxDefault()
	} else {
		this.max = num.AnyToFloat64(end)
	}
	if this.min > this.max {
		this.min, this.max = this.max, this.min
	}
	return nil
}

func (this NumberRange) MarshalText() (text []byte, err error) {
	str := this.String()
	return api.String2Bytes(str), nil
}

// UnmarshalYAML YAML自定义解析
func (this *NumberRange) UnmarshalYAML(node *yaml.Node) error {
	var value string
	if len(node.Content) == 0 {
		value = node.Value
	} else if len(node.Content) == 2 {
		value = node.Content[1].Value
	}

	return this.Parse(value)
}

// UnmarshalText 设置默认值调用
func (this *NumberRange) UnmarshalText(bytes []byte) error {
	text := api.Bytes2String(bytes)
	return this.Parse(text)
}

// Validate 验证
func (this *NumberRange) Validate(v float64) bool {
	if this.min == 0 && this.max == 0 {
		return true
	} else if v >= this.min && v < this.max {
		return true
	}
	return false
}
