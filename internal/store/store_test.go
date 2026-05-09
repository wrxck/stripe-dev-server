package store

import (
	"sync"
	"testing"
)

func TestAddAndList(t *testing.T) {
	s := New(0)
	id1 := s.Add(&Capture{Method: "POST", Path: "/v1/payment_intents"})
	id2 := s.Add(&Capture{Method: "GET", Path: "/v1/customers/cus_123"})
	if id1 == "" || id1 == id2 {
		t.Fatalf("IDs not unique")
	}

	all := s.All("", 0)
	if len(all) != 2 {
		t.Fatalf("expected 2 captures, got %d", len(all))
	}
	if all[0].Path != "/v1/customers/cus_123" {
		t.Fatalf("expected newest-first ordering, got %s", all[0].Path)
	}
}

func TestFilterPath(t *testing.T) {
	s := New(0)
	s.Add(&Capture{Path: "/v1/payment_intents"})
	s.Add(&Capture{Path: "/v1/customers/cus_1"})
	s.Add(&Capture{Path: "/v1/payment_intents/pi_1/confirm"})

	got := s.All("payment_intents", 0)
	if len(got) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(got))
	}
}

func TestLimit(t *testing.T) {
	s := New(0)
	for i := 0; i < 5; i++ {
		s.Add(&Capture{Path: "/v1/x"})
	}
	got := s.All("", 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 with limit, got %d", len(got))
	}
}

func TestByID(t *testing.T) {
	s := New(0)
	id := s.Add(&Capture{Path: "/v1/foo"})
	if got := s.ByID(id); got == nil || got.Path != "/v1/foo" {
		t.Fatalf("ByID = %v", got)
	}
	if got := s.ByID("missing"); got != nil {
		t.Fatalf("ByID(missing) = %v", got)
	}
}

func TestRingEvictsOldest(t *testing.T) {
	s := New(2)
	s.Add(&Capture{Path: "a"})
	s.Add(&Capture{Path: "b"})
	s.Add(&Capture{Path: "c"})
	all := s.All("", 0)
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if all[0].Path != "c" || all[1].Path != "b" {
		t.Fatalf("expected [c,b], got [%s,%s]", all[0].Path, all[1].Path)
	}
}

func TestClear(t *testing.T) {
	s := New(0)
	s.Add(&Capture{Path: "/x"})
	s.Clear()
	if s.Count() != 0 {
		t.Fatalf("expected empty after Clear")
	}
}

func TestConcurrentAdd(t *testing.T) {
	s := New(0)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Add(&Capture{Path: "/x"})
		}()
	}
	wg.Wait()
	if s.Count() != 50 {
		t.Fatalf("race: count = %d, want 50", s.Count())
	}
}
