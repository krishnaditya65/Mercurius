package orders

import "testing"

func TestValidateOrderExecutionTypeAllowsEmptyForBackwardCompatibility(t *testing.T) {
	request := OrderSubmissionRequest{OrderQuantity: 10}
	if err := ValidateOrderExecutionType(request); err != nil {
		t.Fatalf("expected empty OrderExecutionType to be valid, got %v", err)
	}
}

func TestValidateOrderExecutionTypeAllowsEachOrdinaryType(t *testing.T) {
	for _, executionType := range []string{
		OrderExecutionTypeMarket,
		OrderExecutionTypeLimit,
		OrderExecutionTypeStopLoss,
		OrderExecutionTypeStopLossMarket,
	} {
		request := OrderSubmissionRequest{OrderQuantity: 10, OrderExecutionType: executionType}
		if err := ValidateOrderExecutionType(request); err != nil {
			t.Fatalf("expected %s to be valid, got %v", executionType, err)
		}
	}
}

func TestValidateOrderExecutionTypeAllowsFillOrKillWithNoExtraFields(t *testing.T) {
	request := OrderSubmissionRequest{OrderQuantity: 10, OrderExecutionType: OrderExecutionTypeFillOrKill}
	if err := ValidateOrderExecutionType(request); err != nil {
		t.Fatalf("expected FOK to be valid, got %v", err)
	}
}

func TestValidateOrderExecutionTypeAllowsImmediateOrCancelWithNoExtraFields(t *testing.T) {
	request := OrderSubmissionRequest{OrderQuantity: 10, OrderExecutionType: OrderExecutionTypeImmediateOrCancel}
	if err := ValidateOrderExecutionType(request); err != nil {
		t.Fatalf("expected IOC to be valid, got %v", err)
	}
}

func TestValidateOrderExecutionTypeRejectsUnknownType(t *testing.T) {
	request := OrderSubmissionRequest{OrderQuantity: 10, OrderExecutionType: "BRACKET"}
	if err := ValidateOrderExecutionType(request); err != ErrUnknownOrderExecutionType {
		t.Fatalf("expected ErrUnknownOrderExecutionType, got %v", err)
	}
}

func TestValidateOrderExecutionTypeIcebergRequiresVisibleQuantity(t *testing.T) {
	request := OrderSubmissionRequest{OrderQuantity: 100, OrderExecutionType: OrderExecutionTypeIceberg}
	if err := ValidateOrderExecutionType(request); err != ErrIcebergRequiresVisibleQuantity {
		t.Fatalf("expected ErrIcebergRequiresVisibleQuantity, got %v", err)
	}
}

func TestValidateOrderExecutionTypeIcebergRejectsZeroVisibleQuantity(t *testing.T) {
	zero := uint64(0)
	request := OrderSubmissionRequest{OrderQuantity: 100, OrderExecutionType: OrderExecutionTypeIceberg, IcebergVisibleQuantity: &zero}
	if err := ValidateOrderExecutionType(request); err != ErrIcebergVisibleQuantityMustBePositive {
		t.Fatalf("expected ErrIcebergVisibleQuantityMustBePositive, got %v", err)
	}
}

func TestValidateOrderExecutionTypeIcebergRejectsVisibleQuantityExceedingTotal(t *testing.T) {
	visible := uint64(150)
	request := OrderSubmissionRequest{OrderQuantity: 100, OrderExecutionType: OrderExecutionTypeIceberg, IcebergVisibleQuantity: &visible}
	if err := ValidateOrderExecutionType(request); err != ErrIcebergVisibleQuantityExceedsTotalQuantity {
		t.Fatalf("expected ErrIcebergVisibleQuantityExceedsTotalQuantity, got %v", err)
	}
}

func TestValidateOrderExecutionTypeIcebergAcceptsValidVisibleQuantity(t *testing.T) {
	visible := uint64(10)
	request := OrderSubmissionRequest{OrderQuantity: 100, OrderExecutionType: OrderExecutionTypeIceberg, IcebergVisibleQuantity: &visible}
	if err := ValidateOrderExecutionType(request); err != nil {
		t.Fatalf("expected a valid iceberg order to pass, got %v", err)
	}
}

func TestValidateOrderExecutionTypeIcebergAcceptsVisibleQuantityEqualToTotal(t *testing.T) {
	visible := uint64(100)
	request := OrderSubmissionRequest{OrderQuantity: 100, OrderExecutionType: OrderExecutionTypeIceberg, IcebergVisibleQuantity: &visible}
	if err := ValidateOrderExecutionType(request); err != nil {
		t.Fatalf("expected visibleQuantity == total quantity to be valid, got %v", err)
	}
}
