"""Shared floating-point tolerance for "is this position flat?" checks
across `backtesting/`.

`PortfolioState.positionQuantity` (see `backtestRunner.py`) is
accumulated via repeated float `+=`/`-=` across many trades over a
backtest run. A position that is genuinely, economically flat (fully
closed) can therefore land at something like `1e-14` instead of exactly
`0.0` due to ordinary floating-point rounding error — an exact
`positionQuantity == 0` equality check misclassifies that as still
"long" or "short" by a hair, which for a strategy that only acts while
flat (e.g. `pairsTradingStrategy.py`'s entry rule) can permanently wedge
the strategy into HOLD forever.

`POSITION_FLAT_EPSILON` is intentionally far smaller than any realistic
traded quantity (shares/contracts/units are never legitimately traded in
sub-`1e-9` fractions in this codebase) while comfortably absorbing
float64 accumulation error over any realistic backtest length, so it
never masks a real, economically meaningful non-flat position.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory), except for this
one SCREAMING_SNAKE_CASE module-level constant, which follows the
standard Python constant convention used elsewhere in this codebase
(e.g. `regimeDetectionHmm._MINIMUM_VARIANCE_FLOOR`-style constants).
"""

from __future__ import annotations

POSITION_FLAT_EPSILON = 1e-9


def isPositionFlat(positionQuantity: float) -> bool:
    """True if `positionQuantity` is flat (zero) within
    `POSITION_FLAT_EPSILON` — the tolerance-based replacement for an
    exact `positionQuantity == 0` comparison.
    """
    return abs(positionQuantity) < POSITION_FLAT_EPSILON
