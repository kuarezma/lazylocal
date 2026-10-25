package capture

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/ugur/lazylocal/internal/model"
)

func TestRingBuffer_RecordAndListOrder(t *testing.T) {
	rb := NewRingBuffer(3)

	rb.Record(model.Exchange{ID: "ex1", ServiceID: "s1"})
	rb.Record(model.Exchange{ID: "ex2", ServiceID: "s1"})
	rb.Record(model.Exchange{ID: "ex3", ServiceID: "s2"})

	list := rb.List("", false)
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list))
	}
	if list[0].ID != "ex3" || list[1].ID != "ex2" || list[2].ID != "ex1" {
		t.Fatalf("expected order [ex3, ex2, ex1], got [%s, %s, %s]", list[0].ID, list[1].ID, list[2].ID)
	}

	// Overwrite oldest (ex1 should be dropped)
	rb.Record(model.Exchange{ID: "ex4", ServiceID: "s1"})
	list = rb.List("", false)
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list))
	}
	if list[0].ID != "ex4" || list[1].ID != "ex3" || list[2].ID != "ex2" {
		t.Fatalf("expected order [ex4, ex3, ex2], got [%s, %s, %s]", list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestRingBuffer_FilterServiceIDAndErrors(t *testing.T) {
	rb := NewRingBuffer(10)

	rb.Record(model.Exchange{ID: "1", ServiceID: "auth", Resp: model.HTTPResponseData{StatusCode: 200}})
	rb.Record(model.Exchange{ID: "2", ServiceID: "shop", Resp: model.HTTPResponseData{StatusCode: 404}})
	rb.Record(model.Exchange{ID: "3", ServiceID: "auth", Resp: model.HTTPResponseData{StatusCode: 500}})
	rb.Record(model.Exchange{ID: "4", ServiceID: "auth", Error: "connection reset"})
	rb.Record(model.Exchange{ID: "5", ServiceID: "shop", Resp: model.HTTPResponseData{StatusCode: 200}})

	// Filter by serviceID
	authList := rb.List("auth", false)
	if len(authList) != 3 {
		t.Fatalf("expected 3 auth exchanges, got %d", len(authList))
	}
	if authList[0].ID != "4" || authList[1].ID != "3" || authList[2].ID != "1" {
		t.Fatalf("unexpected order for auth: %+v", authList)
	}

	// Filter by onlyErrors
	errList := rb.List("", true)
	if len(errList) != 3 {
		t.Fatalf("expected 3 error exchanges (4, 3, 2), got %d", len(errList))
	}
	if errList[0].ID != "4" || errList[1].ID != "3" || errList[2].ID != "2" {
		t.Fatalf("unexpected order for errors: %+v", errList)
	}

	// Filter both
	authErrList := rb.List("auth", true)
	if len(authErrList) != 2 {
		t.Fatalf("expected 2 auth error exchanges, got %d", len(authErrList))
	}
	if authErrList[0].ID != "4" || authErrList[1].ID != "3" {
		t.Fatalf("unexpected order for auth errors: %+v", authErrList)
	}
}

func TestRingBuffer_GetAndClear(t *testing.T) {
	rb := NewRingBuffer(5)

	rb.Record(model.Exchange{ID: "a", ServiceID: "s1"})
	rb.Record(model.Exchange{ID: "b", ServiceID: "s2"})
	rb.Record(model.Exchange{ID: "c", ServiceID: "s1"})

	// Get
	if item, ok := rb.Get("b"); !ok || item.ID != "b" {
		t.Fatalf("failed to get item 'b': ok=%v, item=%+v", ok, item)
	}
	if _, ok := rb.Get("nonexistent"); ok {
		t.Fatal("expected false for nonexistent ID")
	}

	// Clear serviceID "s1"
	rb.Clear("s1")
	list := rb.List("", false)
	if len(list) != 1 || list[0].ID != "b" {
		t.Fatalf("expected only 'b' left, got %+v", list)
	}

	// Clear all
	rb.Clear("")
	list = rb.List("", false)
	if len(list) != 0 {
		t.Fatalf("expected empty list after Clear(''), got %d", len(list))
	}
}

func TestRingBuffer_ConcurrentAccess(t *testing.T) {
	rb := NewRingBuffer(50)
	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rb.Record(model.Exchange{
					ID:        fmt.Sprintf("w%d-%d", workerID, j),
					ServiceID: fmt.Sprintf("svc-%d", j%3),
					Resp:      model.HTTPResponseData{StatusCode: 200 + (j%5)*100},
				})
			}
		}(i)
	}

	// Readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = rb.List("", false)
				_ = rb.List("svc-1", true)
				_, _ = rb.Get("w0-10")
			}
		}()
	}

	wg.Wait()

	list := rb.List("", false)
	if len(list) != 50 {
		t.Fatalf("expected buffer to be full with 50 items, got %d", len(list))
	}
}

func TestMaskHeaders(t *testing.T) {
	h := http.Header{
		"Authorization":       []string{"Bearer supersecrettoken"},
		"authorization":       []string{"Basic dXNlcjpwYXNz"},
		"COOKIE":              []string{"session_id=xyz"},
		"Set-Cookie":          []string{"user_id=123; Path=/"},
		"X-Api-Key":           []string{"key_abc123"},
		"x-api-key":           []string{"key_lower"},
		"Proxy-Authorization": []string{"Basic proxyauth"},
		"Content-Type":        []string{"application/json"},
		"X-Custom-Header":     []string{"value1", "value2"},
	}

	masked := MaskHeaders(h)

	// Check sensitive headers
	for _, key := range []string{"Authorization", "authorization", "COOKIE", "Set-Cookie", "X-Api-Key", "x-api-key", "Proxy-Authorization"} {
		vals, ok := masked[key]
		if !ok {
			t.Errorf("expected header %s to be present in masked map", key)
			continue
		}
		if len(vals) != 1 || vals[0] != "REDACTED" {
			t.Errorf("header %s was not redacted properly: %+v", key, vals)
		}
	}

	// Check non-sensitive headers
	if ct, ok := masked["Content-Type"]; !ok || len(ct) != 1 || ct[0] != "application/json" {
		t.Errorf("Content-Type corrupted: %+v", ct)
	}

	if ch, ok := masked["X-Custom-Header"]; !ok || len(ch) != 2 || ch[0] != "value1" || ch[1] != "value2" {
		t.Errorf("X-Custom-Header corrupted: %+v", ch)
	}
}
