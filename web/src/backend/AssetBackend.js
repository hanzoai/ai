// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

export function getAssets(owner, page = "", pageSize = "", field = "", value = "", sortField = "", sortOrder = "") {
  return fetch(`${Setting.ServerUrl}/v1/content/assets?owner=${owner}&p=${page}&pageSize=${pageSize}&field=${field}&value=${value}&sortField=${sortField}&sortOrder=${sortOrder}`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function getAsset(owner, name) {
  return fetch(`${Setting.ServerUrl}/v1/content/assets/${encodeURIComponent(owner)}/${encodeURIComponent(name)}`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function updateAsset(owner, name, asset) {
  const newAsset = Setting.deepCopy(asset);
  return fetch(`${Setting.ServerUrl}/v1/content/assets/${encodeURIComponent(owner)}/${encodeURIComponent(name)}`, {
    method: "PATCH",
    credentials: "include",
    body: JSON.stringify(newAsset),
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function addAsset(asset) {
  const newAsset = Setting.deepCopy(asset);
  return fetch(`${Setting.ServerUrl}/v1/content/assets`, {
    method: "POST",
    credentials: "include",
    body: JSON.stringify(newAsset),
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function deleteAsset(asset) {
  const newAsset = Setting.deepCopy(asset);
  return fetch(`${Setting.ServerUrl}/v1/content/assets/${encodeURIComponent(asset.owner)}/${encodeURIComponent(asset.name)}`, {
    method: "DELETE",
    credentials: "include",
    body: JSON.stringify(newAsset),
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function scanAssets(owner, provider) {
  return fetch(`${Setting.ServerUrl}/v1/content/assets/scan?owner=${owner}&provider=${provider}`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function scanAsset(provider, scan, targetMode, target, asset, command, saveToScan) {
  let url = `${Setting.ServerUrl}/v1/scan-asset?provider=${encodeURIComponent(provider)}&targetMode=${encodeURIComponent(targetMode)}`;

  if (scan) {
    url += `&scan=${encodeURIComponent(scan)}`;
  }
  if (target) {
    url += `&target=${encodeURIComponent(target)}`;
  }
  if (asset) {
    url += `&asset=${encodeURIComponent(asset)}`;
  }
  if (command) {
    url += `&command=${encodeURIComponent(command)}`;
  }
  if (saveToScan !== undefined) {
    url += `&saveToScan=${saveToScan}`;
  }

  return fetch(url, {
    method: "POST",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}
