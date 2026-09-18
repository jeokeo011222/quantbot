package sentiment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// cacheTTL 情绪分析结果缓存有效期
// 上市公司公告/新闻披露频率低，缓存 1 小时可避免同一选股周期内重复请求数据源
const cacheTTL = 1 * time.Hour

// providerTimeout 单个数据源请求超时
// 回退链最多 2 个数据源，单源 6 秒超时保证整体耗时可控
const providerTimeout = 6 * time.Second

// userAgent 统一浏览器 UA，避免部分数据源拒绝无 UA 请求
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// SentimentResult 单只股票的情绪分析结果
type SentimentResult struct {
	Code           string    `json:"code"`
	Name           string    `json:"name"`
	Score          float64   `json:"score"` // 0-1，>0.5 偏正面，<0.5 偏负面
	PositiveCount  int       `json:"positiveCount"`
	NegativeCount  int       `json:"negativeCount"`
	NeutralCount   int       `json:"neutralCount"`
	TotalCount     int       `json:"totalCount"`
	Period         string    `json:"period"`
	PositiveTitles []string  `json:"positiveTitles"`
	NegativeTitles []string  `json:"negativeTitles"`
	Source         string    `json:"source"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// cachedResult 带过期时间的缓存项
type cachedResult struct {
	result    SentimentResult
	expiresAt time.Time
}

// Engine 基于公开信息披露的情绪分析引擎（带缓存，线程安全）
// 数据源回退链：巨潮资讯网 → 东方财富（均为个股公告，个股相关性强）
type Engine struct {
	client *http.Client
	cache  map[string]cachedResult
	mu     sync.RWMutex
}

// NewEngine 创建情绪分析引擎
func NewEngine() *Engine {
	return &Engine{
		client: &http.Client{Timeout: 8 * time.Second},
		cache:  make(map[string]cachedResult),
	}
}

// Score 获取股票情绪得分（缓存未过期时直接返回）
// code 支持带交易所前缀（如 sh600519）或纯数字代码（如 600519）
func (e *Engine) Score(code string) (SentimentResult, error) {
	return e.ScoreWithContext(context.Background(), code)
}

// ScoreWithContext 获取股票情绪得分，支持外部取消/超时
func (e *Engine) ScoreWithContext(ctx context.Context, code string) (SentimentResult, error) {
	normalized := normalizeCode(code)
	if normalized == "" {
		return SentimentResult{}, fmt.Errorf("无效的股票代码: %s", code)
	}

	e.mu.RLock()
	if c, ok := e.cache[normalized]; ok && time.Now().Before(c.expiresAt) {
		e.mu.RUnlock()
		return c.result, nil
	}
	e.mu.RUnlock()

	result, err := e.fetchAndScore(ctx, normalized)
	if err != nil {
		return SentimentResult{}, err
	}

	e.mu.Lock()
	e.cache[normalized] = cachedResult{result: result, expiresAt: time.Now().Add(cacheTTL)}
	e.mu.Unlock()
	return result, nil
}

// InvalidateCache 清空缓存（数据源更新后调用）
func (e *Engine) InvalidateCache() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache = make(map[string]cachedResult)
}

// fetchAndScore 按回退链依次尝试数据源：巨潮 → 东方财富
// 当前数据源不可用（网络失败/非200/解析失败）时自动回退下一个，全部失败才报错
func (e *Engine) fetchAndScore(ctx context.Context, code string) (SentimentResult, error) {
	var lastErr error
	for _, p := range providers {
		pctx, cancel := context.WithTimeout(ctx, providerTimeout)
		titles, err := p.fetchTitles(pctx, e.client, code)
		cancel()
		if err == nil {
			return e.scoreTitles(code, p.name(), titles), nil
		}
		lastErr = err
		log.Printf("[Sentiment] 数据源 %s 不可用，尝试回退: %v", p.name(), err)
	}
	return SentimentResult{}, fmt.Errorf("所有情绪数据源均不可用: %w", lastErr)
}

// scoreTitles 对标题列表做关键词情绪分类并计算得分
func (e *Engine) scoreTitles(code, source string, titles []string) SentimentResult {
	endDate := time.Now().Format("2006-01-02")
	startDate := time.Now().AddDate(0, 0, -30).Format("2006-01-02")

	result := SentimentResult{
		Code:      code,
		Period:    fmt.Sprintf("%s ~ %s", startDate, endDate),
		Source:    source,
		UpdatedAt: time.Now(),
	}

	for _, title := range titles {
		title = cleanHTMLTags(title)
		if title == "" {
			continue
		}
		switch classify(title) {
		case positive:
			result.PositiveCount++
			if len(result.PositiveTitles) < 5 {
				result.PositiveTitles = append(result.PositiveTitles, title)
			}
		case negative:
			result.NegativeCount++
			if len(result.NegativeTitles) < 5 {
				result.NegativeTitles = append(result.NegativeTitles, title)
			}
		default:
			result.NeutralCount++
		}
	}

	result.TotalCount = result.PositiveCount + result.NegativeCount + result.NeutralCount
	result.Score = computeScore(result.PositiveCount, result.NegativeCount, result.TotalCount)
	return result
}

// ==================== 数据源回退链 ====================

// sentimentProvider 情绪数据源接口
type sentimentProvider interface {
	name() string
	fetchTitles(ctx context.Context, client *http.Client, code string) ([]string, error)
}

// providers 数据源回退链（巨潮优先，东方财富兜底）
// 两者均为个股公告数据源；同花顺/新浪的新闻接口返回全市场新闻、个股相关性差，已排除
var providers = []sentimentProvider{
	cninfoProvider{},
	eastmoneyProvider{},
}

// cninfoProvider 巨潮资讯网公告数据源（主数据源）
type cninfoProvider struct{}

func (cninfoProvider) name() string { return "巨潮资讯网 (www.cninfo.com.cn)" }

func (cninfoProvider) fetchTitles(ctx context.Context, client *http.Client, code string) ([]string, error) {
	exchange := inferExchange(code)
	endDate := time.Now().Format("2006-01-02")
	startDate := time.Now().AddDate(0, 0, -30).Format("2006-01-02")

	// 巨潮 stock 参数格式：code,gs{exchange}0{code}
	orgID := fmt.Sprintf("gs%s0%s", exchange, code)
	form := url.Values{}
	form.Set("pageNum", "1")
	form.Set("pageSize", "30")
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://www.cninfo.com.cn/new/hisAnnouncement/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://www.cninfo.com.cn/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("巨潮资讯网请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("巨潮资讯网返回异常状态码: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data struct {
		Announcements []struct {
			AnnouncementTitle string `json:"announcementTitle"`
		} `json:"announcements"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("解析巨潮资讯网公告响应失败: %w", err)
	}

	titles := make([]string, 0, len(data.Announcements))
	for _, a := range data.Announcements {
		if a.AnnouncementTitle != "" {
			titles = append(titles, a.AnnouncementTitle)
		}
	}
	return titles, nil
}

