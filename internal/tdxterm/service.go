package tdxterm

import (
	"fmt"
	"strings"
	"time"
)

// Service 通达信「终端接口」高层服务，封装对 Client 的常用数据请求。
type Service struct {
	client *Client
}

// NewService 创建终端接口服务。dllDir 为 TPythClient.dll 所在目录（通达信安装目录），
// initPath 为 InitConnect 的 file_name 标识（建议传唯一脚本名）。连接为惰性建立，
// 首次调用数据接口时自动握手并获取 run_id。
func NewService(dllDir, initPath string) (*Service, error) {
	if err := loadDLL(dllDir); err != nil {
		return nil, err
	}
	if initPath == "" {
		initPath = "QuantBot"
	}
	return &Service{client: NewClient(dllDir, initPath)}, nil
}

// Close 关闭连接。
func (s *Service) Close() {
	if s.client != nil {
		s.client.Close()
	}
}

// Connected 是否已连接。
func (s *Service) Connected() bool {
	return s.client != nil && s.client.Connected()
}

// GetMarketData 获取 K 线行情。stockList 为证券代码列表（如 "600000.SH"/"000001.SZ"），
// period 支持 1m/5m/15m/30m/1h/1d/1w/1mon 等。count>0 时按最近 count 根取数，否则按起止时间。
// 返回 {股票代码: {字段名: 数值数组}} 结构。
func (s *Service) GetMarketData(stockList, fieldList []string, period, startTime, endTime string, count int, dividendType string) (map[string]any, error) {
	if len(stockList) == 0 {
		return nil, fmt.Errorf("必传参数缺失：stock_list不能为空")
	}
	if period == "" {
		return nil, fmt.Errorf("必传参数缺失：period不能为空")
	}
	if dividendType == "" {
		dividendType = "none"
	}
	if endTime == "" {
		endTime = time.Now().Format("20060102150405")
	}

	startFmt := convertTimeFormat(startTime)
	endFmt := convertTimeFormat(endTime)
	if count > 0 {
		startFmt = ""
	}

	payload := map[string]any{
		"type":             1,
		"stock_list":       stockList, // 按通达信格式传"600000.SH"，勿转成 1#600000（终端 type=1 无法识别）
		"start_time":       startFmt,
		"end_time":         endFmt,
		"period":           period,
		"dividend_type":    dividendType,
		"count":            count,
		"stock_page_index": 0,
		"stock_page_size":  100,
	}

	data, err := s.client.callKlineAllPages(procGetTdxDataStr, payload, 600000)
	if err != nil {
		// 连接过期（ErrorId 6/7）时 Client 已自动标记重连，下一次调用自动握手
		return nil, err
	}
	if getErrID(data) != "0" {
		return nil, fmt.Errorf("获取K线失败: %v", data["Error"])
	}
	value, _ := data["Value"].(map[string]any)

	// 可选字段筛选（不区分大小写）
	if len(fieldList) > 0 {
		upper := map[string]string{}
		for f := range value {
			upper[lowerKey(f)] = f
		}
		sel := map[string]any{}
		for _, f := range fieldList {
			lf := lowerKey(f)
			if orig, ok := upper[lf]; ok {
				sel[orig] = value[orig]
			}
		}
		return sel, nil
	}
	return value, nil
}

// GetStockInfo 获取个股基础财务详情。
func (s *Service) GetStockInfo(code string) (map[string]any, error) {
	data, err := s.client.call(procGetTdxDataStr, map[string]any{
		"type":       2,
		"stock_code": normalizeCode(code),
	}, 10000)
	if err != nil {
		return nil, err
	}
	if getErrID(data) != "0" {
		return nil, fmt.Errorf("获取个股详情失败: %v", data["Error"])
	}
	return data, nil
}

// GetMarketSnapshot 获取市场快照（实时行情）。
func (s *Service) GetMarketSnapshot(code string) (map[string]any, error) {
	data, err := s.client.call(procGetTdxDataStr, map[string]any{
		"type":       3,
		"stock_code": normalizeCode(code),
	}, 60000)
	if err != nil {
		return nil, err
	}
	if getErrID(data) != "0" {
		return nil, fmt.Errorf("获取市场快照失败: %v", data["Error"])
	}
	return data, nil
}

// GetSectorList 获取板块列表。
func (s *Service) GetSectorList(listType int) ([]any, error) {
	data, err := s.client.call(procGetTdxDataStr, map[string]any{
		"type":      5,
		"list_type": listType,
	}, 5000)
	if err != nil {
		return nil, err
	}
	if getErrID(data) != "0" {
		return nil, fmt.Errorf("获取板块列表失败: %v", data["Error"])
	}
	return asList(data["Value"]), nil
}

