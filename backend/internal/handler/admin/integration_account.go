package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func isIntegrationRequest(c *gin.Context) bool {
	return c.GetBool(middleware.IntegrationAuthenticatedContextKey)
}

// Generate credentials inside the idempotent callback, so retries replay the
// original account rather than generating a new email or changing the payload.
func createIntegrationAccount(ctx context.Context, admin service.AdminService, input service.CreateUserInput) (*service.User, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var identifier [16]byte
		var password [32]byte
		if _, err := rand.Read(identifier[:]); err != nil {
			return nil, err
		}
		if _, err := rand.Read(password[:]); err != nil {
			return nil, err
		}
		input.Username = hex.EncodeToString(identifier[:]) + "ai"
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
