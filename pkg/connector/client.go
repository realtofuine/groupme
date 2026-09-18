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
	"sync"

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

	// pollCancel stops the REST polling loop started by Connect (see
	// poll.go). Guarded by pollMu since Connect/Disconnect can race with
	// bridgev2's own reconnect logic.
	pollMu     sync.Mutex
	pollCancel context.CancelFunc
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

	// gc.conn.SubscribeToUser blocks on wray's Bayeux handshake, which
	// retries internally with its own backoff and only returns once it
	// succeeds -- there is no timeout. Against a degraded/blocked
	// push.groupme.com this has been observed to block for over an hour
	// straight (see NOTES.md "Faye/Bayeux push connection reliability").
	// Previously this call sat directly in Connect before chat sync was
	// kicked off, which meant a dead Faye server silently prevented
	// *everything* below it -- including the initial chat sync and, now,
	// REST polling (poll.go) -- from ever starting, even though neither
	// actually depends on Faye. Run it in the background instead so Faye
	// is purely an optional low-latency accelerator: when it's up, push
	// events still get delivered near-instantly via HandleTextMessage
	// etc; when it's down (as observed live), REST polling is the actual
	// delivery mechanism and no longer waits on it.
	go func() {
		if err := gc.conn.SubscribeToUser(ctx, groupme.ID(gc.Meta.GMID), gc.Meta.Token); err != nil {
			log.Err(err).Msg("Failed to subscribe to GroupMe push channel")
		}
	}()

	gc.connected = true
	gc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})

	// Portals are otherwise only ever created reactively from live push
	// events (see handlegroupme.go); without this, a freshly logged-in user
	// gets zero portals until something happens to trigger a push. Run
	// asynchronously (and with a context detached from the one passed to
	// Connect) so a slow/large sync doesn't hold up Connect's caller -- some
	// callers (e.g. the unknown-error reconnect path) invoke Connect
	// synchronously. See sync.go.
	go gc.syncChats(context.WithoutCancel(ctx))

	// REST polling fallback (poll.go): periodically re-lists chats and
	// fetches new messages via the REST API, independent of whether Faye
	// ever connects. This is what actually keeps messages flowing while
	// Faye is degraded/down, and is harmless to run alongside a working
	// Faye connection since bridgev2 dedupes incoming messages by ID
	// (see poll.go for details). Its lifetime is tied to this Connect
	// call via pollCancel, stopped in Disconnect.
	gc.pollMu.Lock()
	if gc.pollCancel != nil {
		// Guard against a stray second Connect without an intervening
		// Disconnect (shouldn't normally happen, but would otherwise leak
		// a duplicate poll loop hitting the REST API twice as often).
		gc.pollCancel()
	}
	pollCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	gc.pollCancel = cancel
	gc.pollMu.Unlock()
	go gc.pollMessages(pollCtx)
}

func (gc *GMClient) Disconnect() {
	// The wray Faye client doesn't currently expose an explicit disconnect;
	// dropping the reference lets the listener goroutines exit once the
	// underlying HTTP long-poll requests fail. See NOTES.md.
	gc.conn = nil
	gc.connected = false

	gc.pollMu.Lock()
	if gc.pollCancel != nil {
		gc.pollCancel()
		gc.pollCancel = nil
	}
	gc.pollMu.Unlock()
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
