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

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

func avatarFor(url string) *bridgev2.Avatar {
	if url == "" {
		return &bridgev2.Avatar{ID: "remove", Remove: true}
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(url),
		Get: func(ctx context.Context) ([]byte, error) {
			data, _, err := groupmeext.DownloadImage(url)
			if err != nil {
				return nil, err
			}
			return *data, nil
		},
	}
}

func (gc *GMClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	portalType, gmid := ParsePortalID(portal.ID)
	switch portalType {
	case PortalTypeGroup:
		group, err := gc.Client.ShowGroup(ctx, gmid)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch GroupMe group: %w", err)
		}
		members := &bridgev2.ChatMemberList{
			IsFull:    true,
			MemberMap: make(bridgev2.ChatMemberMap, len(group.Members)),
		}
		for _, m := range group.Members {
			isFromMe := m.UserID == groupme.ID(gc.Meta.GMID)
			members.MemberMap.Set(bridgev2.ChatMember{
				EventSender: bridgev2.EventSender{
					IsFromMe: isFromMe,
					Sender:   MakeUserID(m.UserID),
				},
				Membership: "join",
			})
		}
		roomType := database.RoomTypeDefault
		return &bridgev2.ChatInfo{
			Name:    ptr.Ptr(group.Name),
			Topic:   ptr.Ptr(group.Description),
			Avatar:  avatarFor(group.ImageURL),
			Members: members,
			Type:    &roomType,
		}, nil
	case PortalTypeDM:
		var other *groupme.User
		relations, err := gc.Client.IndexRelations(ctx)
		if err == nil {
			for _, u := range relations {
				if u.ID == gmid {
					other = u
					break
				}
			}
		}
		name := string(gmid)
		var avatarURL string
		if other != nil {
			name = other.Name
			avatarURL = other.AvatarURL
		}
		roomType := database.RoomTypeDM
		members := &bridgev2.ChatMemberList{
			IsFull: true,
			MemberMap: bridgev2.ChatMemberMap{
				MakeUserID(gmid): bridgev2.ChatMember{
					EventSender: bridgev2.EventSender{Sender: MakeUserID(gmid)},
					Membership:  "join",
				},
				MakeUserID(groupme.ID(gc.Meta.GMID)): bridgev2.ChatMember{
					EventSender: bridgev2.EventSender{IsFromMe: true, Sender: MakeUserID(groupme.ID(gc.Meta.GMID))},
					Membership:  "join",
				},
			},
			OtherUserID: MakeUserID(gmid),
		}
		return &bridgev2.ChatInfo{
			Name:    ptr.Ptr(name),
			Avatar:  avatarFor(avatarURL),
			Members: members,
			Type:    &roomType,
		}, nil
	default:
		return nil, fmt.Errorf("unknown portal type for %s", portal.ID)
	}
}

func (gc *GMClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	gmid := ParseUserID(ghost.ID)
	relations, err := gc.Client.IndexRelations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GroupMe relations: %w", err)
	}
	for _, u := range relations {
		if u.ID == gmid {
			return &bridgev2.UserInfo{
				Name:   ptr.Ptr(u.Name),
				Avatar: avatarFor(u.AvatarURL),
			}, nil
		}
	}
	if gmid == groupme.ID(gc.Meta.GMID) {
		me, err := gc.Client.MyUser(ctx)
		if err == nil {
			return &bridgev2.UserInfo{
				Name:   ptr.Ptr(me.Name),
				Avatar: avatarFor(me.AvatarURL),
			}, nil
		}
	}
	return &bridgev2.UserInfo{Name: ptr.Ptr(string(gmid))}, nil
}
