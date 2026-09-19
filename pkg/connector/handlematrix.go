// mautrix-groupme - A Matrix-GroupMe puppeting bridge.
// Copyright (C) 2026 The mautrix-groupme contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"github.com/beeper/groupme-lib"
)

var _ bridgev2.ReactionHandlingNetworkAPI = (*GMClient)(nil)

// GroupMe only supports a single reaction per user per message (reacting
// again overwrites the previous one, confirmed in the community docs -- see
// thirdparty/groupme-lib/likes_api.go), so MaxReactions is fixed at 1.
// The emoji itself, however, is NOT fixed: GroupMe supports 15 specific
// unicode reactions (groupme.UnicodeLikeIcons), not just a generic heart --
// use whichever one the Matrix reaction actually used, falling back to the
// heart only if it's not one GroupMe accepts (e.g. an arbitrary custom
// Matrix emoji with no GroupMe equivalent).
func (gc *GMClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emoji := msg.Content.RelatesTo.Key
	if !groupme.UnicodeLikeIcons[emoji] {
		emoji = "❤️"
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     MakeUserID(groupme.ID(gc.Meta.GMID)),
		Emoji:        emoji,
		MaxReactions: 1,
	}, nil
}

// HandleMatrixMessage bridges an outgoing Matrix message to GroupMe.
//
// Only plain text (and emote/notice) messages are supported for now; the
// legacy bridge never implemented outgoing media either (see NOTES.md), so
// this preserves the previous feature set while running on bridgev2.
func (gc *GMClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	content := msg.Content
	text := content.Body
	if content.Format == event.FormatHTML {
		text = format.HTMLToText(content.FormattedBody)
	}
	if content.MsgType == event.MsgEmote {
		text = "/me " + text
	}

	portalType, gmid := ParsePortalID(msg.Portal.ID)
	out := &groupme.Message{Text: text}

	var sent *groupme.Message
	var err error
	switch portalType {
	case PortalTypeGroup:
		out.GroupID = gmid
		sent, err = gc.Client.CreateMessage(ctx, gmid, out)
	case PortalTypeDM:
		out.RecipientID = gmid
		sent, err = gc.Client.CreateDirectMessage(ctx, out)
	default:
		return nil, fmt.Errorf("unknown portal type for %s", msg.Portal.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send message to GroupMe: %w", err)
	}

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        MakeMessageID(sent.ID),
			MXID:      msg.Event.ID,
			Room:      msg.Portal.PortalKey,
			SenderID:  MakeUserID(groupme.ID(gc.Meta.GMID)),
			Timestamp: time.UnixMilli(msg.Event.Timestamp),
		},
	}, nil
}

// HandleMatrixReaction bridges a Matrix reaction to a GroupMe "like".
func (gc *GMClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	portalType, gmid := ParsePortalID(msg.Portal.ID)
	conversationID := gmid
	if portalType == PortalTypeDM {
		conversationID = groupme.ID(gc.Meta.GMID)
	}
	messageID := ParseMessageID(msg.TargetMessage.ID)
	var emoji string
	if msg.PreHandleResp != nil {
		emoji = msg.PreHandleResp.Emoji
	}
	err := gc.Client.CreateLike(ctx, conversationID, messageID, emoji)
	if err != nil {
		return nil, fmt.Errorf("failed to like GroupMe message: %w", err)
	}
	return &database.Reaction{}, nil
}

// HandleMatrixReactionRemove bridges removing a Matrix reaction to
// un-liking the GroupMe message.
func (gc *GMClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	portalType, gmid := ParsePortalID(msg.Portal.ID)
	conversationID := gmid
	if portalType == PortalTypeDM {
		conversationID = groupme.ID(gc.Meta.GMID)
	}
	messageID := ParseMessageID(msg.TargetReaction.MessageID)
	err := gc.Client.DestroyLike(ctx, conversationID, messageID)
	if err != nil {
		return fmt.Errorf("failed to unlike GroupMe message: %w", err)
	}
	return nil
}
