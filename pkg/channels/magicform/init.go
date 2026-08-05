package magicform

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func init() {
	// Both channel types build the same channel: "nuestra" is the protocol,
	// "magicform" the original brand alias kept for deployed configs.
	channels.RegisterFactory(config.ChannelNuestra, newChannel)
	channels.RegisterFactory(config.ChannelMagicForm, newChannel)
}

func newChannel(
	channelName, channelType string,
	cfg *config.Config,
	b *bus.MessageBus,
) (channels.Channel, error) {
	bc := cfg.Channels[channelName]
	decoded, err := bc.GetDecoded()
	if err != nil {
		return nil, err
	}
	settings, ok := decoded.(*config.NuestraSettings)
	if !ok {
		return nil, channels.ErrSendFailed
	}
	ch, err := NewMagicFormChannel(bc, settings, cfg.Agents.Defaults.WorkspaceRoot, b)
	if err != nil {
		return nil, err
	}
	// NewMagicFormChannel seeds the base name with the legacy "magicform"
	// default, so every instance is renamed to its config key. Logs, security
	// warnings, and health routing all key off the channel name, and a channel
	// keyed "nuestra" or "otherbrand" reporting itself as "magicform" would be
	// indistinguishable from the brand instance.
	ch.SetName(channelName)
	return ch, nil
}
