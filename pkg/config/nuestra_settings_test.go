package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNuestraChannelTypeAliases verifies both channel types decode into the
// same settings struct. "nuestra" is the Nuestra Agent platform protocol;
// "magicform" is the original brand alias and is a live contract with deployed
// tenants, so it must keep working unchanged.
func TestNuestraChannelTypeAliases(t *testing.T) {
	for _, typ := range []string{ChannelNuestra, ChannelMagicForm} {
		t.Run(typ, func(t *testing.T) {
			assert.Truef(t, isValidChannelType(typ), "channel type %q should be valid", typ)

			got := newChannelSettings(typ)
			_, ok := got.(*NuestraSettings)
			assert.Truef(t, ok, "newChannelSettings(%q) = %T, want *NuestraSettings", typ, got)
		})
	}
}

// TestMagicFormConfigStillDecodes pins the backward-compatibility guarantee:
// an existing `"type": "magicform"` config decodes through the full
// InitChannelList path with every field intact.
func TestMagicFormConfigStillDecodes(t *testing.T) {
	channels := ChannelsConfig{
		"magicform": {
			Type:    ChannelMagicForm,
			Enabled: true,
			Settings: RawNode(`{
				"token":          "tok-abc",
				"backend_url":    "https://backend.example",
				"webhook_path":   "/hooks/magicform",
				"workspace_root": "/srv/workspaces",
				"allow_from":     ["10.0.0.1"]
			}`),
		},
	}

	require.NoError(t, InitChannelList(channels))

	decoded, err := channels["magicform"].GetDecoded()
	require.NoError(t, err)

	settings, ok := decoded.(*NuestraSettings)
	require.True(t, ok, "magicform must decode into *NuestraSettings")

	assert.Equal(t, "tok-abc", settings.Token.String())
	assert.Equal(t, "https://backend.example", settings.BackendURL)
	assert.Equal(t, "/hooks/magicform", settings.WebhookPath)
	assert.Equal(t, "/srv/workspaces", settings.WorkspaceRoot)
	assert.Equal(t, []string{"10.0.0.1"}, []string(settings.AllowFrom))
}

// TestMagicFormSettingsIsAlias guards the alias itself. If MagicFormSettings
// ever became a distinct type, the type switch in the channel manager and any
// out-of-tree caller would silently stop matching.
func TestMagicFormSettingsIsAlias(t *testing.T) {
	var s MagicFormSettings
	_, ok := any(&s).(*NuestraSettings)
	assert.True(t, ok, "MagicFormSettings must be an alias for NuestraSettings, not a distinct type")
}
