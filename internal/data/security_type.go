package data

import "strings"

// SecurityAssetType 证券资产类型分类（统一证券分类体系的唯一权威定义）。
// 依据交易所代码段规则对证券编码进行分类，供选股宇宙、股票池、交易过滤、查询展示等全链路复用，
// 避免各处散落 ""/""/"" 的前缀判断造成口径不一致。
type SecurityAssetType string

const (
	// SecurityStockMain 沪深主板A股（沪600/601/603/605；深000/001/002/003）
	SecurityStockMain SecurityAssetType = "STOCK_MAIN"
	// SecurityStockSciTech 科创板（沪688）
	SecurityStockSciTech SecurityAssetType = "STOCK_SCI_TECH"
	// SecurityStockGrowth 创业板（深300/301）
	SecurityStockGrowth SecurityAssetType = "STOCK_GROWTH"
	// SecurityStockBJ 北交所（bj4/8/920 开头）
	SecurityStockBJ SecurityAssetType = "STOCK_BJ"
	// SecurityIndex 指数（沪sh000/sh880/sh881/sh899；深sz399）
	SecurityIndex SecurityAssetType = "INDEX"
	// SecurityETF ETF基金（沪5开头如sh51x/sh58x/sh59x；深sz159）
	SecurityETF SecurityAssetType = "ETF"
	// SecurityFund 场内基金/LOF/封基（深sz160/161/16x/15x分级；沪sh501/502/511/520/521 等非ETF基金）
	SecurityFund SecurityAssetType = "FUND"
	// SecurityConvertibleBond 可转债（沪sh11x；深sz12x）
	SecurityConvertibleBond SecurityAssetType = "CONVERTIBLE_BOND"
	// SecurityBond 债券（沪sh010/019；深sz10x 等）
	SecurityBond SecurityAssetType = "BOND"
	// SecurityBShare B股（沪sh900；深sz200）
	SecurityBShare SecurityAssetType = "B_SHARE"
	// SecurityOther 其他/未知
	SecurityOther SecurityAssetType = "OTHER"
)

// IsStock 是否为可交易的普通A股（主板/科创板/创业板/北交所）。
func (t SecurityAssetType) IsStock() bool {
	switch t {
	case SecurityStockMain, SecurityStockSciTech, SecurityStockGrowth, SecurityStockBJ:
		return true
	default:
		return false
	}
}

// IsSTName 判断股票名称是否为 ST / *ST 风险警示股。
// 单调判定规则：名称大写化后包含 "ST"，即可涵盖 "STxxx" 与 "*STxxx" 两种风险警示。
// A股规则下风险警示股禁止买入，供选股宇宙、交易执行、自动建仓全链路统一复用。
func IsSTName(name string) bool {
	return strings.Contains(strings.ToUpper(name), "ST")
}

// Label 返回分类的中文标签，便于查询与展示（不用于逻辑判定）。
func (t SecurityAssetType) Label() string {
	switch t {
	case SecurityStockMain:
		return "主板A股"
	case SecurityStockSciTech:
		return "科创板"
	case SecurityStockGrowth:
		return "创业板"
	case SecurityStockBJ:
		return "北交所"
	case SecurityIndex:
		return "指数"
	case SecurityETF:
		return "ETF基金"
	case SecurityFund:
		return "场内基金"
	case SecurityConvertibleBond:
		return "可转债"
	case SecurityBond:
		return "债券"
	case SecurityBShare:
		return "B股"
	default:
		return "其他"
	}
}

// ClassifySecurity 依据代码段规则对证券编码（带 sh/sz/bj 前缀）进行分类。
// 权威规则一览（参考真实市场代码段）：
//
//	沪 sh：
//	  - 600/601/603/605 → 主板A；688 → 科创板
//	  - 000/880/881/899 → 指数
//	  - 900 → B股
//	  - 110/111/113/118 → 可转债
//	  - 010/019/020 → 债券
//	  - 5 开头(51x/56x/58x/59x ETF；501/502/511/520 等基金) → ETF/基金
//	深 sz：
//	  - 000/001/002/003 → 主板A；300/301 → 创业板
//	  - 399 → 指数；200 → B股
//	  - 12x(120/123/125/126/127/128) → 可转债
//	  - 159 → ETF；160/161/162/163/165/168/169/15x → 场内基金
//	  - 10x(100/102/108/112) → 债券
//	北 bj：
//	  - 4/8/920 开头 → 北交所股票
//	  - 899 → 北证指数（北证50=899050、北证专精特新=899601 等）
//	  - 81 → 北交所向特定对象发行可转债（如 810011 优机定转）
func classifyBJ(code string) SecurityAssetType {
	switch {
	case strings.HasPrefix(code, "899"):
		return SecurityIndex
	case strings.HasPrefix(code, "81"):
		return SecurityConvertibleBond
	default:
		return SecurityStockBJ
	}
}
func ClassifySecurity(symbol string) SecurityAssetType {
	s := strings.ToLower(strings.TrimSpace(symbol))
	if len(s) < 7 { // 前缀(2) + 6位代码
		return SecurityOther
	}
	prefix := s[:2]
	code := s[2:]

	switch prefix {
	case "bj":
		return classifyBJ(code)
	case "sh":
		return classifySH(code)
	case "sz":
		return classifySZ(code)
	default:
		return SecurityOther
	}
}

