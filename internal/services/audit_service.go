package services

import (
	"encoding/json"
	"fmt"

	"deploycp/internal/models"
	"deploycp/internal/repositories"
)

type AuditService struct {
	repo     *repositories.AuditLogRepository
	activity *repositories.ActivityLogRepository
}

func NewAuditService(repo *repositories.AuditLogRepository, activity *repositories.ActivityLogRepository) *AuditService {
	return &AuditService{repo: repo, activity: activity}
}

func (s *AuditService) Record(actorUserID *uint, action, resource, resourceID, ip string, payload any) {
	payloadStr := ""
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			payloadStr = string(b)
		}
	}
	_ = s.repo.Create(&models.AuditLog{ActorUserID: actorUserID, Action: action, Resource: resource, ResourceID: resourceID, IP: ip, Payload: payloadStr})
	_ = s.activity.Create(&models.ActivityLog{Type: "audit", Title: action, Body: fmt.Sprintf("%s %s", resource, resourceID), Level: "info", RefType: resource, RefID: resourceID})
}

func (s *AuditService) RecordSystemAction(actorUserID *uint, action string, payload string, ip string) {
	s.Record(actorUserID, action, "system", "0", ip, map[string]string{"payload": payload})
}
