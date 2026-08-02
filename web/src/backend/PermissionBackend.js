// Copyright 2023 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

import * as Setting from "../Setting";

// Permissions belong to the IAM service, so these go to /v1/iam/permissions —
// its published surface, and the same place PermissionUtil sends the user to
// review what it just created. ai used to re-serve them from its own address by
// proxying every call to IAM; that door is gone, and there is only this one.

export function getPermissions(owner) {
  return fetch(`${Setting.ServerUrl}/v1/iam/permissions?owner=${owner}`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function addPermission(permission) {
  const newPermission = Setting.deepCopy(permission);
  return fetch(`${Setting.ServerUrl}/v1/iam/permissions`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
    body: JSON.stringify(newPermission),
  }).then(res => res.json());
}
