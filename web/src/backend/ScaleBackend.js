// Copyright 2025 Hanzo AI, Inc.. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

import * as Setting from "../Setting";

export function getGlobalScales() {
  return fetch(`${Setting.ServerUrl}/v1/work/scales/global`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function getScales(owner, page = "", pageSize = "", field = "", value = "", sortField = "", sortOrder = "") {
  return fetch(`${Setting.ServerUrl}/v1/work/scales?owner=${owner}&p=${page}&pageSize=${pageSize}&field=${field}&value=${value}&sortField=${sortField}&sortOrder=${sortOrder}`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function getScale(owner, name) {
  return fetch(`${Setting.ServerUrl}/v1/work/scales/${encodeURIComponent(owner)}/${encodeURIComponent(name)}`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function getPublicScales() {
  return fetch(`${Setting.ServerUrl}/v1/work/scales/public`, {
    method: "GET",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
  }).then(res => res.json());
}

export function updateScale(owner, name, scale) {
  const newScale = Setting.deepCopy(scale);
  return fetch(`${Setting.ServerUrl}/v1/work/scales/${encodeURIComponent(owner)}/${encodeURIComponent(name)}`, {
    method: "PATCH",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
    body: JSON.stringify(newScale),
  }).then(res => res.json());
}

export function addScale(scale) {
  const newScale = Setting.deepCopy(scale);
  return fetch(`${Setting.ServerUrl}/v1/work/scales`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
    body: JSON.stringify(newScale),
  }).then(res => res.json());
}

export function deleteScale(scale) {
  const newScale = Setting.deepCopy(scale);
  return fetch(`${Setting.ServerUrl}/v1/work/scales/${encodeURIComponent(scale.owner)}/${encodeURIComponent(scale.name)}`, {
    method: "DELETE",
    credentials: "include",
    headers: {
      "Accept-Language": Setting.getAcceptLanguage(),
    },
    body: JSON.stringify(newScale),
  }).then(res => res.json());
}
