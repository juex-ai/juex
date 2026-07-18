package fleetweb

import "testing"

func TestActivityClientPoolReusesAndPrunesClients(t *testing.T) {
	pool := newActivityClientPool()
	t.Cleanup(pool.close)

	const (
		firstEndpoint  = "tcp://127.0.0.1:41001"
		secondEndpoint = "tcp://127.0.0.1:41002"
	)
	first, err := pool.client(firstEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	firstAgain, err := pool.client(firstEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if firstAgain != first {
		t.Fatal("same endpoint did not reuse its HTTP client")
	}
	second, err := pool.client(secondEndpoint)
	if err != nil {
		t.Fatal(err)
	}

	pool.retain(map[string]struct{}{firstEndpoint: {}})

	firstAfterRetain, err := pool.client(firstEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if firstAfterRetain != first {
		t.Fatal("retained endpoint lost its cached HTTP client")
	}
	secondAfterPrune, err := pool.client(secondEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if secondAfterPrune == second {
		t.Fatal("pruned endpoint reused a stale HTTP client")
	}
}
