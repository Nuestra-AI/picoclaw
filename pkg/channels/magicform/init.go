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
	// Instances named after their brand ("magicform", "otherbrand", ...) carry
	// that name; only the bare default type keeps the channel's own name.
	if channelName != config.ChannelMagicForm && channelName != config.ChannelNuestra {
		ch.SetName(channelName)
	}
	return ch, nil
}
