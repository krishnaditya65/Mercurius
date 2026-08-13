// Package kycstate is the actual KYC verification logic for
// kyc-onboarding — see FEATURES.md §1 for the full intended scope
// (PAN/Aadhaar verification, liveness + selfie match, e-signature,
// nominee management). This is a deliberately simplified skeleton of
// just the PAN-format-check + status slice of that, real enough that
// oms-gateway can gate order submission on it end-to-end.
//
// TODO(real build): "verification" here is a PAN format check only, not
// an actual call to a KYC verification provider (NSDL/UIDAI-equivalent
// APIs, liveness detection, document OCR). Treat VERIFIED as "passed the
// cheapest possible check", not "actually KYC-compliant".
package kycstate

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
)

type KycVerificationStage string

const (
	StageNotSubmitted KycVerificationStage = "NOT_SUBMITTED"
	StageVerified     KycVerificationStage = "VERIFIED"
	StageRejected     KycVerificationStage = "REJECTED"
)

// panNumberFormatPattern matches the standard 10-character Indian PAN
// format: 5 letters, 4 digits, 1 letter (e.g. ABCDE1234F). This is a
// FORMAT check only — it says nothing about whether the PAN is real or
// belongs to the submitting account holder.
var panNumberFormatPattern = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)

type KycRecord struct {
	AccountIdentifier  string
	VerificationStage  KycVerificationStage
	SubmittedPanNumber string
	SubmittedFullName  string
	RejectionReason    string
}

// KycVerificationStateMachine is an in-memory store of KYC records, one
// per account. TODO(real build): needs to be persisted (Postgres) and
// this whole package needs to become an actual workflow — document
// upload, provider callback, manual review queue — not a synchronous
// format check.
type KycVerificationStateMachine struct {
	mutexGuardingRecords sync.RWMutex
	recordsByAccountId   map[string]KycRecord
}

func NewKycVerificationStateMachine() *KycVerificationStateMachine {
	return &KycVerificationStateMachine{
		recordsByAccountId: make(map[string]KycRecord),
	}
}

// SubmitKycDetails validates the PAN format and immediately marks the
// account VERIFIED or REJECTED — no async review step in the AUTOMATED
// path (a real build's `PENDING_REVIEW` stage between these two would
// sit here). What IS real now: `ListRecordsByStage`/`OverrideStage`
// below give a human admin a genuine way to browse and manually
// re-decide any REJECTED (or VERIFIED) record after the fact — the
// review QUEUE FEATURES.md §14 calls for, even though the initial
// submission itself still auto-decides rather than always queuing for
// review first.
func (stateMachine *KycVerificationStateMachine) SubmitKycDetails(
	accountIdentifier string,
	panNumber string,
	fullName string,
) KycRecord {
	stateMachine.mutexGuardingRecords.Lock()
	defer stateMachine.mutexGuardingRecords.Unlock()

	record := KycRecord{
		AccountIdentifier:  accountIdentifier,
		SubmittedPanNumber: panNumber,
		SubmittedFullName:  fullName,
	}

	if fullName == "" {
		record.VerificationStage = StageRejected
		record.RejectionReason = "full name is required"
	} else if !panNumberFormatPattern.MatchString(panNumber) {
		record.VerificationStage = StageRejected
		record.RejectionReason = fmt.Sprintf("%q is not a validly-formatted PAN (expected AAAAA9999A)", panNumber)
	} else {
		record.VerificationStage = StageVerified
	}

	stateMachine.recordsByAccountId[accountIdentifier] = record
	return record
}

// LookupKycStatus returns the current record, or a StageNotSubmitted
// placeholder for an account that has never submitted KYC details.
func (stateMachine *KycVerificationStateMachine) LookupKycStatus(accountIdentifier string) KycRecord {
	stateMachine.mutexGuardingRecords.RLock()
	defer stateMachine.mutexGuardingRecords.RUnlock()

	record, wasFound := stateMachine.recordsByAccountId[accountIdentifier]
	if !wasFound {
		return KycRecord{AccountIdentifier: accountIdentifier, VerificationStage: StageNotSubmitted}
	}
	return record
}

func (record KycRecord) IsEligibleToPlaceOrders() bool {
	return record.VerificationStage == StageVerified
}

var ErrNoRecordToOverride = fmt.Errorf("no KYC record exists for this account to override — it must have been submitted at least once")
var ErrInvalidOverrideStage = fmt.Errorf("override stage must be VERIFIED or REJECTED — an admin action always makes an explicit decision, never resets to NOT_SUBMITTED or leaves it pending")

// ListRecordsByStage returns every stored record currently in stage,
// sorted by account identifier for a deterministic response — this is
// the actual "KYC review queue" a real admin panel would page through
// (FEATURES.md §14). Calling this with StageRejected is the primary use
// case: surfacing every automatically-rejected submission for a human
// to look at and potentially overturn.
func (stateMachine *KycVerificationStateMachine) ListRecordsByStage(stage KycVerificationStage) []KycRecord {
	stateMachine.mutexGuardingRecords.RLock()
	defer stateMachine.mutexGuardingRecords.RUnlock()

	matchingRecords := make([]KycRecord, 0)
	for _, record := range stateMachine.recordsByAccountId {
		if record.VerificationStage == stage {
			matchingRecords = append(matchingRecords, record)
		}
	}
	sort.Slice(matchingRecords, func(i, j int) bool {
		return matchingRecords[i].AccountIdentifier < matchingRecords[j].AccountIdentifier
	})
	return matchingRecords
}

// OverrideStage is the actual admin decision an entry in the review
// queue resolves to: a human explicitly setting an account's KYC stage
// to VERIFIED or REJECTED, with a recorded reason — e.g. overturning an
// automated rejection after manually confirming the PAN was just a
// typo, or retroactively rejecting a previously-VERIFIED account after
// discovering a problem. Requires the account to have submitted at
// least once (there's nothing to "override" for an account that never
// submitted anything — that's still just NOT_SUBMITTED).
func (stateMachine *KycVerificationStateMachine) OverrideStage(
	accountIdentifier string,
	newStage KycVerificationStage,
	overrideReason string,
) (KycRecord, error) {
	if newStage != StageVerified && newStage != StageRejected {
		return KycRecord{}, ErrInvalidOverrideStage
	}

	stateMachine.mutexGuardingRecords.Lock()
	defer stateMachine.mutexGuardingRecords.Unlock()

	existingRecord, wasFound := stateMachine.recordsByAccountId[accountIdentifier]
	if !wasFound {
		return KycRecord{}, ErrNoRecordToOverride
	}

	existingRecord.VerificationStage = newStage
	if newStage == StageRejected {
		existingRecord.RejectionReason = overrideReason
	} else {
		existingRecord.RejectionReason = ""
	}
	stateMachine.recordsByAccountId[accountIdentifier] = existingRecord

	return existingRecord, nil
}
