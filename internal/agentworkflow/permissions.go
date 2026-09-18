package agentworkflow

import (
	"fmt"
	"log"

	"gorm.io/gorm"
)

// PermissionManager 权限管理器
type PermissionManager struct {
	db *gorm.DB
}

// NewPermissionManager 创建权限管理器
func NewPermissionManager(db *gorm.DB) *PermissionManager {
	return &PermissionManager{db: db}
}

// CheckPermission 检查智能体是否有某项权限
func (pm *PermissionManager) CheckPermission(agentID, permission string) (bool, error) {
	var perm AgentPermission
	result := pm.db.Where("agent_id = ? AND permission = ?", agentID, permission).First(&perm)
	if result.Error == gorm.ErrRecordNotFound {
		// 如果数据库中没有记录，使用默认权限矩阵
		if matrix, ok := PermissionMatrix[agentID]; ok {
			if allowed, ok := matrix[permission]; ok {
				return allowed, nil
			}
		}
		return false, nil
	}
	if result.Error != nil {
		return false, result.Error
	}
	return perm.IsAllowed, nil
}

// HasAnyPermission 检查智能体是否有任一权限
func (pm *PermissionManager) HasAnyPermission(agentID string, permissions []string) (bool, string, error) {
	for _, perm := range permissions {
		has, err := pm.CheckPermission(agentID, perm)
		if err != nil {
			continue
		}
		if has {
			return true, perm, nil
		}
	}
	return false, "", nil
}

// CanApproveInvestment 检查智能体是否可以批准投资
func (pm *PermissionManager) CanApproveInvestment(agentID string) bool {
	has, err := pm.CheckPermission(agentID, PermApproveInvestment)
	if err != nil {
		return false
	}
	return has
}

// CanApproveInvestmentPlan 检查智能体是否可以批准投资方案
func (pm *PermissionManager) CanApproveInvestmentPlan(agentID string) bool {
	has, err := pm.CheckPermission(agentID, PermApproveInvestmentPlan)
	if err != nil {
		return false
	}
	return has
}

// CanRejectInvestmentPlan 检查智能体是否可以拒绝投资方案
func (pm *PermissionManager) CanRejectInvestmentPlan(agentID string) bool {
	has, err := pm.CheckPermission(agentID, PermRejectInvestmentPlan)
	if err != nil {
		return false
	}
	return has
}

// CanCreateInvestmentPlan 检查智能体是否可以创建投资方案
func (pm *PermissionManager) CanCreateInvestmentPlan(agentID string) bool {
	has, err := pm.CheckPermission(agentID, PermCreateInvestmentPlan)
	if err != nil {
		return false
	}
	return has
}

// CanExecuteOrder 检查智能体是否可以执行订单
func (pm *PermissionManager) CanExecuteOrder(agentID string) bool {
	// 只有Trader可以执行订单
	return agentID == AgentTrader
}

// CanSelectStock 检查智能体是否可以选择股票
func (pm *PermissionManager) CanSelectStock(agentID string) bool {
	// 只有CIO可以正式选入股票
	return agentID == AgentCIO
}

// HasVetoPower 检查智能体是否有否决权
func (pm *PermissionManager) HasVetoPower(agentID string) bool {
	// 只有Risk有否决权
	return agentID == AgentRisk
}

// EmergencyFreeze 紧急冻结权限
func (pm *PermissionManager) CanEmergencyFreeze(agentID string) bool {
	has, err := pm.CheckPermission(agentID, PermEmergencyFreeze)
	if err != nil {
		return false
	}
	return has
}

// ValidateTaskAccess 验证智能体对任务的访问权限
func (pm *PermissionManager) ValidateTaskAccess(agentID string, task *Task) error {
	// 任务只能被分配的智能体访问
	if task.AgentID != agentID {
		return fmt.Errorf("任务不属于智能体 %s，属于 %s", agentID, task.AgentID)
	}
	return nil
}

// GetAgentPermissions 获取智能体的所有权限
func (pm *PermissionManager) GetAgentPermissions(agentID string) ([]AgentPermission, error) {
	var permissions []AgentPermission
	result := pm.db.Where("agent_id = ?", agentID).Find(&permissions)
	if result.Error != nil {
		return nil, result.Error
	}

	// 如果数据库中没有，从默认矩阵获取
	if len(permissions) == 0 {
		for perm, allowed := range PermissionMatrix[agentID] {
			permissions = append(permissions, AgentPermission{
				AgentID:    agentID,
				Permission: perm,
				IsAllowed:  allowed,
			})
		}
	}
	return permissions, nil
}

// GrantPermission 授予智能体权限
func (pm *PermissionManager) GrantPermission(agentID, permission string) error {
	var perm AgentPermission
	result := pm.db.Where("agent_id = ? AND permission = ?", agentID, permission).First(&perm)
	if result.Error == gorm.ErrRecordNotFound {
		return pm.db.Create(&AgentPermission{
			AgentID:    agentID,
			Permission: permission,
			IsAllowed:  true,
		}).Error
	}
	if result.Error != nil {
		return result.Error
	}
	perm.IsAllowed = true
	return pm.db.Save(&perm).Error
}

// RevokePermission 撤销智能体权限
func (pm *PermissionManager) RevokePermission(agentID, permission string) error {
	var perm AgentPermission
	result := pm.db.Where("agent_id = ? AND permission = ?", agentID, permission).First(&perm)
	if result.Error == gorm.ErrRecordNotFound {
		return nil
	}
	if result.Error != nil {
		return result.Error
	}
	perm.IsAllowed = false
	return pm.db.Save(&perm).Error
}

// LogPermissionCheck 记录权限检查日志
func (pm *PermissionManager) LogPermissionCheck(agentID, permission string, allowed bool) {
	log.Printf("[Permission] Agent: %s, Permission: %s, Allowed: %v", agentID, permission, allowed)
}
