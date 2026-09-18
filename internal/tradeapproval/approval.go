package tradeapproval

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// ApprovalResult 用户对交易的确认结果
type ApprovalResult string

const (
	// ResultApproved 用户批准交易
	ResultApproved ApprovalResult = "approved"
	// ResultRejected 用户拒绝交易
	ResultRejected ApprovalResult = "rejected"
	// ResultPending 用户未及时确认（超时未决）：视为订单排队中，不判定失败；
	// 记录保留，用户稍后仍可确认/拒绝，批准后将补执行真实交易。
	ResultPending ApprovalResult = "pending"
	// ResultFailed 交易日 15:00 收盘仍未获得用户确认：交易失败。
	ResultFailed ApprovalResult = "failed"
)

// closeHour 收盘截止时间（交易日 15:00，本地时区）
const closeHour = 15

// Executor 补确认执行器：超时未决的交易被用户批准后，由系统据此执行真实交易。
type Executor func(decisionID, action, symbol, stockName, market string, quantity int, price float64, reason string) error

// PendingPersist 待确认订单持久化 + 资金占用钩子（由 App 注入，写入临时表 orders 并预留买入资金）。
type PendingPersist struct {
	// OnPending 订单创建：写临时表 + 买入占用资金；返回错误表示资金超占用，订单不可创建。
	OnPending func(pt *PendingTrade) error
	// OnResolve 订单结局：executed=true 表示已成交（orders 置 filled）；否则取消/失败并释放占用的买入资金。
	OnResolve func(pt *PendingTrade, executed bool)
}

// PendingTrade 待确认交易
type PendingTrade struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"` // BUY / SELL
	Symbol     string    `json:"symbol"`
	StockName  string    `json:"stock_name"`
	Market     string    `json:"market"`
	Quantity   int       `json:"quantity"`
	Price      float64   `json:"price"`
	Amount     float64   `json:"amount"`
	Reason     string    `json:"reason"`
	DecisionID string    `json:"decision_id"`
	CreatedAt  time.Time `json:"created_at"`
	Status     string    `json:"status"` // pending / approved / rejected / executed / expired

	resultCh chan bool
	returned bool // RequestApproval 是否已超时返回（此后确认走补执行路径）
}

// Service 交易审批服务
// 模拟接口模式下，智能体决定交易时需等待用户手动确认。
// 用户未及时确认（超时）不视为失败：订单保持「排队中」，可稍后确认/拒绝，
// 批准后由 Executor 补执行真实交易。
type Service struct {
	mu       sync.Mutex
	pending  map[string]*PendingTrade
	emitFn   func(eventName string, data interface{})
	enabled  bool
	timeout  time.Duration
	executor Executor // 超时未决交易补确认（批准）后的执行器

	// persist 待确认订单持久化 + 资金占用钩子（App 注入）
	persist *PendingPersist
}

// NewService 创建交易审批服务
func NewService() *Service {
	return &Service{
		pending: make(map[string]*PendingTrade),
		timeout: 120 * time.Second,
	}
}

// SetEmitFn 设置事件发送函数（Wails EventsEmit）
func (s *Service) SetEmitFn(fn func(eventName string, data interface{})) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitFn = fn
}

// SetEnabled 设置是否启用手动确认（模拟接口模式启用）
func (s *Service) SetEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = enabled
	log.Printf("[TradeApproval] Manual trade confirmation %s", map[bool]string{true: "enabled", false: "disabled"}[enabled])
}

// IsEnabled 是否启用手动确认
func (s *Service) IsEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetTimeout 设置确认超时
func (s *Service) SetTimeout(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeout = d
}

// SetExecutor 设置补确认执行器（用户超时未决后补点「批准」时执行真实交易）
func (s *Service) SetExecutor(fn Executor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executor = fn
}

