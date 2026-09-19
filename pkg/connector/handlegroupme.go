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

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

// This file implements groupme.HandlerAll, converting GroupMe's real-time
// push events (delivered over the Faye/Bayeux channel via pkg/groupmeext)
// into bridgev2 remote events. It is a port of the equivalent logic that
// used to live in user.go/portal.go before the bridgev2 migration.

func (gc *GMClient) HandleError(err error) {
	gc.UserLogin.Log.Err(err).Msg("Error from GroupMe push subscription")
}

func (gc *GMClient) HandleTextMessage(msg groupme.Message) {
	portalKey := gc.portalKeyForMessage(&msg)
	sender := bridgev2.EventSender{
		IsFromMe: msg.UserID == groupme.ID(gc.Meta.GMID),
		Sender:   MakeUserID(msg.UserID),
	}

	// Every GroupMe message carries the sender's name/avatar as of when it
	// was sent (msg.Name / msg.AvatarURL). GetChatInfo's group-member sync
	// (chatinfo.go) only knows about *current* group members via their
	// per-group nickname, so anyone who has since left a group -- or whose
	// membership sync hasn't run yet -- would otherwise only ever get a
	// raw-numeric-ID ghost name. Opportunistically refresh the ghost here
	// too, on every message, as a second source that also covers former
	// members. Best-effort and non-blocking: message delivery must not wait
	// on this.
	if msg.Name != "" && msg.UserID != groupme.ID(gc.Meta.GMID) {
		go func(gmid groupme.ID, name, avatarURL string) {
			ctx := context.Background()
			ghost, err := gc.Main.br.GetGhostByID(ctx, MakeUserID(gmid))
			if err != nil {
				gc.UserLogin.Log.Warn().Err(err).Str("gmid", string(gmid)).
					Msg("Failed to get ghost for opportunistic name refresh from message")
				return
			}
			ghost.UpdateInfo(ctx, &bridgev2.UserInfo{
				Name:   ptr.Ptr(name),
				Avatar: avatarFor(avatarURL),
			})
		}(msg.UserID, msg.Name, msg.AvatarURL)
	}

	gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.Message[*groupme.Message]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    portalKey,
			Sender:       sender,
			CreatePortal: true,
			Timestamp:    msg.CreatedAt.ToTime(),
		},
		ID:   MakeMessageID(msg.ID),
		Data: &msg,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data *groupme.Message) (*bridgev2.ConvertedMessage, error) {
			return convertGroupMeMessage(ctx, portal, intent, data)
		},
	})
}

// convertGroupMeMessage builds the Matrix message parts for an incoming
// GroupMe message, including any attachments. Currently supported
// attachment types: image. Other types (video, file, location) from the
// legacy bridge have not been ported yet, see NOTES.md.
func convertGroupMeMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *groupme.Message) (*bridgev2.ConvertedMessage, error) {
	cm := &bridgev2.ConvertedMessage{}

	for i, att := range msg.Attachments {
		if att.Type != "image" {
			continue
		}
		imgData, mime, err := groupmeext.DownloadImage(att.URL)
		if err != nil {
			continue
		}
		mxc, file, err := intent.UploadMedia(ctx, portal.MXID, *imgData, "image", mime)
		if err != nil {
			continue
		}
		content := &event.MessageEventContent{
			MsgType: event.MsgImage,
			Body:    "image",
			Info: &event.FileInfo{
				MimeType: mime,
				Size:     len(*imgData),
			},
		}
		if file != nil {
			content.File = file
		} else {
			content.URL = mxc
		}
		cm.Parts = append(cm.Parts, &bridgev2.ConvertedMessagePart{
			ID:      networkid.PartID(fmt.Sprintf("attachment-%d", i)),
			Type:    event.EventMessage,
			Content: content,
		})
	}

	if len(msg.Text) > 0 || len(cm.Parts) == 0 {
		cm.Parts = append(cm.Parts, &bridgev2.ConvertedMessagePart{
			ID:   "",
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    msg.Text,
			},
		})
	}

	return cm, nil
}

