package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func isIntegrationRequest(c *gin.Context) bool {
	return c.GetBool(middleware.IntegrationAuthenticatedContextKey)
}

var integrationAccountNames integrationAccountNameGenerator

type integrationAccountNameGenerator struct {
	mu   sync.Mutex
	last time.Time
}

func (g *integrationAccountNameGenerator) next(now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := now.Truncate(time.Second)
	if !next.After(g.last) {
		next = g.last.Add(time.Second)
	}
	g.last = next
	return next.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("20060102150405")
}

// Generate credentials inside the idempotent callback, so retries replay the
// original account rather than generating a new email or changing the payload.
func createIntegrationAccount(ctx context.Context, admin service.AdminService, input service.CreateUserInput) (*service.User, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var password [32]byte
		if _, err := rand.Read(password[:]); err != nil {
			return nil, err
		}
		// Keep the requested timestamp-only format while avoiding same-second
		// collisions locally. Database uniqueness handles other instances.
		input.Username = integrationAccountNames.next(time.Now())
		input.Email = input.Username + "@zoomtk.com"
		input.Password = base64.RawURLEncoding.EncodeToString(password[:])
		input.Role = service.RoleUser
		user, err := admin.CreateUser(ctx, &input)
		if !errors.Is(err, service.ErrEmailExists) {
			return user, err
		}
	}
	return nil, service.ErrEmailExists
}