// SetPersist 设置待确认订单持久化 + 资金占用钩子。
// 模拟接口模式下，请求确认时由 OnPending 写入 dates 临时表 orders 并占用买入资金；
// 订单结局（成交/取消/失败）由 OnResolve 更新该临时表并释放资金。
func (s *Service) SetPersist(p *PendingPersist) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persist = p
}

// AddRecovered 挂回一条从 orders 表恢复的待确认订单（重启恢复用）。
// 恢复的订单置 returned=true，走补确认路径（用户可确认/拒绝）。
func (s *Service) AddRecovered(pt *PendingTrade) {
	s.mu.Lock()
	pt.returned = true
	if pt.Status == "" {
		pt.Status = "pending"
	}
	s.pending[pt.ID] = pt
	s.mu.Unlock()
}

// CancelFilled 将一笔已按「成交」处理(order 置 filled)但因执行实际失败需要逆转的订单
// 标记为失败并释放其占用的买入资金。幂等：可重复调用无副作用。
func (s *Service) CancelFilled(pt *PendingTrade) {
	if pt == nil {
		return
	}
	s.finalize(pt, false)
}

// RequestApproval 请求交易确认，阻塞直到用户确认或超时。
// 返回：
//   - ResultApproved：用户批准，可执行交易
//   - ResultRejected：用户拒绝，交易取消
//   - ResultPending：用户未及时确认（超时），订单保持排队中，不判定失败；
//     记录保留，用户稍后确认（批准/拒绝）后按结果处理（批准将补执行）。
func (s *Service) RequestApproval(action, symbol, stockName, market string, quantity int, price float64, reason, decisionID string) (ApprovalResult, *PendingTrade, error) {
	now := time.Now()
	todayClose := time.Date(now.Year(), now.Month(), now.Day(), closeHour, 0, 0, 0, now.Location())
	// 已过 15:00 收盘：未获得确认信号，交易直接判定失败，不再发起确认弹窗
	if !now.Before(todayClose) {
		log.Printf("[TradeApproval] 已过收盘时间(%d:00)，交易 %s %s 判定失败", closeHour, action, symbol)
		return ResultFailed, nil, nil
	}

	s.mu.Lock()
	if !s.enabled {
		s.mu.Unlock()
		return ResultApproved, nil, nil // 未启用手动确认，直接放行
	}

	id := fmt.Sprintf("ta_%d", time.Now().UnixNano())
	pt := &PendingTrade{
		ID:         id,
		Action:     action,
		Symbol:     symbol,
		StockName:  stockName,
		Market:     market,
		Quantity:   quantity,
		Price:      price,
		Amount:     float64(quantity) * price,
		Reason:     reason,
		DecisionID: decisionID,
		CreatedAt:  time.Now(),
		Status:     "pending",
		resultCh:   make(chan bool, 1),
	}
	s.pending[id] = pt
	emit := s.emitFn
	timeout := s.timeout
	persist := s.persist
	s.mu.Unlock()

	// 持久化待确认订单 + 买入占用资金（模拟接口模式）。资金超占用（返回 err）时订单判定失败。
	if persist != nil && persist.OnPending != nil {
		if err := persist.OnPending(pt); err != nil {
			log.Printf("[TradeApproval] 待确认订单创建失败(资金超占用): %s %s, 判定失败: %v", action, symbol, err)
			s.mu.Lock()
			pt.Status = "failed"
			delete(s.pending, id)
			s.mu.Unlock()
			return ResultFailed, nil, nil
		}
	}

	log.Printf("[TradeApproval] Requesting manual confirmation: %s %s %d@%.2f (id=%s)", action, symbol, quantity, price, id)
	if emit != nil {
		emit("trade:approval_request", pt)
	}

	// 到 15:00 收盘的定时器：收盘仍未确认则判定交易失败
	closeCh := time.After(todayClose.Sub(now))

	select {
	case approved := <-pt.resultCh:
		s.mu.Lock()
		if approved {
			pt.Status = "approved"
		} else {
			pt.Status = "rejected"
		}
		delete(s.pending, id)
		s.mu.Unlock()
		log.Printf("[TradeApproval] Trade %s confirmed: approved=%v", id, approved)
		// 批准视为成交（orders 置 filled，释放占用）；若调用方执行实际失败将走 CancelFilled 逆转。
		if approved {
			s.finalize(pt, true)
		} else {
			s.finalize(pt, false)
		}
		return resultOf(approved), pt, nil
	case <-time.After(timeout):
		s.mu.Lock()
		pt.returned = true
		// 补偿极罕见竞态：Confirm 恰在超时瞬间已推送批准/拒绝（resultCh 已有值），
		// 读走并返回正确结果，避免「已确认但未执行」。
		select {
		case approved := <-pt.resultCh:
			delete(s.pending, id)
			s.mu.Unlock()
			log.Printf("[TradeApproval] Trade %s confirmed right after timeout: approved=%v", id, approved)
			if approved {
				s.finalize(pt, true)
			} else {
				s.finalize(pt, false)
			}
			return resultOf(approved), pt, nil
		default:
		}
		// 超时未决：保留记录，订单保持排队中，等待用户补确认（不判定失败）
		pt.Status = "pending"
		s.mu.Unlock()
		log.Printf("[TradeApproval] Trade %s 用户未及时确认(排队中)，保留待确认: %s %s %d@%.2f", id, action, symbol, quantity, price)
		return ResultPending, pt, nil
	case <-closeCh:
		// 15:00 收盘仍未获得用户确认：交易失败
		s.mu.Lock()
		pt.Status = "failed"
		delete(s.pending, id)
		s.mu.Unlock()
		log.Printf("[TradeApproval] Trade %s 15:00 收盘未获确认，判定失败: %s %s %d@%.2f", id, action, symbol, quantity, price)
		s.finalize(pt, false)
		return ResultFailed, pt, nil
	}
}

