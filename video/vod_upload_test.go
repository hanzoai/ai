// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package video

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openapi "github.com/alibabacloud-go/darabonba-openapi/client"
	"github.com/alibabacloud-go/tea/tea"
	vod20170321 "github.com/alibabacloud-go/vod-20170321/v2/client"
)

// vodAt points VodClient at a test server for the length of a test.
func vodAt(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client, err := vod20170321.NewClient(&openapi.Config{
		AccessKeyId:     tea.String("id"),
		AccessKeySecret: tea.String("secret"),
		RegionId:        tea.String("cn-shanghai"),
		Endpoint:        tea.String(strings.TrimPrefix(srv.URL, "http://")),
		Protocol:        tea.String("http"),
	})
	if err != nil {
		t.Fatal(err)
	}
	prev := VodClient
	VodClient = client
	t.Cleanup(func() { VodClient = prev })
}

// An upload that fails says why. UploadVideo returned "", nil on each failure,
// so the caller saw success with no id and could report only that the id was
// empty: the refusal from VOD, or the upload address it could not read, was lost.
func TestAFailedUploadSaysWhy(t *testing.T) {
	for _, c := range []struct {
		how string
		h   http.HandlerFunc
	}{
		{"VOD refuses the upload", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"RequestId":"r","Code":"Forbidden.RAM","Message":"not allowed"}`)
		}},
		{"VOD's upload address is not JSON", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			notJSON := base64.StdEncoding.EncodeToString([]byte("not json"))
			fmt.Fprintf(w, `{"RequestId":"r","VideoId":"v-1","UploadAddress":%q,"UploadAuth":%q}`, notJSON, notJSON)
		}},
	} {
		vodAt(t, c.h)
		id, err := UploadVideo("file-1", "clip.mp4", bytes.NewBufferString("frames"))
		if err == nil {
			t.Errorf("%s: UploadVideo answered %q with no error", c.how, id)
		}
		if id != "" {
			t.Errorf("%s: a failed upload answered video id %q", c.how, id)
		}
	}
}
