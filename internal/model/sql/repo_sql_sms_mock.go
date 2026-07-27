package sql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"hermes-mock/internal/entity"
)

// 短信厂商 Mock：配置、消息/DLR 任务与每次回调尝试。

func (r *GormRepository) ListSMSMockEndpoints(ctx context.Context) ([]entity.SMSMockEndpoint, error) {
	var rows []entity.SMSMockEndpoint
	err := r.db.WithContext(ctx).Order("id ASC").Find(&rows).Error
	return rows, err
}

func (r *GormRepository) GetSMSMockEndpoint(ctx context.Context, id int64) (*entity.SMSMockEndpoint, error) {
	var row entity.SMSMockEndpoint
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *GormRepository) GetSMSMockEndpointByToken(ctx context.Context, token string) (*entity.SMSMockEndpoint, error) {
	var row entity.SMSMockEndpoint
	if err := r.db.WithContext(ctx).Where("token = ?", token).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *GormRepository) UpsertSMSMockEndpoint(ctx context.Context, row *entity.SMSMockEndpoint) error {
	if row.ID > 0 {
		modifiedAt := time.Now().UTC()
		res := r.db.WithContext(ctx).Model(&entity.SMSMockEndpoint{}).Where("id = ?", row.ID).Updates(map[string]any{
			"name": row.Name, "enabled": row.Enabled, "provider": row.Provider,
			"protocol_version": row.ProtocolVersion, "config_json": row.ConfigJSON,
			"remark": row.Remark, "gmt_modified": modifiedAt,
		})
		if res.Error != nil {
			return res.Error
		}
		if err := r.db.WithContext(ctx).Where("id = ?", row.ID).First(row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("SMS Mock endpoint %d 已不存在", row.ID)
			}
			return err
		}
		return nil
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "token"}},
		DoNothing: true,
	}).Create(row)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("SMS Mock token 冲突，请重试创建")
	}
	return r.db.WithContext(ctx).Where("token = ?", row.Token).First(row).Error
}

func (r *GormRepository) DeleteSMSMockEndpoint(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("message_id IN (SELECT id FROM mock_sms_message WHERE endpoint_id = ?)", id).
			Delete(&entity.SMSMockCallbackAttempt{}).Error; err != nil {
			return err
		}
		if err := tx.Where("endpoint_id = ?", id).Delete(&entity.SMSMockMessage{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&entity.SMSMockEndpoint{}).Error
	})
}

func (r *GormRepository) CreateSMSMockMessages(ctx context.Context, rows []entity.SMSMockMessage) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&rows).Error
}

func (r *GormRepository) ListSMSMockMessages(ctx context.Context, f entity.SMSMockMessageFilter) ([]entity.SMSMockMessage, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	db := r.db.WithContext(ctx).Model(&entity.SMSMockMessage{})
	if f.EndpointID > 0 {
		db = db.Where("endpoint_id = ?", f.EndpointID)
	}
	if v := strings.TrimSpace(f.Reference); v != "" {
		db = db.Where("reference = ?", v)
	}
	if v := strings.TrimSpace(f.Recipient); v != "" {
		db = db.Where("recipient LIKE ?", "%"+v+"%")
	}
	if v := strings.TrimSpace(f.SelectedCase); v != "" {
		db = db.Where("selected_case = ?", v)
	}
	if v := strings.TrimSpace(f.ReceiptStatus); v != "" {
		db = db.Where("receipt_status = ?", strings.ToUpper(v))
	}
	if v := strings.TrimSpace(f.Keyword); v != "" {
		like := "%" + v + "%"
		db = db.Where("reference LIKE ? OR recipient LIKE ? OR sender LIKE ? OR content LIKE ? OR request_body LIKE ? OR submit_response_body LIKE ? OR callback_last_error LIKE ?",
			like, like, like, like, like, like, like)
	}
	var rows []entity.SMSMockMessage
	err := db.Order("received_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (r *GormRepository) GetSMSMockMessage(ctx context.Context, id int64) (*entity.SMSMockMessage, error) {
	var row entity.SMSMockMessage
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *GormRepository) DeleteSMSMockMessages(ctx context.Context, endpointID int64) (int64, error) {
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("message_id IN (SELECT id FROM mock_sms_message WHERE endpoint_id = ?)", endpointID).
			Delete(&entity.SMSMockCallbackAttempt{}).Error; err != nil {
			return err
		}
		res := tx.Where("endpoint_id = ?", endpointID).Delete(&entity.SMSMockMessage{})
		deleted = res.RowsAffected
		return res.Error
	})
	return deleted, err
}

