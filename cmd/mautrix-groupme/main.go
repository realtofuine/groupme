// mautrix-groupme - A Matrix-GroupMe puppeting bridge.
// Copyright (C) 2022 Sumner Evans, Karmanyaah Malhotra
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

package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/beeper/groupme/pkg/connector"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var c = &connector.GMConnector{}
var m = mxmain.BridgeMain{
	Name:        "mautrix-groupme",
	Description: "A Matrix-GroupMe puppeting bridge.",
	URL:         "https://github.com/beeper/groupme",
	Version:     "0.2.0",
	Connector:   c,
}

func main() {
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
