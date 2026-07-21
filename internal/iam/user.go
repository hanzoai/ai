// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package iam

import (
	"encoding/json"
	"fmt"
	"strings"
)

// User mirrors the Hanzo IAM server's User JSON model. The FULL field set is
// reproduced deliberately (not a slim projection): object/redact.go reflects
// over every field by its json tag as a fail-secure secret-redaction control,
// and it must see every credential field (password*, *Secret, *Token, totp,
// social-login ids, custom1..10, …) to zero it before a user is returned to a
// client. A slim User would silently narrow that control and break its test.
// The json tags are therefore load-bearing and match the server verbatim; the
// Casdoor xorm/db tags are dropped (ai has no ORM).
type User struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	DeletedTime string `json:"deletedTime"`

	Id                   string     `json:"id"`
	ExternalId           string     `json:"externalId"`
	Type                 string     `json:"type"`
	Password             string     `json:"password"`
	PasswordSalt         string     `json:"passwordSalt"`
	PasswordType         string     `json:"passwordType"`
	DisplayName          string     `json:"displayName"`
	FirstName            string     `json:"firstName"`
	LastName             string     `json:"lastName"`
	Avatar               string     `json:"avatar"`
	AvatarType           string     `json:"avatarType"`
	PermanentAvatar      string     `json:"permanentAvatar"`
	Email                string     `json:"email"`
	EmailVerified        bool       `json:"emailVerified"`
	Phone                string     `json:"phone"`
	CountryCode          string     `json:"countryCode"`
	Region               string     `json:"region"`
	Location             string     `json:"location"`
	Address              []string   `json:"address"`
	Addresses            []*Address `json:"addresses"`
	Affiliation          string     `json:"affiliation"`
	Title                string     `json:"title"`
	IdCardType           string     `json:"idCardType"`
	IdCard               string     `json:"idCard"`
	RealName             string     `json:"realName"`
	IsVerified           bool       `json:"isVerified"`
	Homepage             string     `json:"homepage"`
	Bio                  string     `json:"bio"`
	Tag                  string     `json:"tag"`
	Language             string     `json:"language"`
	Gender               string     `json:"gender"`
	Birthday             string     `json:"birthday"`
	Education            string     `json:"education"`
	Score                int        `json:"score"`
	Karma                int        `json:"karma"`
	Ranking              int        `json:"ranking"`
	Balance              float64    `json:"balance"`
	BalanceCredit        float64    `json:"balanceCredit"`
	Currency             string     `json:"currency"`
	BalanceCurrency      string     `json:"balanceCurrency"`
	IsDefaultAvatar      bool       `json:"isDefaultAvatar"`
	IsOnline             bool       `json:"isOnline"`
	IsAdmin              bool       `json:"isAdmin"`
	IsForbidden          bool       `json:"isForbidden"`
	IsDeleted            bool       `json:"isDeleted"`
	SignupApplication    string     `json:"signupApplication"`
	Hash                 string     `json:"hash"`
	PreHash              string     `json:"preHash"`
	RegisterType         string     `json:"registerType"`
	RegisterSource       string     `json:"registerSource"`
	AccessKey            string     `json:"accessKey"`
	AccessSecret         string     `json:"accessSecret"`
	AccessToken          string     `json:"accessToken"`
	OriginalToken        string     `json:"originalToken"`
	OriginalRefreshToken string     `json:"originalRefreshToken"`

	CreatedIp      string `json:"createdIp"`
	LastSigninTime string `json:"lastSigninTime"`
	LastSigninIp   string `json:"lastSigninIp"`

	GitHub          string `json:"github"`
	Google          string `json:"google"`
	QQ              string `json:"qq"`
	WeChat          string `json:"wechat"`
	Facebook        string `json:"facebook"`
	DingTalk        string `json:"dingtalk"`
	Weibo           string `json:"weibo"`
	Gitee           string `json:"gitee"`
	LinkedIn        string `json:"linkedin"`
	Wecom           string `json:"wecom"`
	Lark            string `json:"lark"`
	Gitlab          string `json:"gitlab"`
	Adfs            string `json:"adfs"`
	Baidu           string `json:"baidu"`
	Alipay          string `json:"alipay"`
	Infoflow        string `json:"infoflow"`
	Apple           string `json:"apple"`
	AzureAD         string `json:"azuread"`
	AzureADB2c      string `json:"azureadb2c"`
	Slack           string `json:"slack"`
	Steam           string `json:"steam"`
	Bilibili        string `json:"bilibili"`
	Okta            string `json:"okta"`
	Douyin          string `json:"douyin"`
	Kwai            string `json:"kwai"`
	Line            string `json:"line"`
	Amazon          string `json:"amazon"`
	Auth0           string `json:"auth0"`
	BattleNet       string `json:"battlenet"`
	Bitbucket       string `json:"bitbucket"`
	Box             string `json:"box"`
	CloudFoundry    string `json:"cloudfoundry"`
	Dailymotion     string `json:"dailymotion"`
	Deezer          string `json:"deezer"`
	DigitalOcean    string `json:"digitalocean"`
	Discord         string `json:"discord"`
	Dropbox         string `json:"dropbox"`
	EveOnline       string `json:"eveonline"`
	Fitbit          string `json:"fitbit"`
	Gitea           string `json:"gitea"`
	Heroku          string `json:"heroku"`
	InfluxCloud     string `json:"influxcloud"`
	Instagram       string `json:"instagram"`
	Intercom        string `json:"intercom"`
	Kakao           string `json:"kakao"`
	Lastfm          string `json:"lastfm"`
	Mailru          string `json:"mailru"`
	Meetup          string `json:"meetup"`
	MicrosoftOnline string `json:"microsoftonline"`
	Naver           string `json:"naver"`
	Nextcloud       string `json:"nextcloud"`
	OneDrive        string `json:"onedrive"`
	Oura            string `json:"oura"`
	Patreon         string `json:"patreon"`
	Paypal          string `json:"paypal"`
	SalesForce      string `json:"salesforce"`
	Shopify         string `json:"shopify"`
	Soundcloud      string `json:"soundcloud"`
	Spotify         string `json:"spotify"`
	Strava          string `json:"strava"`
	Stripe          string `json:"stripe"`
	TikTok          string `json:"tiktok"`
	Tumblr          string `json:"tumblr"`
	Twitch          string `json:"twitch"`
	Twitter         string `json:"twitter"`
	Typetalk        string `json:"typetalk"`
	Uber            string `json:"uber"`
	VK              string `json:"vk"`
	Wepay           string `json:"wepay"`
	Xero            string `json:"xero"`
	Yahoo           string `json:"yahoo"`
	Yammer          string `json:"yammer"`
	Yandex          string `json:"yandex"`
	Zoom            string `json:"zoom"`
	Custom          string `json:"custom"`
	Custom2         string `json:"custom2"`
	Custom3         string `json:"custom3"`
	Custom4         string `json:"custom4"`
	Custom5         string `json:"custom5"`
	Custom6         string `json:"custom6"`
	Custom7         string `json:"custom7"`
	Custom8         string `json:"custom8"`
	Custom9         string `json:"custom9"`
	Custom10        string `json:"custom10"`

	PreferredMfaType  string        `json:"preferredMfaType"`
	RecoveryCodes     []string      `json:"recoveryCodes"`
	TotpSecret        string        `json:"totpSecret"`
	MfaPhoneEnabled   bool          `json:"mfaPhoneEnabled"`
	MfaEmailEnabled   bool          `json:"mfaEmailEnabled"`
	MfaRadiusEnabled  bool          `json:"mfaRadiusEnabled"`
	MfaRadiusUsername string        `json:"mfaRadiusUsername"`
	MfaRadiusProvider string        `json:"mfaRadiusProvider"`
	MfaPushEnabled    bool          `json:"mfaPushEnabled"`
	MfaPushReceiver   string        `json:"mfaPushReceiver"`
	MfaPushProvider   string        `json:"mfaPushProvider"`
	MultiFactorAuths  []*MfaProps   `json:"multiFactorAuths,omitempty"`
	Invitation        string        `json:"invitation"`
	InvitationCode    string        `json:"invitationCode"`
	FaceIds           []*FaceId     `json:"faceIds"`
	Cart              []ProductInfo `json:"cart"`

	Ldap       string            `json:"ldap"`
	Properties map[string]string `json:"properties"`

	Roles       []*Role       `json:"roles"`
	Permissions []*Permission `json:"permissions"`
	Groups      []string      `json:"groups"`

	LastChangePasswordTime string `json:"lastChangePasswordTime"`
	LastSigninWrongTime    string `json:"lastSigninWrongTime"`
	SigninWrongTimes       int    `json:"signinWrongTimes"`

	ManagedAccounts     []ManagedAccount `json:"managedAccounts"`
	MfaAccounts         []MfaAccount     `json:"mfaAccounts"`
	MfaItems            []*MfaItem       `json:"mfaItems"`
	MfaRememberDeadline string           `json:"mfaRememberDeadline"`
	NeedUpdatePassword  bool             `json:"needUpdatePassword"`
	IpWhitelist         string           `json:"ipWhitelist"`
}

