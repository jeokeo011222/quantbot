package agentworkflow

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/quantpilot/quantpilot/internal/agentownership"
	"gorm.io/gorm"
)

// EventBus 事件总线
// 吸收 dsh-superagent follow-up ownership 机制：
//   - 注入 *agentownership.Registry 统一管理用户追问归属
//   - FOLLOWUP 事件自动登记归属，Agent 处理不了时调用 Handback 返回主编排器
//   - 所有事件都可带 "agent_owner" 字段，Registry 据此做归属
type EventBus struct {
	db       *gorm.DB
	handlers map[string][]EventHandler
	// OwnershipRegistry 用户追问归属表（可选：nil 则跳过归属逻辑）
	OwnershipRegistry *agentownership.Registry
}

// EventHandler 事件处理器接口
type EventHandler interface {
	HandleEvent(event *Event) error
	GetSupportedEventTypes() []string
	// GetAgentRole 该 Handler 对应的 Agent 角色（用于追问归属路由）
	GetAgentRole() string
}

// NewEventBus 创建事件总线
func NewEventBus(db *gorm.DB) *EventBus {
	return &EventBus{
		db:                db,
		handlers:          make(map[string][]EventHandler),
		OwnershipRegistry: agentownership.NewRegistry(), // 默认初始化空注册表
	}
}

// RegisterHandler 注册事件处理器
func (eb *EventBus) RegisterHandler(handler EventHandler) {
	for _, eventType := range handler.GetSupportedEventTypes() {
		eb.handlers[eventType] = append(eb.handlers[eventType], handler)
		log.Printf("[EventBus] 注册事件处理器: %s -> %T", eventType, handler)
	}
}

// PublishEvent 发布事件
func (eb *EventBus) PublishEvent(eventType, source, description string, payload map[string]interface{}) (*Event, error) {
	eventID := fmt.Sprintf("EVT-%s-%d", time.Now().Format("20060102150405"), time.Now().UnixNano()%10000)

	event := &Event{
		EventID:     eventID,
		EventType:   eventType,
		Source:      source,
		Description: description,
		Payload:     mustMarshalJSON(payload),
		Status:      EventStatusPending,
		CreatedAt:   time.Now(),
	}

	if err := eb.db.Create(event).Error; err != nil {
		return nil, fmt.Errorf("发布事件失败: %w", err)
	}

	log.Printf("[EventBus] 发布事件: %s, 类型: %s", eventID, eventType)

	// 触发处理
	go eb.processEvent(event)

	return event, nil
}

// processEvent 处理事件
func (eb *EventBus) processEvent(event *Event) {
	// 更新状态
	eb.db.Model(&Event{}).Where("event_id = ?", event.EventID).Update("status", EventStatusProcessing)

	var triggeredTasks []string

	// === FOLLOWUP 特殊处理：自动登记归属（吸收 dsh-superagent follow-up ownership 机制）===
	if event.EventType == EventFollowup && eb.OwnershipRegistry != nil {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(event.Payload), &payload); err == nil {
			owner := fmt.Sprintf("%v", payload["agent_owner"])
			content := fmt.Sprintf("%v", payload["content"])
			if owner != "<nil>" && owner != "" {
				eb.OwnershipRegistry.Register(event.EventID, content, owner)
				log.Printf("[EventBus] 追问归属登记: followup=%s -> agent=%s", event.EventID, owner)
			}
		}
	}

	// 查找对应的处理器
	handlers, exists := eb.handlers[event.EventType]
	if exists {
		for _, handler := range handlers {
			if err := handler.HandleEvent(event); err != nil {
				log.Printf("[EventBus] 处理事件失败: %s, handler: %T, error: %v", event.EventID, handler, err)
			}
		}
	}

	// 根据事件类型自动创建任务
	taskIDs := eb.autoCreateTasksForEvent(event)
	triggeredTasks = append(triggeredTasks, taskIDs...)

	// 更新事件状态
	eb.db.Model(&Event{}).Where("event_id = ?", event.EventID).
		Updates(map[string]interface{}{
			"status":         EventStatusCompleted,
			"triggered_tasks": mustMarshalJSON(triggeredTasks),
		})
}

