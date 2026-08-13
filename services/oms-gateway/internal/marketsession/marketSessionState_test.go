package marketsession

import "testing"

func TestNewMarketSessionStateStartsClosed(t *testing.T) {
	state := NewMarketSessionState()
	if state.IsMarketOpen() {
		t.Fatal("expected a freshly constructed market session to start closed")
	}
}

func TestSetMarketOpenTogglesTheFlag(t *testing.T) {
	state := NewMarketSessionState()

	state.SetMarketOpen(true)
	if !state.IsMarketOpen() {
		t.Fatal("expected IsMarketOpen to be true after SetMarketOpen(true)")
	}

	state.SetMarketOpen(false)
	if state.IsMarketOpen() {
		t.Fatal("expected IsMarketOpen to be false after SetMarketOpen(false)")
	}
}