func (r *GormRepository) CompleteSMSMockSubmission(ctx context.Context, ids []int64, state string, completedAt time.Time, activateReceipt bool) error {
	if len(ids) == 0 {
		return nil
	}
	// SQLite 的 deferred transaction 若先 SELECT 再 UPDATE，遇到并发 callback claim 会发生
	// SQLITE_BUSY_SNAPSHOT，busy_timeout 也无法等待。先在事务外取只读快照，事务内第一句直接写。
	var rows []entity.SMSMockMessage
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(ids) {
		return fmt.Errorf("短信 Mock 提交记录不完整: want=%d got=%d", len(ids), len(rows))
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&entity.SMSMockMessage{}).
			Where("id IN ? AND submit_state = ?", ids, entity.SMSMockSubmitPending).
			Updates(map[string]any{"submit_state": state, "submit_completed_at": completedAt})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != int64(len(ids)) {
			return fmt.Errorf("短信 Mock 提交状态已变化: want=%d updated=%d", len(ids), res.RowsAffected)
		}
		if !activateReceipt {
			return tx.Model(&entity.SMSMockMessage{}).
				Where("id IN ? AND receipt_status = ?", ids, entity.SMSMockReceiptWaiting).
				Updates(map[string]any{
					"receipt_status": entity.SMSMockReceiptCanceled, "receipt_due_at": nil, "callback_claimed_at": nil,
				}).Error
		}
		for _, row := range rows {
			if !row.ReceiptEnabled || row.ReceiptStatus != entity.SMSMockReceiptWaiting {
				continue
			}
			due := completedAt.Add(time.Duration(row.ReceiptDelayMs) * time.Millisecond)
			if err := tx.Model(&entity.SMSMockMessage{}).
				Where("id = ? AND receipt_status = ?", row.ID, entity.SMSMockReceiptWaiting).
				Updates(map[string]any{
					"receipt_status": entity.SMSMockReceiptPending, "receipt_due_at": due, "callback_claimed_at": nil,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RecoverSMSMockSubmissions 终止进程崩溃后遗留的同步提交。staleBefore 必须覆盖
// 最大响应等待时间，避免滚动启动的新实例取消另一实例仍在正常处理的 fresh 请求。
func (r *GormRepository) RecoverSMSMockSubmissions(ctx context.Context, staleBefore, recoveredAt time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&entity.SMSMockMessage{}).
		Where("submit_state = ? AND received_at <= ?", entity.SMSMockSubmitPending, staleBefore).
		Updates(map[string]any{
			"submit_state":        entity.SMSMockSubmitClientCanceled,
			"submit_completed_at": recoveredAt,
			"receipt_status": gorm.Expr(
				"CASE WHEN receipt_status = ? THEN ? ELSE receipt_status END",
				entity.SMSMockReceiptWaiting, entity.SMSMockReceiptCanceled,
			),
			"receipt_due_at":      nil,
			"callback_claimed_at": nil,
		})
	return res.RowsAffected, res.Error
}

// ClaimDueSMSMockCallbacks 使用逐行 CAS，多个 hermes-mock 实例同时扫描也只有一个能 claim。
func (r *GormRepository) ClaimDueSMSMockCallbacks(ctx context.Context, now time.Time, limit int) ([]entity.SMSMockMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 32
	}
	var candidates []entity.SMSMockMessage
	if err := r.db.WithContext(ctx).
		Where("submit_state = ? AND receipt_status IN ? AND receipt_due_at IS NOT NULL AND receipt_due_at <= ?", entity.SMSMockSubmitResponded, []string{entity.SMSMockReceiptPending, entity.SMSMockReceiptRetry}, now).
		Order("receipt_due_at ASC, id ASC").Limit(limit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	claimed := make([]entity.SMSMockMessage, 0, len(candidates))
	for _, row := range candidates {
		res := r.db.WithContext(ctx).Model(&entity.SMSMockMessage{}).
			Where("id = ? AND submit_state = ? AND receipt_status IN ?", row.ID, entity.SMSMockSubmitResponded, []string{entity.SMSMockReceiptPending, entity.SMSMockReceiptRetry}).
			Updates(map[string]any{"receipt_status": entity.SMSMockReceiptDelivering, "receipt_due_at": nil, "callback_claimed_at": now})
		if res.Error != nil {
			return claimed, res.Error
		}
		if res.RowsAffected > 0 {
			row.ReceiptStatus = entity.SMSMockReceiptDelivering
			row.ReceiptDueAt = nil
			claimed = append(claimed, row)
		}
	}
	return claimed, nil
}

func (r *GormRepository) UpdateSMSMockCallback(ctx context.Context, update entity.SMSMockCallbackUpdate) error {
	values := map[string]any{
		"receipt_status":              update.NextStatus,
		"receipt_due_at":              update.NextDueAt,
		"callback_attempts":           gorm.Expr("callback_attempts + 1"),
		"callback_last_http_status":   update.HTTPStatus,
		"callback_last_response_body": update.ResponseBody,
		"callback_last_error":         update.LastError,
		"callback_completed_at":       update.CompletedAt,
		"callback_claimed_at":         nil,
	}
	if update.IncrementSent {
		values["receipt_sent_count"] = gorm.Expr("receipt_sent_count + 1")
	}
	if update.ResetCurrentAttempt {
		values["callback_current_attempt"] = 0
	} else {
		values["callback_current_attempt"] = gorm.Expr("callback_current_attempt + 1")
	}
	res := r.db.WithContext(ctx).Model(&entity.SMSMockMessage{}).
		Where("id = ? AND receipt_status = ?", update.MessageID, entity.SMSMockReceiptDelivering).Updates(values)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("短信 Mock 消息 %d 回调状态已变化，拒绝覆盖", update.MessageID)
	}
	return nil
}

func (r *GormRepository) EnqueueSMSMockCallback(ctx context.Context, id int64, now time.Time) (*entity.SMSMockMessage, error) {
	var out entity.SMSMockMessage
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&out).Error; err != nil {
		return nil, err
	}
	if !out.ReceiptEnabled || out.CallbackURL == "" || out.CallbackBody == "" {
		return nil, fmt.Errorf("该消息没有可投递的 DLR")
	}
	if out.SubmitState != entity.SMSMockSubmitResponded {
		return nil, fmt.Errorf("只有已返回同步响应的消息可以投递 DLR")
	}
	if out.ReceiptStatus == entity.SMSMockReceiptDelivering {
		return nil, fmt.Errorf("DLR 正在投递，请稍后重试")
	}
	target := out.ReceiptTargetCount
	if out.ReceiptStatus != entity.SMSMockReceiptPending && out.ReceiptStatus != entity.SMSMockReceiptRetry && target <= out.ReceiptSentCount {
		target = out.ReceiptSentCount + 1
	}
	updates := map[string]any{
		"receipt_status": entity.SMSMockReceiptPending, "receipt_due_at": now,
		"receipt_target_count": target, "callback_current_attempt": 0,
		"callback_completed_at": nil, "callback_last_error": "", "callback_claimed_at": nil,
	}
	res := r.db.WithContext(ctx).Model(&entity.SMSMockMessage{}).
		Where("id = ? AND submit_state = ? AND receipt_status = ?", id, entity.SMSMockSubmitResponded, out.ReceiptStatus).
		Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("DLR 状态已变化，请刷新后重试")
	}
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *GormRepository) CancelSMSMockCallback(ctx context.Context, id int64) error {
	res := r.db.WithContext(ctx).Model(&entity.SMSMockMessage{}).
		Where("id = ? AND receipt_status IN ?", id, []string{entity.SMSMockReceiptWaiting, entity.SMSMockReceiptPending, entity.SMSMockReceiptRetry}).
		Updates(map[string]any{"receipt_status": entity.SMSMockReceiptCanceled, "receipt_due_at": nil, "callback_claimed_at": nil})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("只有尚未激活、等待中或重试中的 DLR 可以取消")
	}
	return nil
}

func (r *GormRepository) RecoverSMSMockCallbacks(ctx context.Context, staleBefore, retryAt time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&entity.SMSMockMessage{}).
		Where("receipt_status = ? AND (callback_claimed_at IS NULL OR callback_claimed_at <= ?)", entity.SMSMockReceiptDelivering, staleBefore).
		Updates(map[string]any{
			"receipt_status": entity.SMSMockReceiptRetry, "receipt_due_at": retryAt, "callback_claimed_at": nil,
			"callback_last_error": "hermes-mock 重启，恢复未确认的 DLR（可能产生厂商式重复回调）",
		})
	return res.RowsAffected, res.Error
}

func (r *GormRepository) CreateSMSMockCallbackAttempt(ctx context.Context, row *entity.SMSMockCallbackAttempt) error {
	return r.db.WithContext(ctx).Create(row).Error
}

func (r *GormRepository) ListSMSMockCallbackAttempts(ctx context.Context, messageID int64, limit int) ([]entity.SMSMockCallbackAttempt, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []entity.SMSMockCallbackAttempt
	err := r.db.WithContext(ctx).Where("message_id = ?", messageID).Order("started_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