// autoCreateTasksForEvent 根据事件自动创建任务
func (eb *EventBus) autoCreateTasksForEvent(event *Event) []string {
	var taskIDs []string

	switch event.EventType {
	case EventSystemStart:
		// 系统启动时初始化日常任务
		log.Printf("[EventBus] 系统启动，创建初始任务")

	case EventMarketPreOpen:
		// 盘前事件触发日常投资工作流
		log.Printf("[EventBus] 盘前事件，触发盘前任务")

	case EventMarketOpen:
		// 开盘事件
		log.Printf("[EventBus] 开盘事件，触发盘中任务")

	case EventMarketClose:
		// 收盘事件
		log.Printf("[EventBus] 收盘事件，触发盘后任务")

	case EventMarketShock:
		// 市场冲击事件
		log.Printf("[EventBus] 市场冲击事件，触发紧急处理")
		taskID := eb.createEmergencyTask(AgentRisk, TaskTypeRiskAlert, "市场冲击风险评估")
		if taskID != "" {
			taskIDs = append(taskIDs, taskID)
		}

	case EventPriceAlert:
		// 价格预警
		log.Printf("[EventBus] 价格预警事件")

	case EventVolatilitySpike:
		// 波动率飙升
		log.Printf("[EventBus] 波动率飙升事件")
		taskID := eb.createEmergencyTask(AgentRisk, TaskTypeRiskAlert, "波动率飙升风险评估")
		if taskID != "" {
			taskIDs = append(taskIDs, taskID)
		}

	case EventFactorBreak:
		// 因子突破
		log.Printf("[EventBus] 因子突破事件")
		taskID := eb.createEmergencyTask(AgentQuant, TaskTypeFactorBreak, "因子异动分析")
		if taskID != "" {
			taskIDs = append(taskIDs, taskID)
		}

	case EventRiskLimit:
		// 风险限制触发
		log.Printf("[EventBus] 风险限制触发事件")
		taskID := eb.createEmergencyTask(AgentRisk, TaskTypeRiskAssessment, "风险限制定位评估")
		if taskID != "" {
			taskIDs = append(taskIDs, taskID)
		}

	case EventPortfolioDrift:
		// 组合漂移
		log.Printf("[EventBus] 组合漂移事件")

	case EventNewsEvent:
		// 新闻事件
		log.Printf("[EventBus] 新闻事件")

	case EventOrderFilled:
		// 订单成交
		log.Printf("[EventBus] 订单成交事件")

	case EventOrderFailed:
		// 订单失败
		log.Printf("[EventBus] 订单失败事件")
		taskID := eb.createEmergencyTask(AgentTrader, TaskTypeExecutionReport, "订单失败处理")
		if taskID != "" {
			taskIDs = append(taskIDs, taskID)
		}

	case EventUserUpdate:
		// 用户更新
		log.Printf("[EventBus] 用户更新事件")

	case EventMandateChanged:
		// 投资政策变更
		log.Printf("[EventBus] 投资政策变更事件")
		taskID := eb.createEmergencyTask(AgentPlanner, TaskTypeCheckMandate, "投资政策变更检查")
		if taskID != "" {
			taskIDs = append(taskIDs, taskID)
		}

	case EventEmergencyStop:
		// 紧急停止
		log.Printf("[EventBus] 紧急停止事件")

		// 紧急冻结所有交易
		var decisions []InvestmentDecision
		eb.db.Where("status IN ?", []string{DecisionStatusDraft, DecisionStatusApproved}).
			Find(&decisions)

		for _, d := range decisions {
			d.Status = DecisionStatusRejected
			d.Reason += " [紧急冻结]"
			eb.db.Save(&d)
		}

		log.Printf("[EventBus] 紧急冻结了 %d 个决策", len(decisions))

	case EventFollowup:
		// 用户追问事件：不自动派工（因为 Agent 归属已在 processEvent 里登记），
		// 由被归属的 Agent handler 自行决定在范围内回答还是 handback。
		// 若 handler 不存在（即没有 agent 订阅 FOLLOWUP 事件），说明主编排器需要重新路由。
		log.Printf("[EventBus] 用户追问事件: %s", event.EventID)
		if _, hasHandler := eb.handlers[event.EventType]; !hasHandler {
			log.Printf("[EventBus] 无 FOLLOWUP handler，需主编排器重新路由该追问")
		}
	}

	return taskIDs
}

// createEmergencyTask 创建紧急任务
func (eb *EventBus) createEmergencyTask(agentID, taskType, title string) string {
	taskMgr := NewTaskManager(eb.db)
	task, err := taskMgr.CreateTask(
		agentID,
		taskType,
		title,
		"由事件驱动自动创建的紧急任务",
		"EVENT_TRIGGERED",
		"CRITICAL",
		PhaseEmergency,
		nil,
		nil,
		"",
	)
	if err != nil {
		log.Printf("[EventBus] 创建紧急任务失败: %v", err)
		return ""
	}

	log.Printf("[EventBus] 创建紧急任务: %s, Agent: %s", task.TaskID, agentID)
	return task.TaskID
}

// GetEvent 获取事件
func (eb *EventBus) GetEvent(eventID string) (*Event, error) {
	var event Event
	err := eb.db.Where("event_id = ?", eventID).First(&event).Error
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// GetEventsByType 按类型获取事件
func (eb *EventBus) GetEventsByType(eventType string, limit int) ([]Event, error) {
	var events []Event
	query := eb.db.Where("event_type = ?", eventType).Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&events).Error
	return events, err
}

// GetTodayEvents 获取今日事件
func (eb *EventBus) GetTodayEvents() ([]Event, error) {
	today := time.Now().Format("2006-01-02")
	var events []Event
	err := eb.db.Where("DATE(created_at) = ?", today).Order("created_at DESC").Find(&events).Error
	return events, err
}

// GetPendingEvents 获取待处理事件
func (eb *EventBus) GetPendingEvents() ([]Event, error) {
	var events []Event
	err := eb.db.Where("status = ?", EventStatusPending).Order("created_at ASC").Find(&events).Error
	return events, err
}

// CreateSystemEvents 创建系统定时事件
func (eb *EventBus) CreateSystemEvents() {
	now := time.Now()

	// 根据时间创建相应事件
	hour := now.Hour()

	switch {
	// 09:10-09:15 盘前研究
	case hour == 9 && now.Minute() >= 10 && now.Minute() < 20:
		eb.PublishEvent(EventMarketPreOpen, "SYSTEM", "盘前研究阶段开始", nil)

	// 09:30 开盘
	case hour == 9 && now.Minute() >= 30:
		eb.PublishEvent(EventMarketOpen, "SYSTEM", "市场开盘", nil)

	// 15:00 收盘
	case hour == 15 && now.Minute() == 0:
		eb.PublishEvent(EventMarketClose, "SYSTEM", "市场收盘", nil)
	}
}
