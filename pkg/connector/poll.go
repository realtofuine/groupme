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
	"errors"
	"sort"
	"time"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme-lib"
)

// This file implements REST-API polling as a resilient fallback/replacement
// for GroupMe's real-time Faye/Bayeux push connection (see handlegroupme.go
// and NOTES.md "Faye/Bayeux push connection reliability"). That connection
// has been observed failing persistently (504 Gateway Timeout on every
// handshake attempt, for over an hour of continuous retries against a live
// account) -- with no other way to learn about new messages, that meant
// nothing synced into Matrix at all: not messages from other people, and
// not even the logged-in user's own messages sent from the native GroupMe
// app.
//
// Design summary (see NOTES.md for the full writeup):
//   - Runs as its own goroutine per Connect() call (see client.go), on a
//     timer, independent of whether the Faye connection is up.
//   - Each tick re-lists the user's groups and DM chats via the same REST
//     calls used by the initial sync (gc.Client.IndexAllGroups /
//     IndexAllChats, see sync.go) -- so newly created chats are picked up
//     automatically, without needing a separately maintained chat list.
//   - For each chat, "last seen message ID" is read back from bridgev2's
//     own message store (DB.Message.GetLastNInPortal) instead of a new
//     table: that store already records the most recently bridged message
//     per portal (from either Faye or a previous poll) and survives
//     restarts, so there's no new persistent state to invent or keep in
//     sync.
//   - New messages are fed through GMClient.HandleTextMessage -- the exact
//     same conversion path used for live Faye push messages -- so polled
//     messages are bridged identically to pushed ones, including the
//     sender's own messages (GroupMe's message-list API returns those too,
//     with the same UserID/RecipientID shape as push payloads, so
//     HandleTextMessage's existing IsFromMe/portal-routing logic just
//     works without any special-casing here).
//   - Safe to run alongside a working Faye connection: bridgev2 core
//     dedupes incoming messages by ID before doing anything with them
//     (Portal.handleRemoteMessage -> DB.Message.GetAllPartsByID), so if
//     both Faye and polling observe the same message, whichever arrives
//     first wins and the other is a silent no-op.

// defaultPollIntervalSeconds is used when the config doesn't specify one
// (or specifies an invalid value). 20s is a reasonable default: GroupMe
// doesn't aggressively rate-limit lightly-polled read endpoints for a
// single personal account, but there's no reason to hammer it either.
const defaultPollIntervalSeconds = 20

// minPollIntervalSeconds is a floor on the configured interval, regardless
// of what the config says, so a typo (e.g. "1" instead of "10") can't turn
// this into a tight request loop against GroupMe's API.
const minPollIntervalSeconds = 10

// pollInterval returns the configured poll interval, clamped to a sane
// minimum and defaulted if unset.
func (gc *GMClient) pollInterval() time.Duration {
	secs := gc.Main.Config.Poll.IntervalSeconds
	if secs <= 0 {
		secs = defaultPollIntervalSeconds
	}
	if secs < minPollIntervalSeconds {
		secs = minPollIntervalSeconds
	}
	return time.Duration(secs) * time.Second
}

// pollMessages runs until ctx is cancelled (see Disconnect in client.go),
// periodically polling every known chat for new messages via REST.
func (gc *GMClient) pollMessages(ctx context.Context) {
	if !gc.Main.Config.Poll.Enabled {
		gc.UserLogin.Log.Info().Msg("REST message polling disabled by config")
		return
	}

	interval := gc.pollInterval()
	log := gc.UserLogin.Log.With().Str("action", "message poll").Dur("interval", interval).Logger()
	log.Info().Msg("Starting GroupMe REST message polling loop")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Debug().Msg("Stopping GroupMe REST message polling loop")
			return
		case <-ticker.C:
		}
		gc.pollOnce(ctx, log)
	}
}

// pollOnce runs a single poll pass over every known group and DM chat.
func (gc *GMClient) pollOnce(ctx context.Context, log zerolog.Logger) {
	groups, err := gc.Client.IndexAllGroups()
	if err != nil {
		log.Err(err).Msg("Failed to list groups while polling for new messages")
	} else {
		for _, group := range groups {
			if group == nil || len(group.ID) == 0 {
				continue
			}
			gc.pollChat(ctx, log, gc.portalKeyForGroup(group.ID), group.ID, false)
		}
	}

	if ctx.Err() != nil {
		return
	}

	chats, err := gc.Client.IndexAllChats()
	if err != nil {
		log.Err(err).Msg("Failed to list DM chats while polling for new messages")
	} else {
		for _, chat := range chats {
			if chat == nil || len(chat.OtherUser.ID) == 0 {
				continue
			}
			gc.pollChat(ctx, log, gc.portalKeyForDM(chat.OtherUser.ID), chat.OtherUser.ID, true)
		}
	}
}

