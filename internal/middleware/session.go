package middleware

import (
	"fmt"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"
)

const (
	SessionKeyUserID    = "user_id"
	SessionKeyCSRFToken = "csrf_token"
	SessionKeyFlash     = "flash"
)

type SessionManager struct {
	Store *session.Store
}

func NewSessionManager(store *session.Store) *SessionManager {
	return &SessionManager{Store: store}
}

func (m *SessionManager) SetUserID(c *fiber.Ctx, userID uint) error {
	sess, err := m.Store.Get(c)
	if err != nil {
		return err
	}
	if err := sess.Regenerate(); err != nil {
		return err
	}
	sess.Set(SessionKeyUserID, fmt.Sprintf("%d", userID))
	return sess.Save()
}

func (m *SessionManager) GetUserID(c *fiber.Ctx) (uint, error) {
	sess, err := m.Store.Get(c)
	if err != nil {
		return 0, err
	}
	v := fmt.Sprintf("%v", sess.Get(SessionKeyUserID))
	if v == "" || v == "<nil>" {
		return 0, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(n), nil
}

func (m *SessionManager) Clear(c *fiber.Ctx) error {
	sess, err := m.Store.Get(c)
	if err != nil {
		return err
	}
	sess.Destroy()
	return nil
}

func (m *SessionManager) SetFlash(c *fiber.Ctx, message string) {
	sess, err := m.Store.Get(c)
	if err != nil {
		return
	}
	sess.Set(SessionKeyFlash, message)
	_ = sess.Save()
}

func (m *SessionManager) PullFlash(c *fiber.Ctx) string {
	sess, err := m.Store.Get(c)
	if err != nil {
		return ""
	}
	v := fmt.Sprintf("%v", sess.Get(SessionKeyFlash))
	if v == "<nil>" {
		v = ""
	}
	sess.Delete(SessionKeyFlash)
	_ = sess.Save()
	return v
}
