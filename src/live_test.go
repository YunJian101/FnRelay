package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatusRecorderCountsBytesImmediately(t *testing.T) {
	var stats Stats
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), stats: &stats}
	n, err := rec.Write([]byte("abcdef"))
	if err != nil || n != 6 {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	if got := stats.BytesOut.Load(); got != 6 {
		t.Fatalf("bytes_out = %d, want 6", got)
	}
}

func TestLiveRegistrySnapshot(t *testing.T) {
	reg := NewLiveRegistry()
	r := httptest.NewRequest("GET", "http://app.test.local/video?token=secret&x=1", nil)
	r.RemoteAddr = "192.168.11.20:5566"
	rule := &Rule{Name: "jellyfin", Upstream: "http://127.0.0.1:8096"}
	live := reg.Start(r, "app.test.local", rule)
	if got := reg.Count(); got != 1 {
		t.Fatalf("active count = %d, want 1", got)
	}
	live.Status.Store(200)
	live.Down.Add(1000)
	live.Up.Add(200)
	time.Sleep(100 * time.Millisecond)

	first := reg.Snapshot()
	if first.Active != 1 || len(first.Requests) != 1 {
		t.Fatalf("active: %+v", first)
	}
	entry := first.Requests[0]
	if entry.ClientIP != "192.168.11.20" || entry.Host != "app.test.local" {
		t.Fatalf("identity: %+v", entry)
	}
	if entry.Query != "token=%5BREDACTED%5D&x=1" {
		t.Fatalf("query: %s", entry.Query)
	}
	if entry.UpstreamHost != "127.0.0.1:8096" || entry.Status != 200 {
		t.Fatalf("upstream/status: %+v", entry)
	}
	if entry.DownBPS <= 0 {
		t.Fatalf("first-sample speed not measured: %+v", entry)
	}

	time.Sleep(120 * time.Millisecond)
	live.Down.Add(1000)
	second := reg.Snapshot()
	if second.Requests[0].DownBPS <= 0 {
		t.Fatalf("speed not measured: %+v", second.Requests[0])
	}
	if second.DownBPS != second.Requests[0].DownBPS {
		t.Fatalf("aggregate speed: %+v", second)
	}

	reg.Finish(live.ID)
	if got := reg.Snapshot(); got.Active != 0 {
		t.Fatalf("finish failed: %+v", got)
	}
	if got := reg.Count(); got != 0 {
		t.Fatalf("active count after finish = %d, want 0", got)
	}
}

func TestLiveRegistrySortNewestFirst(t *testing.T) {
	reg := NewLiveRegistry()
	r1 := httptest.NewRequest("GET", "http://a.test/", nil)
	r2 := httptest.NewRequest("GET", "http://b.test/", nil)
	r1.RemoteAddr = "1.1.1.1:1"
	r2.RemoteAddr = "2.2.2.2:2"
	first := reg.Start(r1, "a.test", nil)
	time.Sleep(10 * time.Millisecond)
	second := reg.Start(r2, "b.test", nil)
	snap := reg.Snapshot()
	if len(snap.Requests) != 2 || snap.Requests[0].ID != second.ID || snap.Requests[1].ID != first.ID {
		t.Fatalf("sort: %+v", snap.Requests)
	}
	reg.Finish(first.ID)
	reg.Finish(second.ID)
}
