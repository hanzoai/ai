// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/luxfi/zap"
)

// status reads the native-cloud response status the handlers set, mirroring the
// assertion the other zap group tests use.
func zapVideoStatus(t *testing.T, msg *zap.Message) uint32 {
	t.Helper()
	return msg.Root().Uint32(object.CloudRespStatus)
}

// TestZapVideosAuthRejection asserts the auth seam on create: no token → 401 and
// a publishable (read-only) key → 403, exactly like the beego VideosGenerations
// path. These short-circuit before any provider/upstream call, so they are fully
// deterministic.
func TestZapVideosAuthRejection(t *testing.T) {
	body := []byte(`{"model":"zen3-video","prompt":"a cat"}`)
	cases := []struct {
		name string
		auth string
		want uint32
	}{
		{"empty", "", 401},
		{"publishable", "Bearer pk-live-abc123", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := zapVideosGenerateHandler(context.Background(), tc.auth, body)
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if got := zapVideoStatus(t, msg); got != tc.want {
				t.Fatalf("status = %d, want %d (err=%q)", got, tc.want, msg.Root().Text(object.CloudRespError))
			}
		})
	}
}

// TestZapVideosPollDownloadMissingID asserts the native-cloud poll/download
// handlers reject a body with no job id as a 400 before touching the store.
func TestZapVideosPollDownloadMissingID(t *testing.T) {
	cases := []struct {
		name string
		fn   func(context.Context, string, []byte) (*zap.Message, error)
		body string
		want uint32
	}{
		{"retrieve-empty-id", zapVideosRetrieveHandler, `{}`, 400},
		{"retrieve-bad-json", zapVideosRetrieveHandler, `{`, 400},
		{"content-empty-id", zapVideosContentHandler, `{}`, 400},
		{"content-bad-json", zapVideosContentHandler, `{`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := tc.fn(context.Background(), "Bearer hk-whatever", []byte(tc.body))
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if got := zapVideoStatus(t, msg); got != tc.want {
				t.Fatalf("status = %d, want %d (err=%q)", got, tc.want, msg.Root().Text(object.CloudRespError))
			}
		})
	}
}

// TestZapVideosUnknownJobAuthFirst asserts the ownership seam's authenticate-first
// invariant: a poll/download for an id not in the store with NO credential is a
// 401 (never a probe-able 404 that would distinguish "bad key" from "no such
// job"). Deterministic — an unknown id never reaches an upstream call.
func TestZapVideosUnknownJobAuthFirst(t *testing.T) {
	id := "video_does-not-exist-000"
	for _, fn := range []func(context.Context, string, string) (*zap.Message, error){
		zapVideoRetrieve, zapVideoContent,
	} {
		msg, err := fn(context.Background(), "", id)
		if err != nil {
			t.Fatalf("handler error: %v", err)
		}
		if got := zapVideoStatus(t, msg); got != 401 {
			t.Fatalf("unknown-job unauth status = %d, want 401 (err=%q)", got, msg.Root().Text(object.CloudRespError))
		}
	}
}

// TestZapVideoJobResponseParity is the terminal-encode/lifecycle parity test:
// the OpenAI Sora video object a native poll/download returns must project the
// job's status and progress exactly like the beego path (videoJobResponse is the
// shared projection). This is the billing/lifecycle surface the client sees.
func TestZapVideoJobResponseParity(t *testing.T) {
	completed := &videoJob{
		id: "video_test-complete", userModel: "zen3-video",
		createdAt: time.Now(), status: "completed",
	}
	failed := &videoJob{
		id: "video_test-failed", userModel: "zen3-video",
		createdAt: time.Now(), status: "failed", failureReason: "upstream boom",
	}

	var got map[string]interface{}
	raw, _ := json.Marshal(videoJobResponse(completed))
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("marshal completed: %v", err)
	}
	if got["object"] != "video" || got["status"] != "completed" {
		t.Fatalf("completed projection wrong: %v", got)
	}
	if p, _ := got["progress"].(float64); p != 100 {
		t.Fatalf("completed progress = %v, want 100", got["progress"])
	}
	if got["id"] != "video_test-complete" || got["model"] != "zen3-video" {
		t.Fatalf("completed identity wrong: %v", got)
	}

	raw, _ = json.Marshal(videoJobResponse(failed))
	got = map[string]interface{}{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if got["status"] != "failed" {
		t.Fatalf("failed status wrong: %v", got)
	}
	errObj, ok := got["error"].(map[string]interface{})
	if !ok || errObj["message"] != "upstream boom" {
		t.Fatalf("failed error projection wrong: %v", got["error"])
	}
}