// GetId returns "<owner>/<name>", the IAM user identifier.
func (u *User) GetId() string { return u.Owner + "/" + u.Name }

// Address is one postal address on a User.
type Address struct {
	Tag     string `json:"tag"`
	Line1   string `json:"line1"`
	Line2   string `json:"line2"`
	City    string `json:"city"`
	State   string `json:"state"`
	ZipCode string `json:"zipCode"`
	Region  string `json:"region"`
}

// MfaProps is one enrolled multi-factor method. Secret/RecoveryCodes are
// credentials and are cleared by object/redact.go before a user is returned.
type MfaProps struct {
	Enabled       bool     `json:"enabled"`
	IsPreferred   bool     `json:"isPreferred"`
	MfaType       string   `json:"mfaType"`
	Secret        string   `json:"secret,omitempty"`
	CountryCode   string   `json:"countryCode,omitempty"`
	URL           string   `json:"url,omitempty"`
	RecoveryCodes []string `json:"recoveryCodes,omitempty"`
}

// MfaItem is one required MFA enrollment item.
type MfaItem struct {
	Name string `json:"name"`
	Rule string `json:"rule"`
}

// MfaAccount is a TOTP account entry (carries a secret key).
type MfaAccount struct {
	AccountName string `json:"accountName"`
	Issuer      string `json:"issuer"`
	SecretKey   string `json:"secretKey"`
	Origin      string `json:"origin"`
}

