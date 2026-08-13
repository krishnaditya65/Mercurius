package accountcontrol

import "testing"

func TestAccountNotFrozenByDefault(t *testing.T) {
	stateMachineUnderTest := NewAccountFreezeStateMachine()

	status := stateMachineUnderTest.CheckFreezeStatus("acct-001")

	if status.IsFrozen {
		t.Fatal("an account with no freeze action taken must not be frozen")
	}
}

func TestFreezeAccountMarksItFrozenWithReason(t *testing.T) {
	stateMachineUnderTest := NewAccountFreezeStateMachine()

	stateMachineUnderTest.FreezeAccount("acct-001", "suspected AML flag, pending review")
	status := stateMachineUnderTest.CheckFreezeStatus("acct-001")

	if !status.IsFrozen {
		t.Fatal("expected account to be frozen")
	}
	if status.FreezeReason != "suspected AML flag, pending review" {
		t.Fatalf("unexpected freeze reason: %q", status.FreezeReason)
	}
}

func TestUnfreezeAccountClearsFrozenStatus(t *testing.T) {
	stateMachineUnderTest := NewAccountFreezeStateMachine()

	stateMachineUnderTest.FreezeAccount("acct-001", "temporary hold")
	stateMachineUnderTest.UnfreezeAccount("acct-001")
	status := stateMachineUnderTest.CheckFreezeStatus("acct-001")

	if status.IsFrozen {
		t.Fatal("expected account to be unfrozen")
	}
}

func TestFreezingOneAccountDoesNotAffectAnother(t *testing.T) {
	stateMachineUnderTest := NewAccountFreezeStateMachine()

	stateMachineUnderTest.FreezeAccount("acct-001", "reason")

	if stateMachineUnderTest.CheckFreezeStatus("acct-002").IsFrozen {
		t.Fatal("freezing acct-001 must not affect acct-002")
	}
}
