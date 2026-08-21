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
// ai needs to talk to Hanzo IAM (hanzo.id), decoupled from the retired SDK
// module github.com/hanzoai/iam-v1.
//
// It is NOT a published SDK. It exists so ai links only against standard OIDC
// (JWKS token verification) and the IAM server's JSON REST API (/v1/iam/...),
// instead of pulling in the whole iam-v1 server module (the retired framework, xorm, ldap,
// aliyun/aws SDKs, …) that it dragged into ai's build for code ai never ran.
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
//
// # A record is addressed by its noun
//
// IAM serves `/v1/iam/<plural>` with `/get`, `/update` and `/delete` under it —
// `applications/get`, `permissions`, `users/update`. The `<verb>-<noun>`
// spellings this client used to build ("get-cert") are gone from its router, and
// three things about the replacement are easy to get wrong in a way that does
// not announce itself:
//
// THE KEY IS TWO PARAMETERS. `?owner=&name=`, never the joined `?id=<owner>/<name>`
// — which is not a parameter these routes read, so a request still spelling it
// states no owner at all.
//
// THERE IS NO ENVELOPE. A route answers with the record itself, or with the
// collection under its own name ({"users":[…]}). The retired surface wrapped
// everything in {status, data}, so reaching for `data` now finds nothing and
// yields a zero value — a blank record, or an empty list, returned as a success.
//
// THE METHOD IS AUTHORIZED, NOT JUST ROUTED. IAM decides whether a request is a
// READ from its HTTP method, so a record fetched with the wrong verb is weighed
// as a write and read-scoped grants do not fire. That answers 403, not 405 or
// 404 — a refusal that reads like a permissions regression. Most single-record
// reads are GETs; a few (providers/get, tokens/get, sessions/get, roles/get)
// are POSTs. Read the registration in the IAM package before adding a call.
package iam
