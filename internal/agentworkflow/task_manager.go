package agentworkflow

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// TaskManager 任务管理器
type TaskManager struct {
	db *gorm.DB
}

// NewTaskManager 创建任务管理器
func NewTaskManager(db *gorm.DB) *TaskManager {
	return &TaskManager{db: db}
}

// CreateTask 创建任务
func (tm *TaskManager) CreateTask(agentID, taskType, title, description, trigger, priority, phase string, inputTaskIDs []string, inputData map[string]interface{}, parentTaskID string) (*Task, error) {
	taskID := tm.generateTaskID()

	inputTaskIDsJSON := mustMarshalJSON(inputTaskIDs)
	inputDataJSON := mustMarshalJSON(inputData)

	task := &Task{
		TaskID:       taskID,
		AgentID:      agentID,
		TaskType:     taskType,
		Title:        title,
		Description:  description,
		Trigger:      trigger,
		Priority:     priority,
		Status:       TaskStatusCreated,
		Phase:        phase,
		InputTaskIDs: inputTaskIDsJSON,
		InputData:    inputDataJSON,
		ParentTaskID: parentTaskID,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := tm.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("创建任务失败: %w", err)
	}

	log.Printf("[TaskManager] 创建任务: %s, Agent: %s, Type: %s", taskID, agentID, taskType)
	return task, nil
}

// StartTask 开始执行任务
func (tm *TaskManager) StartTask(taskID string) error {
	now := time.Now()
	result := tm.db.Model(&Task{}).Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":     TaskStatusRunning,
			"started_at": &now,
			"updated_at": now,
		})
	return result.Error
}

// CompleteTask 完成任务
func (tm *TaskManager) CompleteTask(taskID, outputData string) error {
	now := time.Now()
	result := tm.db.Model(&Task{}).Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":       TaskStatusCompleted,
			"output_data":  outputData,
			"completed_at": &now,
			"updated_at":   now,
		})
	return result.Error
}

// FailTask 任务失败
func (tm *TaskManager) FailTask(taskID string) error {
	return tm.db.Model(&Task{}).Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":       TaskStatusFailed,
			"completed_at": time.Now(),
			"updated_at":   time.Now(),
		}).Error
}

// CancelTask 取消任务
func (tm *TaskManager) CancelTask(taskID string) error {
	return tm.db.Model(&Task{}).Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":       TaskStatusCancelled,
			"completed_at": time.Now(),
			"updated_at":   time.Now(),
		}).Error
}

// ApproveTask 审批任务完成
func (tm *TaskManager) ApproveTask(taskID string) error {
	return tm.db.Model(&Task{}).Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":     TaskStatusApproved,
			"updated_at": time.Now(),
		}).Error
}

// WaitTask 任务等待
func (tm *TaskManager) WaitTask(taskID string) error {
	return tm.db.Model(&Task{}).Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":     TaskStatusWaiting,
			"updated_at": time.Now(),
		}).Error
}

// GetTask 获取任务
func (tm *TaskManager) GetTask(taskID string) (*Task, error) {
	var task Task
	err := tm.db.Where("task_id = ?", taskID).First(&task).Error
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// GetTasksByStatus 按状态获取任务
func (tm *TaskManager) GetTasksByStatus(status string) ([]Task, error) {
	var tasks []Task
	err := tm.db.Where("status = ?", status).Order("created_at DESC").Find(&tasks).Error
	return tasks, err
}

// GetTasksByAgent 按智能体获取任务
func (tm *TaskManager) GetTasksByAgent(agentID string) ([]Task, error) {
	var tasks []Task
	err := tm.db.Where("agent_id = ?", agentID).Order("created_at DESC").Find(&tasks).Error
	return tasks, err
}

// GetTasksByAgentAndStatus 按智能体和状态获取任务
func (tm *TaskManager) GetTasksByAgentAndStatus(agentID, status string) ([]Task, error) {
	var tasks []Task
	err := tm.db.Where("agent_id = ? AND status = ?", agentID, status).Order("created_at DESC").Find(&tasks).Error
	return tasks, err
}

// GetTodayTasks 获取今日任务
func (tm *TaskManager) GetTodayTasks() ([]Task, error) {
	today := time.Now().Format("2006-01-02")
	var tasks []Task
	err := tm.db.Where("DATE(created_at) = ?", today).Order("created_at DESC").Find(&tasks).Error
	return tasks, err
}

// GetTasksByPhase 按阶段获取任务
func (tm *TaskManager) GetTasksByPhase(phase string) ([]Task, error) {
	var tasks []Task
	err := tm.db.Where("phase = ?", phase).Order("created_at DESC").Find(&tasks).Error
	return tasks, err
}

// GetTaskSummary 获取任务汇总
func (tm *TaskManager) GetTaskSummary() (map[string]interface{}, error) {
	today := time.Now().Format("2006-01-02")

	type Summary struct {
		Total     int64
		Created   int64
		Running   int64
		Completed int64
		Failed    int64
		Pending   int64
	}

	var s Summary

	tm.db.Model(&Task{}).Where("DATE(created_at) = ?", today).Count(&s.Total)
	tm.db.Model(&Task{}).Where("DATE(created_at) = ? AND status = ?", today, TaskStatusCreated).Count(&s.Created)
	tm.db.Model(&Task{}).Where("DATE(created_at) = ? AND status = ?", today, TaskStatusRunning).Count(&s.Running)
	tm.db.Model(&Task{}).Where("DATE(created_at) = ? AND status = ?", today, TaskStatusCompleted).Count(&s.Completed)
	tm.db.Model(&Task{}).Where("DATE(created_at) = ? AND status = ?", today, TaskStatusFailed).Count(&s.Failed)
	s.Pending = s.Created

	type AgentCount struct {
		AgentID   string
		Total     int64
		Completed int64
	}
	var agentCounts []AgentCount
	tm.db.Model(&Task{}).
		Select("agent_id, count(*) as total, sum(case when status = 'COMPLETED' then 1 else 0 end) as completed").
		Where("DATE(created_at) = ?", today).
		Group("agent_id").
		Scan(&agentCounts)

	return map[string]interface{}{
		"date":     today,
		"total":    s.Total,
		"pending":  s.Pending,
		"running":  s.Running,
		"completed": s.Completed,
		"failed":   s.Failed,
		"by_agent": agentCounts,
	}, nil
}

// generateTaskID 生成任务ID
func (tm *TaskManager) generateTaskID() string {
	today := time.Now().Format("20060102")

	var count int64
	tm.db.Model(&Task{}).
		Where("created_at >= ?", time.Now().Truncate(24*time.Hour)).
		Count(&count)

	seq := fmt.Sprintf("%04d", count+1)
	return fmt.Sprintf("TASK-%s-%s", today, seq)
}