// ManagedAccount is a linked downstream account (carries a password).
type ManagedAccount struct {
	Application string `json:"application"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	SigninUrl   string `json:"signinUrl"`
}

// FaceId is enrolled biometric face data.
type FaceId struct {
	Name       string    `json:"name"`
	FaceIdData []float64 `json:"faceIdData"`
	ImageUrl   string    `json:"ImageUrl"`
}

// Role is an IAM role reference.
type Role struct {
	Owner       string   `json:"owner"`
	Name        string   `json:"name"`
	CreatedTime string   `json:"createdTime"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description"`
	Users       []string `json:"users"`
	Groups      []string `json:"groups"`
	Roles       []string `json:"roles"`
	Domains     []string `json:"domains"`
	IsEnabled   bool     `json:"isEnabled"`
}

// ProductInfo is one line item on a User's cart.
type ProductInfo struct {
	Owner       string  `json:"owner"`
	Name        string  `json:"name"`
	DisplayName string  `json:"displayName"`
	Image       string  `json:"image,omitempty"`
	Detail      string  `json:"detail,omitempty"`
	Price       float64 `json:"price"`
	Currency    string  `json:"currency,omitempty"`
	IsRecharge  bool    `json:"isRecharge,omitempty"`
	Quantity    int     `json:"quantity,omitempty"`
	PricingName string  `json:"pricingName,omitempty"`
	PlanName    string  `json:"planName,omitempty"`
}

// GetUser fetches a single user by name within the client's organization.
func (c *Client) GetUser(name string) (*User, error) {
	url := c.GetUrl("get-user", map[string]string{
		"id": fmt.Sprintf("%s/%s", c.OrganizationName, name),
	})
	bytes, err := c.DoGetBytes(url)
	if err != nil {
		return nil, err
	}
	var user *User
	if err = json.Unmarshal(bytes, &user); err != nil {
		return nil, err
	}
	return user, nil
}

// GetUsers lists all users in the client's organization.
func (c *Client) GetUsers() ([]*User, error) {
	url := c.GetUrl("get-users", map[string]string{"owner": c.OrganizationName})
	bytes, err := c.DoGetBytes(url)
	if err != nil {
		return nil, err
	}
	var users []*User
	if err = json.Unmarshal(bytes, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// UpdateUserForColumns updates only the named columns of user.
func (c *Client) UpdateUserForColumns(user *User, columns []string) (bool, error) {
	if user.Owner == "" {
		user.Owner = c.OrganizationName
	}
	queryMap := map[string]string{"id": user.GetId()}
	if len(columns) != 0 {
		queryMap["columns"] = strings.Join(columns, ",")
	}
	postBytes, err := json.Marshal(user)
	if err != nil {
		return false, err
	}
	resp, err := c.DoPost("update-user", queryMap, postBytes, false, false)
	if err != nil {
		return false, err
	}
	return resp.Data == "Affected", nil
}

// Package-level helpers operate on the configured (or env-derived) global client.

func GetUser(name string) (*User, error) { return ensureClient().GetUser(name) }
func GetUsers() ([]*User, error)         { return ensureClient().GetUsers() }
func UpdateUserForColumns(user *User, columns []string) (bool, error) {
	return ensureClient().UpdateUserForColumns(user, columns)
}
