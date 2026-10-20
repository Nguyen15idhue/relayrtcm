package main

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeNMEAIntervalSec(t *testing.T) {
	cases := map[int]int{
		-5:   0,
		0:    0,
		1:    1,
		10:   10,
		3600: 3600,
		9999: 3600,
	}
	for input, want := range cases {
		if got := normalizeNMEAIntervalSec(input); got != want {
			t.Fatalf("normalizeNMEAIntervalSec(%d) = %d, want %d", input, got, want)
		}
	}
}

func TestWorkerNMEAIntervalAndJitter(t *testing.T) {
	w := &Worker{
		cfg:    ConfigStation{NMEAIntervalSec: 5},
		device: DeviceProfile{NMEAInterval: 12 * time.Second, NMEAJitter: 3 * time.Second},
		rand:   rand.New(rand.NewSource(1)),
	}
	if got := w.nmeaInterval(); got != 5*time.Second {
		t.Fatalf("configured interval = %v, want 5s", got)
	}
	if got := w.nmeaJitter(); got != time.Second {
		t.Fatalf("configured jitter = %v, want 1s", got)
	}
	for i := 0; i < 100; i++ {
		next := w.nextNMEAInterval()
		if next < 4*time.Second || next > 6*time.Second {
			t.Fatalf("next interval out of range: %v", next)
		}
	}

	w.cfg.NMEAIntervalSec = 0
	if got := w.nmeaInterval(); got != 12*time.Second {
		t.Fatalf("auto interval = %v, want 12s", got)
	}
	if got := w.nmeaJitter(); got != 2400*time.Millisecond {
		t.Fatalf("auto jitter = %v, want 2.4s", got)
	}

	w.cfg.NMEAIntervalSec = 9999
	if got := w.nmeaInterval(); got != time.Hour {
		t.Fatalf("clamped interval = %v, want 1h", got)
	}
}

func TestHistoryStoreLifecycle(t *testing.T) {
	store, err := OpenHistoryStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const station = "test_station"
	if err := store.AddEvent(station, "config", "Worker initialized", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.AddEvent(station, "timeout", "read source: i/o timeout", 1.5); err != nil {
		t.Fatal(err)
	}
	if err := store.AddEvent(station, "retry", "Retry in 20s", 0); err != nil {
		t.Fatal(err)
	}

	sessionID, err := store.BeginSession(station)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID == 0 {
		t.Fatal("expected session id")
	}
	if err := store.SampleOnline(); err != nil {
		t.Fatal(err)
	}

	detail, err := store.StationDetail(station, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if detail.FirstSeen == nil {
		t.Fatal("expected first_seen")
	}
	if detail.ActiveSessionStart == nil {
		t.Fatal("expected active session")
	}
	if detail.TotalSessions != 1 {
		t.Fatalf("total sessions = %d, want 1", detail.TotalSessions)
	}
	if detail.EventCounts.Timeout != 1 {
		t.Fatalf("timeouts = %d, want 1", detail.EventCounts.Timeout)
	}
	if detail.EventCounts.Retry != 1 {
		t.Fatalf("retries = %d, want 1", detail.EventCounts.Retry)
	}
	if len(detail.Events) == 0 {
		t.Fatal("expected events")
	}
	if len(detail.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(detail.Sessions))
	}
	if detail.Sessions[0].End != nil {
		t.Fatal("active session should have nil end")
	}

	time.Sleep(10 * time.Millisecond)
	if err := store.EndSession(sessionID, station, "error", "test disconnect"); err != nil {
		t.Fatal(err)
	}

	detail, err = store.StationDetail(station, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ActiveSessionStart != nil {
		t.Fatal("expected no active session")
	}
	if detail.Sessions[0].End == nil {
		t.Fatal("expected session end")
	}
	if detail.Sessions[0].Result != "error" {
		t.Fatalf("session result = %q, want error", detail.Sessions[0].Result)
	}

	if err := store.DeleteStation(station); err != nil {
		t.Fatal(err)
	}
	detail, err = store.StationDetail(station, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if detail.FirstSeen != nil || len(detail.Events) != 0 || len(detail.Sessions) != 0 {
		t.Fatal("expected station history to be deleted")
	}
}

func TestHistoryStoreClosesOrphanSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := OpenHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	const station = "orphan_station"
	if _, err := store.BeginSession(station); err != nil {
		t.Fatal(err)
	}
	if err := store.SampleOnline(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	detail, err := reopened.StationDetail(station, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ActiveSessionStart != nil {
		t.Fatal("orphan session should be closed on startup")
	}
	if len(detail.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(detail.Sessions))
	}
	if detail.Sessions[0].Result != "unknown" {
		t.Fatalf("orphan result = %q, want unknown", detail.Sessions[0].Result)
	}
}
