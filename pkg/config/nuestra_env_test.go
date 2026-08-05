package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNuestraEnvPrefix(t *testing.T) {
	cases := map[string]string{
		"magicform": "PICOCLAW_CHANNELS_MAGICFORM_",
		"nuestra":   "PICOCLAW_CHANNELS_NUESTRA_",
		"brand-two": "PICOCLAW_CHANNELS_BRAND_TWO_",
		"brand.two": "PICOCLAW_CHANNELS_BRAND_TWO_",
	}
	for in, want := range cases {
		assert.Equalf(t, want, nuestraEnvPrefix(in), "nuestraEnvPrefix(%q)", in)
	}
}

// TestNuestraEnvOverridesConfig verifies a deployment can keep the token out of
// config.json and inject it from the environment, which is how the container
// deploy is wired.
func TestNuestraEnvOverridesConfig(t *testing.T) {
	t.Setenv("PICOCLAW_CHANNELS_MAGICFORM_TOKEN", "token-from-env")
	t.Setenv("PICOCLAW_CHANNELS_MAGICFORM_BACKEND_URL", "https://env.example")
	t.Setenv("PICOCLAW_CHANNELS_MAGICFORM_ALLOW_FROM", "a, b ,, c")

	channels := ChannelsConfig{
		"magicform": {
			Type:     ChannelMagicForm,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/magicform"}`),
		},
	}
	require.NoError(t, InitChannelList(channels))

	decoded, err := channels["magicform"].GetDecoded()
	require.NoError(t, err)
	s := decoded.(*NuestraSettings)

	assert.Equal(t, "token-from-env", s.Token.String())
	assert.Equal(t, "https://env.example", s.BackendURL)
	assert.Equal(t, "/hooks/magicform", s.WebhookPath, "config value kept when no env override")
	assert.Equal(t, []string{"a", "b", "c"}, []string(s.AllowFrom), "blank entries dropped")
}

// TestNuestraEnvIsPerInstance is the regression guard. With type-tag binding,
// one brand's env vars were stamped onto every configured brand, overwriting
// each brand's own token and webhook path.
func TestNuestraEnvIsPerInstance(t *testing.T) {
	t.Setenv("PICOCLAW_CHANNELS_MAGICFORM_TOKEN", "magicform-secret")
	t.Setenv("PICOCLAW_CHANNELS_MAGICFORM_WEBHOOK_PATH", "/hooks/magicform")
	t.Setenv("PICOCLAW_CHANNELS_OTHERBRAND_TOKEN", "otherbrand-secret")

	channels := ChannelsConfig{
		"magicform": {
			Type:     ChannelMagicForm,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/magicform"}`),
		},
		"otherbrand": {
			Type:     ChannelNuestra,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/otherbrand"}`),
		},
	}
	require.NoError(t, InitChannelList(channels))

	mf, err := channels["magicform"].GetDecoded()
	require.NoError(t, err)
	ob, err := channels["otherbrand"].GetDecoded()
	require.NoError(t, err)

	mfs, obs := mf.(*NuestraSettings), ob.(*NuestraSettings)

	assert.Equal(t, "magicform-secret", mfs.Token.String())
	assert.Equal(t, "/hooks/magicform", mfs.WebhookPath)

	// The leak: otherbrand previously inherited both of these from magicform.
	assert.Equal(t, "otherbrand-secret", obs.Token.String(), "each brand must keep its own token")
	assert.Equal(t, "/hooks/otherbrand", obs.WebhookPath, "one brand must not overwrite another's webhook path")
}

// TestNuestraEnvUsesChannelKeyNotBrand verifies nothing is hardcoded to the
// magicform brand: a channel keyed "nuestra" binds PICOCLAW_CHANNELS_NUESTRA_*.
func TestNuestraEnvUsesChannelKeyNotBrand(t *testing.T) {
	t.Setenv("PICOCLAW_CHANNELS_NUESTRA_TOKEN", "nuestra-secret")
	t.Setenv("PICOCLAW_CHANNELS_MAGICFORM_TOKEN", "should-not-apply")

	channels := ChannelsConfig{
		"nuestra": {
			Type:     ChannelNuestra,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/nuestra"}`),
		},
	}
	require.NoError(t, InitChannelList(channels))

	decoded, err := channels["nuestra"].GetDecoded()
	require.NoError(t, err)
	s := decoded.(*NuestraSettings)

	assert.Equal(t, "nuestra-secret", s.Token.String())
}