// HandleLike is called when GroupMe reports that a message's reactions
// changed. GroupMe doesn't tell us who added/removed which reaction, just
// the resulting state, so this is bridged as a full reaction resync for
// the message.
//
// GroupMe now supports full per-emoji reactions (confirmed live against
// the real API: msg.Reactions is a list of {emoji code, user_ids} pairs,
// e.g. a "\U0001F44D" (👍) entry with its own reactor list, separate from
// any "❤️" (❤️) entry on the same message). msg.Reactions is a
// local addition to the pinned groupme-lib dependency, which predates this
// GroupMe feature entirely -- see thirdparty/groupme-lib/json.go.
// FavoritedBy (the older, single-undifferentiated-like field) is kept only
// as a fallback for payloads that might not carry the newer field (e.g.
// possibly some live-push payload shapes, unverified) so a like is never
// silently dropped -- but it can only ever be represented as a generic ❤,
// since it doesn't say which emoji was actually used.
func (gc *GMClient) HandleLike(msg groupme.Message) {
	portalKey := gc.portalKeyForMessage(&msg)
	users := make(map[networkid.UserID]*bridgev2.ReactionSyncUser)

	if len(msg.Reactions) > 0 {
		for _, r := range msg.Reactions {
			if r.Code == "" {
				continue
			}
			for _, userIDStr := range r.UserIDs {
				uid := MakeUserID(groupme.ID(userIDStr))
				u, ok := users[uid]
				if !ok {
					u = &bridgev2.ReactionSyncUser{HasAllReactions: true}
					users[uid] = u
				}
				u.Reactions = append(u.Reactions, &bridgev2.BackfillReaction{
					Sender: bridgev2.EventSender{
						IsFromMe: groupme.ID(userIDStr) == groupme.ID(gc.Meta.GMID),
						Sender:   uid,
					},
					Emoji: r.Code,
				})
			}
		}
	} else {
		for _, userIDStr := range msg.FavoritedBy {
			uid := MakeUserID(groupme.ID(userIDStr))
			users[uid] = &bridgev2.ReactionSyncUser{
				HasAllReactions: true,
				Reactions: []*bridgev2.BackfillReaction{{
					Sender: bridgev2.EventSender{
						IsFromMe: groupme.ID(userIDStr) == groupme.ID(gc.Meta.GMID),
						Sender:   uid,
					},
					Emoji: "❤",
				}},
			}
		}
	}

	gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReactionSync,
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		TargetMessage: MakeMessageID(msg.ID),
		Reactions: &bridgev2.ReactionSyncData{
			Users:       users,
			HasAllUsers: true,
		},
	})
}

func (gc *GMClient) HandleJoin(id groupme.ID) {
	gc.resyncGroup(id)
}

func (gc *GMClient) HandleGroupName(group groupme.ID, newName string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleGroupTopic(group groupme.ID, newTopic string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleGroupAvatar(group groupme.ID, newAvatar string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleLikeIcon(group groupme.ID, packID, packIndex int, typ string) {
	// Custom "like" icons aren't represented on the Matrix side yet.
}

func (gc *GMClient) HandleNewNickname(group, user groupme.ID, name string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleNewAvatarInGroup(group, user groupme.ID, url string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleMembers(group groupme.ID, members []groupme.Member, added bool) {
	gc.resyncGroup(group)
}

// resyncGroup queues a full chat-info resync for a GroupMe group. Several
// push events (name/topic/avatar/membership changes) don't carry enough
// information to apply an incremental update, so the legacy bridge always
// refetched the whole group; this preserves that behavior.
func (gc *GMClient) resyncGroup(id groupme.ID) {
	gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatResync,
			PortalKey: gc.portalKeyForGroup(id),
			Timestamp: time.Now(),
		},
		GetChatInfoFunc: gc.GetChatInfo,
	})
}
