package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ==================== CninfoInfoTool ====================

// CninfoInfoTool 巨潮资讯网信息工具
// 巨潮资讯网（www.cninfo.com.cn）是中国证监会指定的上市公司信息披露网站，
// 平台提供上市公司公告、公司资讯、公司互动、股东大会网络投票等内容，一站式服务资本市场投资者。
// 本工具供智能体了解国内上市公司信息披露情况，辅助市场研究与风险判断。
type CninfoInfoTool struct {
	client *http.Client
}

// NewCninfoInfoTool 创建巨潮资讯网信息工具
func NewCninfoInfoTool() *CninfoInfoTool {
	return &CninfoInfoTool{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *CninfoInfoTool) Name() string {
	return "get_cninfo_info"
}

func (t *CninfoInfoTool) Description() string {
	return "从巨潮资讯网（中国证监会指定上市公司信息披露平台 www.cninfo.com.cn）查询A股上市公司的公告、资讯等信息，返回公告标题、发布日期、重要程度和公告原文链接，用于了解上市公司经营动态与基本面变化"
}

func (t *CninfoInfoTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"code": map[string]interface{}{
						"type":        "string",
						"description": "股票代码，如 600519（贵州茅台）、000001（平安银行）、300750（宁德时代）",
					},
					"exchange": map[string]interface{}{
						"type":        "string",
						"description": "交易所：sh(上海)、sz(深圳)、bj(北京)。可留空，系统根据股票代码自动识别",
						"enum":        []string{"sh", "sz", "bj", ""},
					},
					"type": map[string]interface{}{
						"type":        "string",
						"description": "查询类型：announcement(个股公告，默认，需提供code)、search(全市场公告关键词检索，需提供keyword)",
						"enum":        []string{"announcement", "search"},
					},
					"keyword": map[string]interface{}{
						"type":        "string",
						"description": "检索关键词（type=search时使用），如：回购、业绩预告、分红、减持、重大事项等",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "最多返回多少条，默认10，最大30",
					},
					"days": map[string]interface{}{
						"type":        "integer",
						"description": "回看最近多少天的信息，默认30天",
					},
				},
				"required": []string{},
			},
		},
	}
}

// cninfoAnnouncement 巨潮公告结构
type cninfoAnnouncement struct {
	SecCode              string `json:"secCode"`
	SecName              string `json:"secName"`
	OrgID                string `json:"orgId"`
	AnnouncementID       string `json:"announcementId"`
	AnnouncementTitle    string `json:"announcementTitle"`
	AnnouncementTime     int64  `json:"announcementTime"`
	AdjunctURL           string `json:"adjunctUrl"`
	AnnouncementType     string `json:"announcementType"`
	Important            *int   `json:"important"`
	AnnouncementTypeName string `json:"announcementTypeName"`
}

// cninfoQueryResponse 巨潮公告查询响应
type cninfoQueryResponse struct {
	TotalAnnouncement int                  `json:"totalAnnouncement"`
	TotalRecordNum    int                  `json:"totalRecordNum"`
	Announcements     []cninfoAnnouncement `json:"announcements"`
}

func (t *CninfoInfoTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	queryType := "announcement"
	if qt, ok := args["type"].(string); ok && qt != "" {
		queryType = qt
	}

	limit := 10
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
		if limit > 30 {
			limit = 30
		}
	}

	days := 30
	if d, ok := args["days"].(float64); ok && d > 0 {
		days = int(d)
	}

	switch queryType {
	case "search":
		keyword, _ := args["keyword"].(string)
		if keyword == "" {
			return nil, fmt.Errorf("type=search 时需要提供 keyword 检索关键词")
		}
		return t.searchAnnouncements(ctx, keyword, limit, days)
	default:
		code, _ := args["code"].(string)
		if code == "" {
			return nil, fmt.Errorf("type=announcement 时需要提供 code 股票代码")
		}
		exchange, _ := args["exchange"].(string)
		return t.queryAnnouncements(ctx, code, exchange, limit, days)
	}
}

