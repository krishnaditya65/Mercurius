package referralrewards

import (
	"errors"
	"testing"
	"time"
)

func TestGenerateReferralCodeIsStableAcrossCalls(t *testing.T) {
	registry := NewRegistry()

	firstCode, err := registry.GenerateReferralCode("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstCode == "" {
		t.Fatalf("expected a non-empty referral code")
	}

	secondCode, err := registry.GenerateReferralCode("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstCode != secondCode {
		t.Fatalf("expected the same referral code across calls, got %q then %q", firstCode, secondCode)
	}
}

func TestDifferentAccountsGetDifferentReferralCodes(t *testing.T) {
	registry := NewRegistry()

	codeA, _ := registry.GenerateReferralCode("acct-001")
	codeB, _ := registry.GenerateReferralCode("acct-002")

	if codeA == codeB {
		t.Fatalf("expected distinct referral codes, both were %q", codeA)
	}
}

func TestRecordReferralLinksReferredAccountToReferrer(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	code, _ := registry.GenerateReferralCode("acct-referrer")

	link, err := registry.RecordReferral(code, "acct-referred", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if link.ReferrerAccountIdentifier != "acct-referrer" {
		t.Fatalf("expected referrer acct-referrer, got %s", link.ReferrerAccountIdentifier)
	}
	if link.Status != ReferralStatusPendingQualifyingEvent {
		t.Fatalf("expected pending status, got %s", link.Status)
	}

	stored, exists := registry.GetReferralLink("acct-referred")
	if !exists || stored.ReferrerAccountIdentifier != "acct-referrer" {
		t.Fatalf("expected stored link, got %+v exists=%v", stored, exists)
	}
}

func TestRecordReferralRejectsUnknownCode(t *testing.T) {
	registry := NewRegistry()

	if _, err := registry.RecordReferral("MERC-NOPE99", "acct-referred", time.Now()); !errors.Is(err, ErrUnknownReferralCode) {
		t.Fatalf("expected ErrUnknownReferralCode, got %v", err)
	}
}

func TestRecordReferralRejectsSelfReferral(t *testing.T) {
	registry := NewRegistry()
	code, _ := registry.GenerateReferralCode("acct-001")

	if _, err := registry.RecordReferral(code, "acct-001", time.Now()); !errors.Is(err, ErrSelfReferralNotAllowed) {
		t.Fatalf("expected ErrSelfReferralNotAllowed, got %v", err)
	}
}

func TestRecordReferralRejectsDoubleReferralOfSameAccount(t *testing.T) {
	registry := NewRegistry()
	codeA, _ := registry.GenerateReferralCode("acct-referrer-a")
	codeB, _ := registry.GenerateReferralCode("acct-referrer-b")

	if _, err := registry.RecordReferral(codeA, "acct-referred", time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := registry.RecordReferral(codeB, "acct-referred", time.Now()); !errors.Is(err, ErrAlreadyReferred) {
		t.Fatalf("expected ErrAlreadyReferred, got %v", err)
	}
}

func TestMarkRewardedTransitionsStatusAndIsNotDoubleApplied(t *testing.T) {
	registry := NewRegistry()
	code, _ := registry.GenerateReferralCode("acct-referrer")
	registry.RecordReferral(code, "acct-referred", time.Now())

	rewarded, err := registry.MarkRewarded("acct-referred", StandardReferralRewardInMinorUnits, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rewarded.Status != ReferralStatusRewarded {
		t.Fatalf("expected rewarded status, got %s", rewarded.Status)
	}
	if rewarded.RewardAmountInMinorUnits != StandardReferralRewardInMinorUnits {
		t.Fatalf("expected reward amount %d, got %d", StandardReferralRewardInMinorUnits, rewarded.RewardAmountInMinorUnits)
	}

	if _, err := registry.MarkRewarded("acct-referred", StandardReferralRewardInMinorUnits, time.Now()); !errors.Is(err, ErrAlreadyRewarded) {
		t.Fatalf("expected ErrAlreadyRewarded on second call, got %v", err)
	}
}

func TestMarkRewardedWithNoReferralLinkFails(t *testing.T) {
	registry := NewRegistry()

	if _, err := registry.MarkRewarded("acct-nobody", StandardReferralRewardInMinorUnits, time.Now()); !errors.Is(err, ErrNoReferralLink) {
		t.Fatalf("expected ErrNoReferralLink, got %v", err)
	}
}

func TestReferralsForReferrerListsEveryReferral(t *testing.T) {
	registry := NewRegistry()
	code, _ := registry.GenerateReferralCode("acct-referrer")

	registry.RecordReferral(code, "acct-referred-1", time.Now())
	registry.RecordReferral(code, "acct-referred-2", time.Now())

	referrals := registry.ReferralsForReferrer("acct-referrer")
	if len(referrals) != 2 {
		t.Fatalf("expected 2 referrals, got %d", len(referrals))
	}
}
