package exchange

var (
	// A股指数列表
	aShareIndexList = []string{
		"sh000001", // 上证综合指数
		"sh000002", // 上证A股指数
		"sh000300", // 沪深300指数
		"sh000688", // 科创50指数
		"sh000905", // 中证500指数
		"sz399001", // 深证成份指数
		"sz399006", // 创业板指
		"sz399107", // 深证A指
		"sh880005", // 通达信板块-涨跌家数
		"sh510050", // 上证50ETF
		"sh510300", // 沪深300ETF
		"sh510900", // H股ETF
	}
)

// IndexList 指数列表
func IndexList() []string {
	return aShareIndexList
}
