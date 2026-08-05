package magicform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newTestChannel(t *testing.T, channelName string, settings *config.NuestraSettings) *MagicFormChannel {
	t.Helper()

	bc := &config.Channel{Type: config.ChannelNuestra, Enabled: true}
	ch, err := NewMagicFormChannel(bc, settings, t.TempDir(), bus.NewMessageBus())
	require.NoError(t, err)
	// Mirrors what the factory in init.go does for every instance.
	ch.SetName(channelName)
	return ch
}

// TestDefaultPathsFollowChannelName pins that default HTTP paths derive from the
// channel's config key. A fixed path would put two brands on one webhook and let
// one brand's health handler overwrite the other's.
func TestDefaultPathsFollowChannelName(t *testing.T) {
	cases := []struct {
		channelName string
		wantWebhook string
		wantHealth  string
	}{
		{config.ChannelMagicForm, "/hooks/magicform", "/health/magicform"},
		{config.ChannelNuestra, "/hooks/nuestra", "/health/nuestra"},
		{"otherbrand", "/hooks/otherbrand", "/health/otherbrand"},
	}
	for _, tc := range cases {
		t.Run(tc.channelName, func(t *testing.T) {
			ch := newTestChannel(t, tc.channelName, &config.NuestraSettings{})
			assert.Equal(t, tc.wantWebhook, ch.WebhookPath())
			assert.Equal(t, tc.wantHealth, ch.HealthPath())
		})
	}
}

// TestExplicitWebhookPathWins verifies an operator-set webhook_path is not
// overridden by the name-derived default.
func TestExplicitWebhookPathWins(t *testing.T) {
	ch := newTestChannel(t, "otherbrand", &config.NuestraSettings{WebhookPath: "/custom/inbound"})
	assert.Equal(t, "/custom/inbound", ch.WebhookPath())
	assert.Equal(t, "/health/otherbrand", ch.HealthPath(), "health path stays name-derived")
}

// TestBrandsDoNotCollide is the regression guard: two instances in one process
// must not share a webhook or health path.
func TestBrandsDoNotCollide(t *testing.T) {
	mf := newTestChannel(t, config.ChannelMagicForm, &config.NuestraSettings{})
	ob := newTestChannel(t, "otherbrand", &config.NuestraSettings{})

	assert.NotEqual(t, mf.WebhookPath(), ob.WebhookPath())
	assert.NotEqual(t, mf.HealthPath(), ob.HealthPath())
}

// TestHealthHandlerReportsChannelName verifies the health body identifies the
// brand that answered, rather than always reporting "magicform".
func TestHealthHandlerReportsChannelName(t *testing.T) {
	ch := newTestChannel(t, "otherbrand", &config.NuestraSettings{})

	rec := httptest.NewRecorder()
	ch.HealthHandler(rec, httptest.NewRequest(http.MethodGet, "/health/otherbrand", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, "otherbrand", body["channel"])
}

// TestPathNameFallback covers an instance whose name was never set: it keeps the
// legacy magicform paths rather than emitting "/hooks/".
func TestPathNameFallback(t *testing.T) {
	bc := &config.Channel{Type: config.ChannelNuestra, Enabled: true}
	ch, err := NewMagicFormChannel(bc, &config.NuestraSettings{}, t.TempDir(), bus.NewMessageBus())
	require.NoError(t, err)

	assert.Equal(t, "/hooks/magicform", ch.WebhookPath())
	assert.Equal(t, "/health/magicform", ch.HealthPath())
}