func classifySH(code string) SecurityAssetType {
	switch {
	// 主板A
	case strings.HasPrefix(code, "600"),
		strings.HasPrefix(code, "601"),
		strings.HasPrefix(code, "603"),
		strings.HasPrefix(code, "605"):
		return SecurityStockMain
	// 科创板
	case strings.HasPrefix(code, "688"):
		return SecurityStockSciTech
	// 指数（综合/成份/板块/行业指数）
	case strings.HasPrefix(code, "000"),
		strings.HasPrefix(code, "880"),
		strings.HasPrefix(code, "881"),
		strings.HasPrefix(code, "899"):
		return SecurityIndex
	// B股
	case strings.HasPrefix(code, "900"):
		return SecurityBShare
	// 可转债
	case strings.HasPrefix(code, "110"),
		strings.HasPrefix(code, "111"),
		strings.HasPrefix(code, "113"),
		strings.HasPrefix(code, "118"):
		return SecurityConvertibleBond
	// 国债/企业债
	case strings.HasPrefix(code, "010"),
		strings.HasPrefix(code, "019"),
		strings.HasPrefix(code, "020"):
		return SecurityBond
	// 5 开头：ETF / 场内基金
	case strings.HasPrefix(code, "5"):
		return classifySHFund(code)
	default:
		return SecurityOther
	}
}

// classifySHFund 沪市 5 开头进一步区分 ETF 与一般场内基金（LOF/封基）。
func classifySHFund(code string) SecurityAssetType {
	// 常见 ETF 代码段
	if strings.HasPrefix(code, "508") || // REITs
		strings.HasPrefix(code, "510") ||
		strings.HasPrefix(code, "511") ||
		strings.HasPrefix(code, "512") ||
		strings.HasPrefix(code, "513") ||
		strings.HasPrefix(code, "515") ||
		strings.HasPrefix(code, "516") ||
		strings.HasPrefix(code, "517") ||
		strings.HasPrefix(code, "518") ||
		strings.HasPrefix(code, "560") ||
		strings.HasPrefix(code, "561") ||
		strings.HasPrefix(code, "562") ||
		strings.HasPrefix(code, "563") ||
		strings.HasPrefix(code, "588") ||
		strings.HasPrefix(code, "589") {
		return SecurityETF
	}
	return SecurityFund
}

func classifySZ(code string) SecurityAssetType {
	switch {
	// 主板A
	case strings.HasPrefix(code, "000"),
		strings.HasPrefix(code, "001"),
		strings.HasPrefix(code, "002"),
		strings.HasPrefix(code, "003"):
		return SecurityStockMain
	// 创业板
	case strings.HasPrefix(code, "300"),
		strings.HasPrefix(code, "301"):
		return SecurityStockGrowth
	// 指数
	case strings.HasPrefix(code, "399"):
		return SecurityIndex
	// B股
	case strings.HasPrefix(code, "200"):
		return SecurityBShare
	// 可转债（深市 12x）
	case strings.HasPrefix(code, "120"),
		strings.HasPrefix(code, "123"),
		strings.HasPrefix(code, "125"),
		strings.HasPrefix(code, "126"),
		strings.HasPrefix(code, "127"),
		strings.HasPrefix(code, "128"):
		return SecurityConvertibleBond
	// 债券
	case strings.HasPrefix(code, "100"),
		strings.HasPrefix(code, "102"),
		strings.HasPrefix(code, "108"),
		strings.HasPrefix(code, "112"):
		return SecurityBond
	// 基金：159 为 ETF；160/161/163/165/168/169/18x(部分)/15x 为场内基金
	case strings.HasPrefix(code, "159"):
		return SecurityETF
	case strings.HasPrefix(code, "160"),
		strings.HasPrefix(code, "161"),
		strings.HasPrefix(code, "162"),
		strings.HasPrefix(code, "163"),
		strings.HasPrefix(code, "164"),
		strings.HasPrefix(code, "165"),
		strings.HasPrefix(code, "166"),
		strings.HasPrefix(code, "167"),
		strings.HasPrefix(code, "168"),
		strings.HasPrefix(code, "169"),
		strings.HasPrefix(code, "150"),
		strings.HasPrefix(code, "151"),
		strings.HasPrefix(code, "152"),
		strings.HasPrefix(code, "153"):
		return SecurityFund
	default:
		return SecurityOther
	}
}