// finalize 订单结局：回调持久化钩子（orders 置成交/取消/失败 + 释放占用资金）。
// executed=true 表示已成交；false 表示取消/失败（需释放占用的买入资金）。
func (s *Service) finalize(pt *PendingTrade, executed bool) {
	s.mu.Lock()
	persist := s.persist
	s.mu.Unlock()
	if persist != nil && persist.OnResolve != nil {
		persist.OnResolve(pt, executed)
	}
}

// Confirm 确认交易（approve=true 批准，false 拒绝）
// 对已超时未决（返回 ResultPending 后）的记录，批准将调用 Executor 补执行真实交易。
func (s *Service) Confirm(id string, approved bool) error {
	s.mu.Lock()
	pt, ok := s.pending[id]
	if !ok {
		s.mu.Unlock()
		log.Printf("[TradeApproval] Confirm 未命中待确认记录(已不存在/已处理): id=%s", id)
		return fmt.Errorf("待确认交易不存在或已处理: %s", id)
	}
	if pt.Status != "pending" {
		s.mu.Unlock()
		log.Printf("[TradeApproval] Confirm 记录状态异常: id=%s status=%s", id, pt.Status)
		return fmt.Errorf("交易已处理完成，状态: %s", pt.Status)
	}
	if !pt.returned {
		// 正常等待中：推送结果给 RequestApproval，由调用方（PlaceTradeTool/CIO）执行交易
		select {
		case pt.resultCh <- approved:
			s.mu.Unlock()
			return nil
		default:
			s.mu.Unlock()
			return fmt.Errorf("交易已处理完成，无法重复确认")
		}
	}

	// 已超时未决（排队中）：补确认路径
	executor := s.executor
	s.mu.Unlock()

	if !approved {
		// 用户补拒绝：取消该排队订单
		s.mu.Lock()
		pt.Status = "rejected"
		delete(s.pending, id)
		s.mu.Unlock()
		log.Printf("[TradeApproval] Trade %s 补确认被拒绝: %s %s", id, pt.Action, pt.Symbol)
		s.finalize(pt, false)
		return nil
	}

	// 用户补批准前检查：已过 15:00 收盘，未获确认，交易判定失败，禁止补成交
	now := time.Now()
	todayClose := time.Date(now.Year(), now.Month(), now.Day(), closeHour, 0, 0, 0, now.Location())
	if !now.Before(todayClose) {
		s.mu.Lock()
		pt.Status = "failed"
		delete(s.pending, id)
		s.mu.Unlock()
		s.finalize(pt, false)
		return fmt.Errorf("已收盘(%d:00)，交易未获确认，判定失败", closeHour)
	}

	// 用户补批准：执行真实交易
	if executor == nil {
		return fmt.Errorf("交易补确认执行器未配置，无法执行: %s", id)
	}
	if err := executor(pt.DecisionID, pt.Action, pt.Symbol, pt.StockName, pt.Market, pt.Quantity, pt.Price, pt.Reason); err != nil {
		// 执行失败：保留记录（仍 pending/returned），允许用户重试或拒绝
		// 必须落日志暴露具体被拒原因（涨停/跌停/资金/决策失效/紧急停止等），否则确认失败完全不可见
		log.Printf("[TradeApproval] Trade %s 补确认执行失败: %v (仍待确认: %s %s %d@%.2f)", id, err, pt.Action, pt.Symbol, pt.Quantity, pt.Price)
		return fmt.Errorf("交易确认执行失败: %v（可重试或改为拒绝）", err)
	}
	s.mu.Lock()
	pt.Status = "executed"
	delete(s.pending, id)
	s.mu.Unlock()
	log.Printf("[TradeApproval] Trade %s 补确认后已执行: %s %s %d@%.2f", id, pt.Action, pt.Symbol, pt.Quantity, pt.Price)
	s.finalize(pt, true)
	return nil
}

