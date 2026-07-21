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

// Package iam is ai's INTERNAL IAM client: the small, clean OIDC+REST surface
// ai needs to talk to Hanzo IAM (hanzo.id), decoupled from the retired Casdoor
// SDK module github.com/hanzoai/iam-v1.
//
// It is NOT a published SDK. It exists so ai links only against standard OIDC
// (JWKS token verification) and the IAM server's JSON REST API (/v1/iam/...),
// instead of pulling in the whole Casdoor server module (beego, xorm, ldap,
// aliyun/aws SDKs, …) that iam-v1 dragged into ai's build for code ai never ran.
//
// The wire types (User, Claims, Permission, Resource, …) mirror the IAM
// server's JSON models so requests round-trip losslessly — in particular the
// full User field set with its json tags is reproduced verbatim, because
// object/redact.go reflects over every User field by json tag as a fail-secure
// secret-redaction control. Slimming User would silently weaken that control
// and break its test, so fidelity is required, not optional.
//
// Endpoint resolution: callers configure a Client explicitly via InitConfig /
// NewClient (ai's account bootstrap does), and the package-level helpers fall
// back to a lazily-built client from IAM_ENDPOINT / IAM_ISSUER (default
// https://hanzo.id) so a read never nil-panics when InitConfig was skipped.
package iam