// GetStockListInSector 获取板块成分股。
func (s *Service) GetStockListInSector(blockCode string, blockType, listType int) ([]any, error) {
	if blockType == 1 {
		blockCode = "BKCODE." + blockCode
	}
	if blockType == 2 {
		blockCode = "QH." + blockCode
	}
	data, err := s.client.call(procGetTdxDataStr, map[string]any{
		"type":       6,
		"block_code": blockCode,
		"block_type": blockType,
		"list_type":  listType,
	}, 5000)
	if err != nil {
		return nil, err
	}
	if getErrID(data) != "0" {
		return nil, fmt.Errorf("获取板块成分股失败: %v", data["Error"])
	}
	return asList(data["Value"]), nil
}

// GetStockList 获取股票列表。
func (s *Service) GetStockList(market string, listType int) ([]any, error) {
	if market == "" {
		market = "5"
	}
	data, err := s.client.call(procGetTdxDataStr, map[string]any{
		"type":      0,
		"market":    market,
		"list_type": listType,
	}, 60000)
	if err != nil {
		return nil, err
	}
	if getErrID(data) != "0" {
		return nil, fmt.Errorf("获取股票列表失败: %v", data["Error"])
	}
	return asList(data["Value"]), nil
}

// GetTradingDates 获取交易日历。
func (s *Service) GetTradingDates(market, startTime, endTime string, count int) ([]any, error) {
	if endTime == "" {
		endTime = time.Now().Format("20060102150405")
	}
	if count == 0 {
		count = -1
	}
	data, err := s.client.call(procGetTdxDataStr, map[string]any{
		"type":       12,
		"market":     market,
		"start_time": convertTimeFormat(startTime),
		"end_time":   convertTimeFormat(endTime),
		"count":      count,
	}, 5000)
	if err != nil {
		return nil, err
	}
	if getErrID(data) != "0" {
		return nil, fmt.Errorf("获取交易日历失败: %v", data["Error"])
	}
	return asList(data["Date"]), nil
}

// GetFinancialData 获取专业财务数据。
// stockList 为证券代码列表（点号格式，如 "600519.SH"/"000001.SZ"，勿转成 1# 格式），
// fieldList 为字段筛选（FN 编码，如 []string{"FN6","FN230","FN232"}），不能为空。
// tableList 为财务数据表名（如 "Finance" 财务 / "Capital" 股本），默认 "Finance"。
// startTime/endTime 格式 YYYYMMDD（startTime 必填）；reportType 合法值 'announce_time'(按公告日期)
// 或 'tag_time'(按报告期)，默认 'tag_time'。注意：需要先在通达信客户端中下载专业财务数据。
func (s *Service) GetFinancialData(stockList, fieldList []string, startTime, endTime, reportType string) (map[string]any, error) {
	if len(stockList) == 0 {
		return nil, fmt.Errorf("必传参数缺失：stock_list不能为空")
	}
	if len(fieldList) == 0 {
		return nil, fmt.Errorf("必传参数缺失：field_list不能为空")
	}
	if reportType == "" {
		reportType = "tag_time"
	}
	payload := map[string]any{
		"type":        1,
		"stock_list":  stockList,           // 通达信专业财务接口用点号格式，type=1 无法识别 1# 格式
		"table_list":  []string{"Finance"}, // 财务数据表名（get_financial_data 用 table_list 指定表）
		"field_list":  fieldList,
		"start_time":  startTime, // 财务接口要求紧凑 YYYYMMDD（勿转连字符，否则 DLL 报 stime error）
		"end_time":    endTime,
		"report_type": reportType,
	}
	data, err := s.client.callProDataAllPages(procGetProData, payload, 600000)
	if err != nil {
		return nil, err
	}
	if getErrID(data) != "0" {
		return nil, fmt.Errorf("获取财务数据失败: %v", data["Error"])
	}
	value, _ := data["Value"].(map[string]any)
	return value, nil
}

// ============ 内部辅助 ============

// getErrID 返回返回包中的 ErrorId 字符串。
func getErrID(obj map[string]any) string {
	return toString(obj["ErrorId"])
}

// asList 将 any 安全转为 []any。
func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

// lowerKey 转小写（用于字段名大小写不敏感匹配）。
func lowerKey(s string) string {
	return strings.ToLower(s)
}
