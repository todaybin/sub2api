package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type codeBuddySchedulingAdminStub struct {
	AdminService
	expected         *Account
	settings         CodeBuddyCheckinSettings
	disable, restore bool
	persistErr       error
}

func (s *codeBuddySchedulingAdminStub) UpdateCodeBuddyBalanceScheduling(_ context.Context, expected *Account, settings CodeBuddyCheckinSettings, disable, restore bool) error {
	s.expected, s.settings, s.disable, s.restore = expected, settings, disable, restore
	return s.persistErr
}

type codeBuddyCheckinTransport func(*http.Request) (*http.Response, error)

func (f codeBuddyCheckinTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCodeBuddyCheckinUsesAtomicBalanceScheduling(t *testing.T) {
	for _, tt := range []struct {
		name, creditBody string
		disable, restore bool
	}{
		{name: "positive remaining", creditBody: `{"data":{"Accounts":[{"CapacityRemain":12}]}}`, restore: true},
		{name: "zero overrides grant", creditBody: `{"data":{"Accounts":[{"CapacityRemain":0}]}}`, disable: true},
		{name: "negative overrides grant", creditBody: `{"data":{"Accounts":[{"CycleCapacityRemain":-12}]}}`, disable: true},
		{name: "grant with unavailable remaining", creditBody: `{"code":"1","msg":"not available"}`, restore: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			admin := &codeBuddySchedulingAdminStub{}
			oauth := &CodeBuddyOAuthService{clientFactory: func(string) (*http.Client, error) {
				return &http.Client{Transport: codeBuddyCheckinTransport(func(req *http.Request) (*http.Response, error) {
					body := `{"data":{"credit":10}}`
					if strings.HasSuffix(req.URL.Path, "get-user-resource") {
						body = tt.creditBody
					}
					require.Equal(t, "Bearer token", req.Header.Get("Authorization"))
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
				})}, nil
			}}
			account := &Account{ID: 7, Platform: PlatformCodeBuddy, Type: AccountTypeOAuth, Status: StatusActive,
				Credentials: map[string]any{"region": CodeBuddyRegionInternational, "access_token": "token"},
				Extra:       map[string]any{SchedulingRevisionExtraKey: "manual-pause", SchedulingManualPausedAtExtraKey: "now"},
			}
			runner := NewCodeBuddyCheckinRunnerService(admin, oauth, nil)
			require.NoError(t, runner.runOne(context.Background(), account, CodeBuddyCheckinSettingsForAccount(account)))
			require.Equal(t, tt.disable, admin.disable)
			require.Equal(t, tt.restore, admin.restore)
			// The repository receives the original override revision even when credits return.
			require.Equal(t, "manual-pause", admin.expected.Extra[SchedulingRevisionExtraKey])
			require.Contains(t, admin.expected.Extra, SchedulingManualPausedAtExtraKey)
			require.NotEmpty(t, admin.settings.NextRunAt)
			admin.persistErr = errors.New("stale scheduling revision")
			require.ErrorIs(t, runner.runOne(context.Background(), account, CodeBuddyCheckinSettingsForAccount(account)), admin.persistErr)
		})
	}
}