// pollChat fetches and bridges any messages newer than the last one seen
// for a single chat (group or DM). chatID is the GroupMe group ID for
// groups, or the other participant's user ID for DMs.
func (gc *GMClient) pollChat(ctx context.Context, log zerolog.Logger, portalKey networkid.PortalKey, chatID groupme.ID, private bool) {
	last, err := gc.Main.br.DB.Message.GetLastNInPortal(ctx, portalKey, 1)
	if err != nil {
		log.Err(err).Str("chat_id", chatID.String()).Msg("Failed to look up last seen message ID for poll")
		return
	}
	var sinceID groupme.ID
	if len(last) > 0 {
		sinceID = ParseMessageID(last[0].ID)
	}
	// If sinceID is empty (no message has ever been bridged for this chat,
	// e.g. a portal created by initial sync that hasn't received anything
	// since), GroupMe's list endpoints just return their default page of
	// the most recent messages. That's not a real backfill implementation
	// (see NOTES.md "Backfill"), but it's a harmless, bounded side effect
	// that opportunistically gives a freshly created portal some recent
	// history instead of starting completely silent.

	var msgs []*groupme.Message
	if private {
		resp, err := gc.Client.IndexDirectMessages(ctx, chatID.String(), &groupme.IndexDirectMessagesQuery{SinceID: sinceID})
		if err != nil {
			logPollError(log, chatID, true, err)
			return
		}
		msgs = resp.Messages
	} else {
		resp, err := gc.Client.IndexMessages(ctx, chatID, &groupme.IndexMessagesQuery{AfterID: sinceID, Limit: 20})
		if err != nil {
			logPollError(log, chatID, false, err)
			return
		}
		msgs = resp.Messages
	}

	if len(msgs) > 0 {
		// Both endpoints are documented to return results newest-first when
		// no since/after cursor is given, and the DM endpoint's since_id
		// behavior isn't documented as strictly ascending either (unlike
		// the group endpoint's after_id, which is). Sort explicitly by
		// timestamp so messages are always bridged in chronological order
		// regardless of which case applies.
		sort.SliceStable(msgs, func(i, j int) bool {
			return msgs[i].CreatedAt.ToTime().Before(msgs[j].CreatedAt.ToTime())
		})

		for _, msg := range msgs {
			if msg == nil || len(msg.ID) == 0 {
				continue
			}
			// Same conversion path as live Faye push messages -- see
			// handlegroupme.go. bridgev2 core dedupes by message ID before
			// this does anything observable, so it's safe even if Faye
			// also delivers this same message around the same time.
			gc.HandleTextMessage(*msg)
		}
	}

	// Reaction/like changes on already-seen messages: the since/after
	// cursor fetch above only ever returns messages NEWER than the last
	// one bridged, so a like added to an older message -- the normal case,
	// since you react to something already sent -- is invisible to it.
	// This must run unconditionally, NOT after the `len(msgs) == 0` guard
	// below: "no new messages this tick" is the common case (a like on an
	// existing message doesn't produce a new message), so gating this on
	// that guard meant it silently never ran in exactly the case it exists
	// for. Confirmed live: an app-added like sat unsynced indefinitely
	// until this was moved above the guard.
	// Live push (HandleLike, handlegroupme.go) is otherwise the only path
	// that catches this, and it's been observed reconnecting roughly every
	// 45 seconds (see NOTES.md "Faye/Bayeux push connection reliability"),
	// so a like sent during one of those windows would otherwise never
	// arrive at all. Independently re-fetch the most recent page (no
	// cursor) every poll tick and resync reactions for each message via
	// the same full-resync path HandleLike already uses live; this is a
	// cheap, safe no-op when FavoritedBy hasn't changed.
	gc.pollRecentReactions(ctx, log, chatID, private)
}

// pollRecentReactions re-fetches the most recent page of messages for a
// chat (unconditionally, no since/after cursor) purely to catch
// FavoritedBy (like) changes on messages already bridged -- see the call
// site's comment in pollChat for why this is needed alongside the
// cursor-based new-message fetch.
func (gc *GMClient) pollRecentReactions(ctx context.Context, log zerolog.Logger, chatID groupme.ID, private bool) {
	var msgs []*groupme.Message
	if private {
		resp, err := gc.Client.IndexDirectMessages(ctx, chatID.String(), &groupme.IndexDirectMessagesQuery{})
		if err != nil {
			logPollError(log, chatID, true, err)
			return
		}
		msgs = resp.Messages
	} else {
		resp, err := gc.Client.IndexMessages(ctx, chatID, &groupme.IndexMessagesQuery{Limit: 20})
		if err != nil {
			logPollError(log, chatID, false, err)
			return
		}
		msgs = resp.Messages
	}
	for _, msg := range msgs {
		if msg == nil || len(msg.ID) == 0 {
			continue
		}
		gc.HandleLike(*msg)
	}
}

// logPollError logs a polling failure, downgrading GroupMe's "304 Not
// Modified" response (its documented way of saying "no messages found for
// this since_id/after_id filter", i.e. simply nothing new) to debug level
// instead of treating a normal empty poll as an error.
func logPollError(log zerolog.Logger, chatID groupme.ID, private bool, err error) {
	var meta *groupme.Meta
	if errors.As(err, &meta) && meta.Code == groupme.HTTPNotModified {
		log.Debug().Str("chat_id", chatID.String()).Bool("private", private).Msg("No new messages")
		return
	}
	log.Err(err).Str("chat_id", chatID.String()).Bool("private", private).Msg("Failed to poll for new messages")
}
