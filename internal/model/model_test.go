package model

import (
	"testing"
	"time"
)

func TestBusPubSub(t *testing.T) {
	bus := NewBus()
	ch, unsub := bus.Subscribe(8)
	defer unsub()

	svc := Service{
		ID:           "shop-3000",
		Name:         "feat-cart.shop",
		Host:         "feat-cart.shop.localhost",
		Port:         3000,
		PID:          12345,
		ZombieReason: ZombieDeletedCwd,
	}
	if !svc.IsZombie() {
		t.Fatalf("expected service with reason %q to be zombie", svc.ZombieReason)
	}

	bus.Publish(Event{
		Type:     EventServicesUpdated,
		Services: []Service{svc},
	})

	select {
	case ev := <-ch:
		if ev.Type != EventServicesUpdated || len(ev.Services) != 1 || ev.Services[0].Name != "feat-cart.shop" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}