// queryAnnouncements 查询指定股票的公告
func (t *CninfoInfoTool) queryAnnouncements(ctx context.Context, code, exchange string, limit, days int) (map[string]interface{}, error) {
	code = normalizeStockCode(code)
	if code == "" {
		return nil, fmt.Errorf("无效的股票代码")
	}
	if exchange == "" {
		exchange = inferExchange(code)
	}

	endDate := time.Now().Format("2006-01-02")
	startDate := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	// 巨潮 stock 参数格式：code,gs{exchange}0{code}
	orgID := fmt.Sprintf("gs%s0%s", exchange, code)

	form := url.Values{}
	form.Set("pageNum", "1")
	form.Set("pageSize", fmt.Sprintf("%d", limit))
	form.Set("column", "szse")
	form.Set("tabName", "fulltext")
	form.Set("plate", "")
	form.Set("stock", fmt.Sprintf("%s,%s", code, orgID))
	form.Set("searchkey", "")
	form.Set("secid", "")
	form.Set("category", "")
	form.Set("trade", "")
	form.Set("seDate", fmt.Sprintf("%s~%s", startDate, endDate))
	form.Set("sortName", "")
	form.Set("sortType", "")
	form.Set("isHLtitle", "true")

	body, err := t.postForm(ctx, "https://www.cninfo.com.cn/new/hisAnnouncement/query", form)
	if err != nil {
		return nil, err
	}

	var result cninfoQueryResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析巨潮资讯网公告响应失败: %w", err)
	}

	items := make([]map[string]interface{}, 0, len(result.Announcements))
	for _, a := range result.Announcements {
		items = append(items, t.buildAnnouncementItem(a))
	}

	return map[string]interface{}{
		"source":        "巨潮资讯网 (www.cninfo.com.cn)",
		"queryType":     "个股公告",
		"code":          code,
		"exchange":      exchange,
		"total":         result.TotalAnnouncement,
		"period":        fmt.Sprintf("%s ~ %s", startDate, endDate),
		"announcements": items,
	}, nil
}

// searchAnnouncements 按关键词全市场检索公告
func (t *CninfoInfoTool) searchAnnouncements(ctx context.Context, keyword string, limit, days int) (map[string]interface{}, error) {
	endDate := time.Now().Format("2006-01-02")
	startDate := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	form := url.Values{}
	form.Set("searchkey", keyword)
	form.Set("sdate", startDate)
	form.Set("edate", endDate)
	form.Set("isfulltext", "false")
	form.Set("sortName", "pubdate")
	form.Set("sortType", "desc")
	form.Set("pageNum", "1")

	body, err := t.postForm(ctx, "https://www.cninfo.com.cn/new/fulltextSearch/full", form)
	if err != nil {
		return nil, err
	}

	var result cninfoQueryResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析巨潮资讯网检索响应失败: %w", err)
	}

	items := make([]map[string]interface{}, 0, len(result.Announcements))
	for _, a := range result.Announcements {
		if len(items) >= limit {
			break
		}
		items = append(items, t.buildAnnouncementItem(a))
	}

	return map[string]interface{}{
		"source":        "巨潮资讯网 (www.cninfo.com.cn)",
		"queryType":     "关键词检索",
		"keyword":       keyword,
		"total":         result.TotalAnnouncement,
		"period":        fmt.Sprintf("%s ~ %s", startDate, endDate),
		"announcements": items,
	}, nil
}

// postForm 发送表单请求
func (t *CninfoInfoTool) postForm(ctx context.Context, apiURL string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://www.cninfo.com.cn/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("巨潮资讯网请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("巨潮资讯网返回异常状态码: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// buildAnnouncementItem 构建公告返回项
func (t *CninfoInfoTool) buildAnnouncementItem(a cninfoAnnouncement) map[string]interface{} {
	item := map[string]interface{}{
		"title": cleanHTMLTags(a.AnnouncementTitle),
		"code":  a.SecCode,
		"name":  a.SecName,
		"date":  time.UnixMilli(a.AnnouncementTime).Format("2006-01-02"),
	}
	if a.AnnouncementTypeName != "" {
		item["type"] = a.AnnouncementTypeName
	} else {
		item["type"] = a.AnnouncementType
	}
	if a.Important != nil && *a.Important == 1 {
		item["isImportant"] = true
	} else {
		item["isImportant"] = false
	}
	if a.AdjunctURL != "" {
		item["pdfUrl"] = fmt.Sprintf("https://static.cninfo.com.cn/%s", a.AdjunctURL)
	}
	return item
}

// normalizeStockCode 规范化股票代码（去除空格、字母前缀等）
func normalizeStockCode(code string) string {
	code = strings.TrimSpace(code)
	code = strings.ToLower(code)
	// 去除 sh/sz/bj 等交易所前缀
	code = strings.TrimPrefix(code, "sh")
	code = strings.TrimPrefix(code, "sz")
	code = strings.TrimPrefix(code, "bj")
	// 去除 .SH/.SZ/.BJ 后缀
	code = strings.TrimSuffix(code, ".sh")
	code = strings.TrimSuffix(code, ".sz")
	code = strings.TrimSuffix(code, ".bj")
	// 仅保留数字
	re := regexp.MustCompile(`\D`)
	code = re.ReplaceAllString(code, "")
	return code
}

// inferExchange 根据股票代码推断交易所
func inferExchange(code string) string {
	if code == "" {
		return ""
	}
	switch code[0] {
	case '6', '9':
		return "sh" // 上海：600/601/603/605/688(科创板)/689
	case '0', '3':
		return "sz" // 深圳：000/001/002/003(主板)、300/301(创业板)
	case '4', '8':
		return "bj" // 北京（北交所）：43x/83x/87x
	case '2':
		return "sz" // 深市B股 200
	default:
		return "sh"
	}
}

// cleanHTMLTags 去除公告标题中的 HTML 标签（如 <em>）
func cleanHTMLTags(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
}
