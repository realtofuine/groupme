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
	"fmt"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme-lib"
)

// Portal IDs are namespaced with a "group:" or "dm:" prefix followed by the
// GroupMe group ID or the other participant's GroupMe user ID (for DMs).

func MakeGroupPortalID(groupID groupme.ID) networkid.PortalID {
	return networkid.PortalID(fmt.Sprintf("group:%s", groupID))
}

func MakeDMPortalID(otherUserID groupme.ID) networkid.PortalID {
	return networkid.PortalID(fmt.Sprintf("dm:%s", otherUserID))
}

// ParsePortalID splits a networkid.PortalID into its type and the GroupMe ID.
func ParsePortalID(id networkid.PortalID) (PortalType, groupme.ID) {
	parts := strings.SplitN(string(id), ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return PortalType(parts[0]), groupme.ID(parts[1])
}

// DMConversationID builds the conversation ID GroupMe uses for a DM between
// two users in endpoints like /messages/:conversation_id/:message_id/like:
// both user IDs joined by "+", numerically smaller first (e.g.
// "87270184+106452543"). Confirmed against the conversation_id field of
// every DM returned by /v3/chats on 2026-09-24 -- all 55 were ordered this
// way regardless of which side is the logged-in user. Passing just one
// user's ID (what this bridge previously did) 404s.
func DMConversationID(a, b groupme.ID) groupme.ID {
	an, aErr := strconv.ParseUint(string(a), 10, 64)
	bn, bErr := strconv.ParseUint(string(b), 10, 64)
	if aErr == nil && bErr == nil && an > bn {
		a, b = b, a
	}
	return a + "+" + b
}

func MakeUserID(gmid groupme.ID) networkid.UserID {
	return networkid.UserID(gmid)
}

func ParseUserID(id networkid.UserID) groupme.ID {
	return groupme.ID(id)
}

func MakeMessageID(gmid groupme.ID) networkid.MessageID {
	return networkid.MessageID(gmid)
}

func ParseMessageID(id networkid.MessageID) groupme.ID {
	return groupme.ID(id)
}