// TestNuestraEnvNeutralFallback covers the common deployment: one brand per
// container, configured with the brand-neutral vars. The channel is keyed
// "magicform" but the environment never names that brand.
func TestNuestraEnvNeutralFallback(t *testing.T) {
	t.Setenv("PICOCLAW_CHANNELS_NUESTRA_TOKEN", "neutral-secret")
	t.Setenv("PICOCLAW_CHANNELS_NUESTRA_BACKEND_URL", "https://neutral.example")
	t.Setenv("PICOCLAW_CHANNELS_NUESTRA_ALLOW_FROM", "x,y")

	channels := ChannelsConfig{
		"magicform": {
			Type:     ChannelMagicForm,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/magicform"}`),
		},
	}
	require.NoError(t, InitChannelList(channels))

	decoded, err := channels["magicform"].GetDecoded()
	require.NoError(t, err)
	s := decoded.(*NuestraSettings)

	assert.Equal(t, "neutral-secret", s.Token.String())
	assert.Equal(t, "https://neutral.example", s.BackendURL)
	assert.Equal(t, []string{"x", "y"}, []string(s.AllowFrom))
	assert.Equal(t, "/hooks/magicform", s.WebhookPath, "config value kept when neither env form is set")
}

// TestNuestraEnvKeyedBeatsNeutral pins the precedence rule: when a brand sets
// its own variable, the neutral fallback must not override it.
func TestNuestraEnvKeyedBeatsNeutral(t *testing.T) {
	t.Setenv("PICOCLAW_CHANNELS_NUESTRA_TOKEN", "neutral-secret")
	t.Setenv("PICOCLAW_CHANNELS_MAGICFORM_TOKEN", "keyed-secret")

	channels := ChannelsConfig{
		"magicform": {
			Type:     ChannelMagicForm,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/magicform"}`),
		},
	}
	require.NoError(t, InitChannelList(channels))

	decoded, err := channels["magicform"].GetDecoded()
	require.NoError(t, err)
	s := decoded.(*NuestraSettings)

	assert.Equal(t, "keyed-secret", s.Token.String(), "key-scoped var must win over the neutral fallback")
}

// TestNuestraEnvNeutralWithMultipleBrands guards the mixed case: the neutral
// var supplies a shared default while a brand overrides just its own token.
// Without per-instance resolution this collapsed to one value for both.
func TestNuestraEnvNeutralWithMultipleBrands(t *testing.T) {
	t.Setenv("PICOCLAW_CHANNELS_NUESTRA_BACKEND_URL", "https://shared.example")
	t.Setenv("PICOCLAW_CHANNELS_MAGICFORM_TOKEN", "magicform-secret")
	t.Setenv("PICOCLAW_CHANNELS_OTHERBRAND_TOKEN", "otherbrand-secret")

	channels := ChannelsConfig{
		"magicform": {
			Type:     ChannelMagicForm,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/magicform"}`),
		},
		"otherbrand": {
			Type:     ChannelNuestra,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/otherbrand"}`),
		},
	}
	require.NoError(t, InitChannelList(channels))

	mf, err := channels["magicform"].GetDecoded()
	require.NoError(t, err)
	ob, err := channels["otherbrand"].GetDecoded()
	require.NoError(t, err)
	mfs, obs := mf.(*NuestraSettings), ob.(*NuestraSettings)

	// Shared default reaches both brands.
	assert.Equal(t, "https://shared.example", mfs.BackendURL)
	assert.Equal(t, "https://shared.example", obs.BackendURL)

	// Per-brand secrets stay distinct.
	assert.Equal(t, "magicform-secret", mfs.Token.String())
	assert.Equal(t, "otherbrand-secret", obs.Token.String())
	assert.Equal(t, "/hooks/magicform", mfs.WebhookPath)
	assert.Equal(t, "/hooks/otherbrand", obs.WebhookPath)
}

// TestNuestraEnvChannelKeyedNuestra covers the prefix collision: for a channel
// keyed "nuestra" the scoped and neutral prefixes are the same string, so the
// lookup must still resolve rather than double-apply or miss.
func TestNuestraEnvChannelKeyedNuestra(t *testing.T) {
	t.Setenv("PICOCLAW_CHANNELS_NUESTRA_TOKEN", "only-secret")

	channels := ChannelsConfig{
		"nuestra": {
			Type:     ChannelNuestra,
			Enabled:  true,
			Settings: RawNode(`{"webhook_path":"/hooks/nuestra"}`),
		},
	}
	require.NoError(t, InitChannelList(channels))

	decoded, err := channels["nuestra"].GetDecoded()
	require.NoError(t, err)
	s := decoded.(*NuestraSettings)

	assert.Equal(t, nuestraEnvNeutralPrefix, nuestraEnvPrefix("nuestra"), "prefixes collide for this key")
	assert.Equal(t, "only-secret", s.Token.String())
}