// eastmoneyProvider 东方财富公告数据源（回退源1）
type eastmoneyProvider struct{}

func (eastmoneyProvider) name() string { return "东方财富 (eastmoney.com)" }

func (eastmoneyProvider) fetchTitles(ctx context.Context, client *http.Client, code string) ([]string, error) {
	apiURL := fmt.Sprintf("https://np-anotice-stock.eastmoney.com/api/security/ann?sr=-1&page_size=30&page_index=1&ann_type=A&client_source=web&stock_list=%s&f_node=0&s_node=0", code)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://data.eastmoney.com/")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("东方财富请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("东方财富返回异常状态码: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data struct {
		Data struct {
			List []struct {
				Title string `json:"title"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("解析东方财富公告响应失败: %w", err)
	}

	titles := make([]string, 0, len(data.Data.List))
	for _, item := range data.Data.List {
		if item.Title != "" {
			titles = append(titles, item.Title)
		}
	}
	return titles, nil
}

// ==================== 情绪分类规则 ====================

type sentimentClass int

const (
	neutral sentimentClass = iota
	positive
	negative
)

// positiveKeywords 利好关键词（公告/新闻标题子串匹配）
var positiveKeywords = []string{
	"业绩预增", "业绩增长", "业绩大增", "业绩快报", "净利润增长", "净利润同比", "扭亏", "预盈", "盈利",
	"回购", "增持", "股权激励", "员工持股",
	"中标", "签订合同", "重大合同", "战略合作", "合作协议",
	"分红", "派息", "送转", "高送转",
	"解除质押", "解除冻结",
	"获批", "核准", "增资", "收购", "重组", "并购", "超预期", "提价", "涨价",
}

// negativeKeywords 利空关键词（公告/新闻标题子串匹配）
var negativeKeywords = []string{
	"业绩预亏", "业绩预减", "业绩下滑", "业绩下降", "亏损", "净利润下降", "由盈转亏",
	"减持", "质押", "冻结", "诉讼", "仲裁", "处罚", "违规", "立案", "调查", "警示", "问询", "监管函",
	"退市", "终止", "破产", "清算", "风险提示", "商誉减值", "计提减值",
	"担保", "逾期", "违约", "债务", "资金占用", "非标", "保留意见",
	"下调", "降价", "停产", "事故", "召回",
}

func classify(title string) sentimentClass {
	t := strings.ToLower(title)
	for _, kw := range negativeKeywords {
		if strings.Contains(t, strings.ToLower(kw)) {
			return negative
		}
	}
	for _, kw := range positiveKeywords {
		if strings.Contains(t, strings.ToLower(kw)) {
			return positive
		}
	}
	return neutral
}

// computeScore 计算情绪得分（0-1）
// 无公告时返回中性 0.5；得分 = 0.5 + (正面-负面) / (2×总数)
func computeScore(positiveCount, negativeCount, totalCount int) float64 {
	if totalCount <= 0 {
		return 0.5
	}
	s := 0.5 + float64(positiveCount-negativeCount)/(2.0*float64(totalCount))
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

// normalizeCode 规范化股票代码（去除交易所前缀/后缀，仅保留数字）
func normalizeCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.TrimPrefix(code, "sh")
	code = strings.TrimPrefix(code, "sz")
	code = strings.TrimPrefix(code, "bj")
	code = strings.TrimSuffix(code, ".sh")
	code = strings.TrimSuffix(code, ".sz")
	code = strings.TrimSuffix(code, ".bj")
	re := regexp.MustCompile(`\D`)
	return re.ReplaceAllString(code, "")
}

// inferExchange 根据股票代码推断交易所
func inferExchange(code string) string {
	if code == "" {
		return ""
	}
	switch code[0] {
	case '6', '9':
		return "sh"
	case '0', '3', '2':
		return "sz"
	case '4', '8':
		return "bj"
	default:
		return "sh"
	}
}

// cleanHTMLTags 去除公告/新闻标题中的 HTML 标签（如 <em>）
func cleanHTMLTags(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
}
