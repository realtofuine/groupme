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

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

// GMClient implements [bridgev2.NetworkAPI] for a single logged-in GroupMe
// account. It also implements groupme.HandlerAll so it can be registered
// directly on the push subscription (see handlegroupme.go).
type GMClient struct {
	Main      *GMConnector
	UserLogin *bridgev2.UserLogin
	Meta      *UserLoginMetadata
	Client    *groupmeext.Client

	conn      *groupme.PushSubscription
	connected bool
}

var _ bridgev2.NetworkAPI = (*GMClient)(nil)
var _ groupme.HandlerAll = (*GMClient)(nil)

func (gc *GMConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta, ok := login.Metadata.(*UserLoginMetadata)
	if !ok || meta == nil {
		meta = &UserLoginMetadata{}
		login.Metadata = meta
	}
	gcli := &GMClient{
		Main:      gc,
		UserLogin: login,
		Meta:      meta,
	}
	if meta.Token != "" {
		gcli.Client = groupmeext.NewClient(meta.Token)
	}
	login.Client = gcli
	return nil
}

func (gc *GMClient) Connect(ctx context.Context) {
	log := gc.UserLogin.Log
	if gc.Client == nil || gc.Meta.Token == "" {
		gc.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "groupme-not-logged-in",
		})
		return
	}

	sub := groupme.NewPushSubscription(ctx)
	gc.conn = &sub
	fayeClient := groupmeext.NewFayeClient(log)
	gc.conn.StartListening(ctx, fayeClient)
	gc.conn.AddFullHandler(gc)

	err := gc.conn.SubscribeToUser(ctx, groupme.ID(gc.Meta.GMID), gc.Meta.Token)
	if err != nil {
		log.Err(err).Msg("Failed to subscribe to GroupMe push channel")
		gc.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      "groupme-connect-failed",
			Message:    err.Error(),
		})
		return
	}
	gc.connected = true
	gc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
}

func (gc *GMClient) Disconnect() {
	// The wray Faye client doesn't currently expose an explicit disconnect;
	// dropping the reference lets the listener goroutines exit once the
	// underlying HTTP long-poll requests fail. See NOTES.md.
	gc.conn = nil
	gc.connected = false
}

func (gc *GMClient) IsLoggedIn() bool {
	return gc.Client != nil && gc.Meta.Token != ""
}

func (gc *GMClient) LogoutRemote(ctx context.Context) {
	gc.Disconnect()
	gc.Meta.Token = ""
	gc.Client = nil
}

func (gc *GMClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return string(userID) == gc.Meta.GMID
}

// portalKeyForGroup returns the portal key for a GroupMe group chat.
func (gc *GMClient) portalKeyForGroup(groupID groupme.ID) networkid.PortalKey {
	return networkid.PortalKey{
		ID:       MakeGroupPortalID(groupID),
		Receiver: gc.UserLogin.ID,
	}
}

// portalKeyForDM returns the portal key for a GroupMe direct message with
// the given other user.
func (gc *GMClient) portalKeyForDM(otherUserID groupme.ID) networkid.PortalKey {
	return networkid.PortalKey{
		ID:       MakeDMPortalID(otherUserID),
		Receiver: gc.UserLogin.ID,
	}
}

// portalKeyForMessage determines the portal key that an incoming push
// message belongs to. GroupMe group messages carry GroupID; direct messages
// only carry ConversationID/ChatID plus the sender/recipient user IDs.
func (gc *GMClient) portalKeyForMessage(msg *groupme.Message) networkid.PortalKey {
	if len(msg.GroupID) > 0 {
		return gc.portalKeyForGroup(msg.GroupID)
	}
	other := msg.RecipientID
	if msg.UserID != groupme.ID(gc.Meta.GMID) {
		other = msg.UserID
	}
	return gc.portalKeyForDM(other)
}

func (gc *GMClient) fatalError(err error, code status.BridgeStateErrorCode) {
	gc.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateUnknownError,
		Error:      code,
		Message:    err.Error(),
	})
}
