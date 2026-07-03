// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package model

import (
	"encoding/json"
	"testing"
)

// TestDoaiImageSize verifies the OpenAI-style "WxH" size is serialized as fal's
// structured image_size OBJECT ({"width":W,"height":H}), and that empty or
// unparseable sizes are omitted (nil) so the model default applies. This is the
// regression guard for the do-ai contract change that started rejecting a bare
// "1024x1024" string ("image_size: Input should be a valid dictionary...").
func TestDoaiImageSize(t *testing.T) {
	cases := []struct {
		size string
		want string // json for {"image_size":<v>,omitempty}, or "" when omitted
	}{
		{"1024x1024", `{"image_size":{"width":1024,"height":1024}}`},
		{"1024x1792", `{"image_size":{"width":1024,"height":1792}}`},
		{"1792x1024", `{"image_size":{"width":1792,"height":1024}}`},
		{"  1024 x 1024  ", `{"image_size":{"width":1024,"height":1024}}`},
		{"", `{}`},
		{"square_hd", `{}`},
		{"1024", `{}`},
		{"0x0", `{}`},
		{"-1x10", `{}`},
	}
	for _, c := range cases {
		wrap := struct {
			ImageSize any `json:"image_size,omitempty"`
		}{ImageSize: doaiImageSize(c.size)}
		b, err := json.Marshal(wrap)
		if err != nil {
			t.Fatalf("marshal %q: %v", c.size, err)
		}
		if string(b) != c.want {
			t.Errorf("doaiImageSize(%q) => %s, want %s", c.size, b, c.want)
		}
	}
}