// GetPending 获取所有待确认交易
func (s *Service) GetPending() []*PendingTrade {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*PendingTrade, 0, len(s.pending))
	for _, pt := range s.pending {
		result = append(result, pt)
	}
	return result
}

// ClosePending 收盘清扫：交易日 15:00 后调用。
// 将「已超时未决（排队中）且仍未获用户确认」的交易判定失败并移除，通知前端关闭弹窗。
// 仍在等待 RequestApproval 返回的记录不在此处理（其收盘定时器会自行返回 ResultFailed）。
// 幂等：重复调用无副作用。
func (s *Service) ClosePending() int {
	s.mu.Lock()
	failed := make([]*PendingTrade, 0)
	for id, pt := range s.pending {
		if !pt.returned {
			continue // 仍被 RequestApproval 等待，其收盘定时器自行处理
		}
		pt.Status = "failed"
		failed = append(failed, pt)
		delete(s.pending, id)
	}
	emit := s.emitFn
	s.mu.Unlock()

	if len(failed) > 0 {
		log.Printf("[TradeApproval] 收盘清扫: %d 笔排队交易未获确认，判定失败", len(failed))
		if emit != nil {
			emit("trade:approval_expired", failed)
		}
		for _, pt := range failed {
			s.finalize(pt, false)
		}
	}
	return len(failed)
}

// CancelAll 取消所有待确认交易（应用关闭时调用）
func (s *Service) CancelAll() {
	s.mu.Lock()
	cancelled := make([]*PendingTrade, 0)
	for id, pt := range s.pending {
		pt.Status = "expired"
		select {
		case pt.resultCh <- false:
		default:
		}
		cancelled = append(cancelled, pt)
		delete(s.pending, id)
	}
	s.mu.Unlock()
	for _, pt := range cancelled {
		s.finalize(pt, false)
	}
}

// resultOf 将用户布尔确认结果映射为 ApprovalResult
func resultOf(approved bool) ApprovalResult {
	if approved {
		return ResultApproved
	}
	return ResultRejected
}
