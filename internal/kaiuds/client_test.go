package kaiuds

import (
    "context"
    "encoding/json"
    "net"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func TestDecideFramesAndOrg(t *testing.T) {
    path := filepath.Join(t.TempDir(), "kai.sock")
    ln, err := net.Listen("unix", path)
    if err != nil { t.Fatal(err) }
    defer ln.Close()

    done := make(chan error, 1)
    go func() {
        conn, e := ln.Accept()
        if e != nil { done <- e; return }
        defer conn.Close()
        for i, want := range []string{"org-1", "[]", `{"state":"ok"}`} {
            buf, e := readFrame(conn, maxBodyBytes)
            if e != nil { done <- e; return }
            if string(buf) != want {
                done <- &mismatch{i: i, got: string(buf), want: want}
                return
            }
        }
        done <- writeFrame(conn, []byte(`{"answers":{}}`))
    }()
    out, err := (Client{Path:path}).Decide(context.Background(), "org-1", "[]", []byte(`{"state":"ok"}`))
    if err != nil { t.Fatal(err) }
    if !json.Valid(out) || string(out) != `{"answers":{}}` { t.Fatalf("unexpected response %s", out) }
    if err := <-done; err != nil { t.Fatal(err) }
}

type mismatch struct { i int; got, want string }
func (m *mismatch) Error() string { return "frame mismatch" }

func TestRejectsInvalidOrgAndOversize(t *testing.T) {
    c := Client{Path:"/tmp/never-connect"}
    for _, org := range []string{strings.Repeat("x",257), "ok\nnot-ok", "\xff"} {
        if _, err := c.Decide(context.Background(),org,"",nil); err == nil {
            t.Fatalf("accepted invalid org %q",org)
        }
    }
    if _, err := c.Decide(context.Background(),"good","",make([]byte,maxBodyBytes+1)); err == nil {
        t.Fatal("accepted over-limit body")
    }
}

func TestCancelledDial(t *testing.T) {
    ctx,cancel := context.WithCancel(context.Background());cancel()
    _,err := (Client{Path:"/run/no-such-kai.sock",Timeout: time.Second}).Decide(ctx, "org", "", nil)
    if err == nil {t.Fatal("expected dial failure")}
}
