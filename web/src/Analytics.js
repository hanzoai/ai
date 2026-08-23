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

import React from "react";
import {useLocation} from "react-router-dom";
import {AnalyticsProvider, ErrorBoundary, usePageview} from "@hanzo/event/react";

// The ONE Hanzo Cloud telemetry endpoint — POST api.hanzo.ai/v1/event. Cloud
// fans the one batched stream out to the web (analytics), product (insights) and
// error (sentry) lenses; the client never sends the org — Cloud resolves the
// tenant server-side from the session or the publishable ingest key.
const HOST = "https://api.hanzo.ai";

// Publishable ingest key (pk_…), minted per org via POST /v1/ingest/keys. It is a
// write-only, bundle-safe client key that lets the signed-OUT views (sign-in,
// callback, errors before the session resolves) still resolve to an org. Unset →
// those events are best-effort and dropped at the edge.
const INGEST_KEY = process.env.REACT_APP_EVENT_INGEST_KEY ? process.env.REACT_APP_EVENT_INGEST_KEY.trim() : undefined;

// Honor an explicit browser opt-out (Global Privacy Control, then legacy DNT).
// Opting out suppresses pageviews AND errors — the `enabled` gate is the whole
// consent story.
function consented() {
  const nav = navigator;
  if (nav.globalPrivacyControl === true) {
    return false;
  }
  const dnt = nav.doNotTrack;
  return dnt !== "1" && dnt !== "yes";
}

function Pageview() {
  usePageview(useLocation().pathname);
  return null;
}

// Telemetry root. The provider owns the ONE @hanzo/event client: it fires the
// first pageview, registers auto error capture (window.onerror +
// unhandledrejection) and flushes on unload; <Pageview> adds one per route
// change and <ErrorBoundary> catches render errors React swallows before
// window.onerror sees them. Errors are just events on that one stream, so there
// is no separate DSN. App calls identify() once the account resolves.
export function Analytics({children}) {
  return (
    <AnalyticsProvider config={{product: "cloud-web", host: HOST, ingestKey: INGEST_KEY, enabled: consented()}}>
      <ErrorBoundary>
        <Pageview />
        {children}
      </ErrorBoundary>
    </AnalyticsProvider>
  );
}
