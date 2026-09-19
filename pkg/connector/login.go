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

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme/pkg/groupmeext"
)

const LoginFlowIDToken = "access-token"

func (gc *GMConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "GroupMe access token",
		Description: "Log in with a GroupMe developer access token",
		ID:          LoginFlowIDToken,
	}}
}

func (gc *GMConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != LoginFlowIDToken {
		return nil, fmt.Errorf("unknown login flow ID %q", flowID)
	}
	return &GMLogin{User: user, Main: gc}, nil
}

type GMLogin struct {
	User *bridgev2.User
	Main *GMConnector
}

var _ bridgev2.LoginProcessUserInput = (*GMLogin)(nil)

func (gl *GMLogin) Cancel() {}

func (gl *GMLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "fi.mau.groupme.login.enter_token",
		Instructions: "Enter your GroupMe access token. You can find it by logging into dev.groupme.com and checking the URL after clicking \"Access Token\".",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldTypeToken,
				ID:          "token",
				Name:        "Access token",
				Description: "GroupMe API access token",
			}},
		},
	}, nil
}

func (gl *GMLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	token := input["token"]
	if token == "" {
		return nil, fmt.Errorf("access token is required")
	}
	client := groupmeext.NewClient(token)
	me, err := client.MyUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate access token: %w", err)
	}

	loginID := networkid.UserLoginID(me.ID)
	ul, err := gl.User.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: me.Name,
		Metadata: &UserLoginMetadata{
			Token: token,
			GMID:  string(me.ID),
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to save login: %w", err)
	}
	go ul.Client.Connect(ul.Log.WithContext(context.Background()))

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "fi.mau.groupme.login.complete",
		Instructions: fmt.Sprintf("Successfully logged in as %s", me.Name),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: loginID,
			UserLogin:   ul,
		},
	}, nil
}
