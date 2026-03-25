// Copyright 2023 Hanzo AI Inc. All Rights Reserved.
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

export function getModelRoutes(owner, page = "", pageSize = "", field = "", value = "", sortField = "", sortOrder = "") {
  return fetch(`${Setting.ServerUrl}/api/get-model-routes?owner=${owner}&p=${page}&pageSize=${pageSize}&field=${field}&value=${value}&sortField=${sortField}&sortOrder=${sortOrder}`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function getModelRoute(owner, modelName) {
  return fetch(`${Setting.ServerUrl}/api/get-model-route?owner=${owner}&modelName=${encodeURIComponent(modelName)}`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function addModelRoute(route) {
  const newRoute = Setting.deepCopy(route);
  return fetch(`${Setting.ServerUrl}/api/add-model-route`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
    body: JSON.stringify(newRoute),
  }).then(res => res.json());
}

export function updateModelRoute(owner, modelName, route) {
  const newRoute = Setting.deepCopy(route);
  return fetch(`${Setting.ServerUrl}/api/update-model-route?owner=${owner}&modelName=${encodeURIComponent(modelName)}`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
    body: JSON.stringify(newRoute),
  }).then(res => res.json());
}

export function deleteModelRoute(route) {
  const newRoute = Setting.deepCopy(route);
  return fetch(`${Setting.ServerUrl}/api/delete-model-route`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
    body: JSON.stringify(newRoute),
  }).then(res => res.json());
}
