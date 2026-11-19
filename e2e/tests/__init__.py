"""Opaque-box E2E test modules across Tiers 1, 2, 3, and 4 (ordered F1 -> F12 per tier)."""

from __future__ import annotations

from . import tier1_storage_agent_and_archive  # F1..F4
from . import tier1_backend_pipelines  # F5..F9
from . import tier1_trainer_and_ui  # F10..F12
from . import tier2_storage_agent_and_security  # F1..F4
from . import tier2_backend_negative  # F5..F9
from . import tier2_trainer_and_ui_negative  # F10..F12
from . import tier3_combinations  # Cross-feature pairwise
from . import tier4_scenarios  # Real-world end-to-end scenarios

__all__ = [
    "tier1_storage_agent_and_archive",
    "tier1_backend_pipelines",
    "tier1_trainer_and_ui",
    "tier2_storage_agent_and_security",
    "tier2_backend_negative",
    "tier2_trainer_and_ui_negative",
    "tier3_combinations",
    "tier4_scenarios",
]
