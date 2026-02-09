"""Detect new or retired models by comparing provider APIs against the local registry.

Usage:
    uv run python scripts/auto_update.py

Set API keys via environment variables:
    OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY,
    XAI_API_KEY, DEEPSEEK_API_KEY, MISTRAL_API_KEY
"""

import asyncio
import os
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

import httpx

# Add project root to path
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from models_data import MODELS

TIMEOUT = 15.0

# Model ID prefixes/keywords to skip (non-chat models, old generations, dated snapshots)
OPENAI_SKIP_PREFIXES = (
    "whisper",
    "tts",
    "dall-e",
    "text-embedding",
    "text-moderation",
    "babbage",
    "davinci",
    "canary-",
    "chatgpt-",
    "codex-",
    "omni-",
    "computer-use-",
    "gpt-3.5",  # legacy generation
    "gpt-4-",  # legacy GPT-4 variants (but not gpt-4o or gpt-4.1)
    "gpt-4o-audio",
    "gpt-4o-mini-audio",
    "gpt-4o-realtime",
    "gpt-4o-mini-realtime",
    "gpt-4o-transcribe",
    "gpt-4o-mini-transcribe",
    "gpt-4o-search",
    "gpt-4o-mini-search",
    "gpt-4o-mini-tts",
    "gpt-image",
    "gpt-audio",
    "gpt-realtime",
    "sora",
    "o1",  # deprecated reasoning model
)

# Dated snapshot pattern: models ending in -YYYY-MM-DD
_DATED_SNAPSHOT_RE = re.compile(r"-\d{4}-\d{2}-\d{2}$")

GOOGLE_SKIP_PREFIXES = (
    "embedding",
    "text-embedding",
    "aqa",
    "imagen",
    "veo",
    "gemma",
    "learnlm",
    "tunedModels",
)

XAI_SKIP_PREFIXES = (
    "embedding",
    "aurora",
    "grok-2",  # very old models
)

DEEPSEEK_SKIP_PREFIXES = (
    "deepseek-coder",
    "deepseek-embed",
)

MISTRAL_SKIP_PREFIXES = (
    "mistral-embed",
    "mistral-moderation",
    "mistral-ocr",
    "pixtral-large",  # alias for mistral-large vision
    "open-",
    "ft:",
    "pixtral-12b",
)


@dataclass
class ProviderDiff:
    """Diff results for a single provider."""

    provider: str
    api_models: set[str] = field(default_factory=set)
    registry_models: set[str] = field(default_factory=set)
    new_in_api: set[str] = field(default_factory=set)
    missing_from_api: set[str] = field(default_factory=set)
    skipped: bool = False
    skip_reason: str = ""
    error: str = ""


def registry_models_for(provider: str) -> set[str]:
    """Get model IDs from registry for a given provider."""
    return {mid for mid, m in MODELS.items() if m["provider"] == provider}


def should_skip_model(
    model_id: str,
    skip_prefixes: tuple[str, ...],
    *,
    filter_dated_snapshots: bool = False,
) -> bool:
    """Check if a model ID should be filtered out (non-chat models, snapshots)."""
    model_lower = model_id.lower()
    if any(model_lower.startswith(prefix) for prefix in skip_prefixes):
        return True
    return bool(filter_dated_snapshots and _DATED_SNAPSHOT_RE.search(model_id))


async def detect_openai(client: httpx.AsyncClient) -> ProviderDiff:
    """Detect new/retired OpenAI models."""
    diff = ProviderDiff(provider="OpenAI", registry_models=registry_models_for("OpenAI"))
    key = os.environ.get("OPENAI_API_KEY")
    if not key:
        diff.skipped = True
        diff.skip_reason = "OPENAI_API_KEY not set"
        return diff

    try:
        resp = await client.get(
            "https://api.openai.com/v1/models",
            headers={"Authorization": f"Bearer {key}"},
            timeout=TIMEOUT,
        )
        resp.raise_for_status()
        all_models = {m["id"] for m in resp.json()["data"]}
        diff.api_models = {
            mid
            for mid in all_models
            if not should_skip_model(mid, OPENAI_SKIP_PREFIXES, filter_dated_snapshots=True)
        }
    except Exception as e:
        diff.error = str(e)
        return diff

    diff.new_in_api = diff.api_models - diff.registry_models
    diff.missing_from_api = diff.registry_models - diff.api_models
    return diff


async def detect_anthropic(_client: httpx.AsyncClient) -> ProviderDiff:
    """Detect new/retired Anthropic models.

    Anthropic doesn't have a list-models endpoint, so we can only verify
    known models. New model detection requires manual review.
    """
    diff = ProviderDiff(provider="Anthropic", registry_models=registry_models_for("Anthropic"))
    diff.skipped = True
    diff.skip_reason = "No list-models API; manual review required for new models"
    return diff


async def detect_google(client: httpx.AsyncClient) -> ProviderDiff:
    """Detect new/retired Google models."""
    diff = ProviderDiff(provider="Google", registry_models=registry_models_for("Google"))
    key = os.environ.get("GOOGLE_API_KEY")
    if not key:
        diff.skipped = True
        diff.skip_reason = "GOOGLE_API_KEY not set"
        return diff

    try:
        resp = await client.get(
            f"https://generativelanguage.googleapis.com/v1beta/models?key={key}",
            timeout=TIMEOUT,
        )
        resp.raise_for_status()
        all_models = {m["name"].split("/")[-1] for m in resp.json().get("models", [])}
        diff.api_models = {
            mid for mid in all_models if not should_skip_model(mid, GOOGLE_SKIP_PREFIXES)
        }
    except Exception as e:
        diff.error = str(e)
        return diff

    diff.new_in_api = diff.api_models - diff.registry_models
    diff.missing_from_api = diff.registry_models - diff.api_models
    return diff


async def detect_xai(client: httpx.AsyncClient) -> ProviderDiff:
    """Detect new/retired xAI models."""
    diff = ProviderDiff(provider="xAI", registry_models=registry_models_for("xAI"))
    key = os.environ.get("XAI_API_KEY")
    if not key:
        diff.skipped = True
        diff.skip_reason = "XAI_API_KEY not set"
        return diff

    try:
        resp = await client.get(
            "https://api.x.ai/v1/models",
            headers={"Authorization": f"Bearer {key}"},
            timeout=TIMEOUT,
        )
        resp.raise_for_status()
        all_models = {m["id"] for m in resp.json()["data"]}
        diff.api_models = {
            mid for mid in all_models if not should_skip_model(mid, XAI_SKIP_PREFIXES)
        }
    except Exception as e:
        diff.error = str(e)
        return diff

    diff.new_in_api = diff.api_models - diff.registry_models
    diff.missing_from_api = diff.registry_models - diff.api_models
    return diff


async def detect_deepseek(client: httpx.AsyncClient) -> ProviderDiff:
    """Detect new/retired DeepSeek models."""
    diff = ProviderDiff(provider="DeepSeek", registry_models=registry_models_for("DeepSeek"))
    key = os.environ.get("DEEPSEEK_API_KEY")
    if not key:
        diff.skipped = True
        diff.skip_reason = "DEEPSEEK_API_KEY not set"
        return diff

    try:
        resp = await client.get(
            "https://api.deepseek.com/v1/models",
            headers={"Authorization": f"Bearer {key}"},
            timeout=TIMEOUT,
        )
        resp.raise_for_status()
        all_models = {m["id"] for m in resp.json()["data"]}
        diff.api_models = {
            mid for mid in all_models if not should_skip_model(mid, DEEPSEEK_SKIP_PREFIXES)
        }
    except Exception as e:
        diff.error = str(e)
        return diff

    diff.new_in_api = diff.api_models - diff.registry_models
    diff.missing_from_api = diff.registry_models - diff.api_models
    return diff


async def detect_mistral(client: httpx.AsyncClient) -> ProviderDiff:
    """Detect new/retired Mistral models."""
    diff = ProviderDiff(provider="Mistral", registry_models=registry_models_for("Mistral"))
    key = os.environ.get("MISTRAL_API_KEY")
    if not key:
        diff.skipped = True
        diff.skip_reason = "MISTRAL_API_KEY not set"
        return diff

    try:
        resp = await client.get(
            "https://api.mistral.ai/v1/models",
            headers={"Authorization": f"Bearer {key}"},
            timeout=TIMEOUT,
        )
        resp.raise_for_status()
        all_models = {m["id"] for m in resp.json()["data"]}
        diff.api_models = {
            mid for mid in all_models if not should_skip_model(mid, MISTRAL_SKIP_PREFIXES)
        }
    except Exception as e:
        diff.error = str(e)
        return diff

    diff.new_in_api = diff.api_models - diff.registry_models
    diff.missing_from_api = diff.registry_models - diff.api_models
    return diff


def detect_meta() -> ProviderDiff:
    """Meta has no direct API — always skip."""
    diff = ProviderDiff(provider="Meta", registry_models=registry_models_for("Meta"))
    diff.skipped = True
    diff.skip_reason = "No direct Meta API; open-weight models accessed via third-party providers"
    return diff


def print_report(diffs: list[ProviderDiff]) -> bool:
    """Print a structured diff report. Returns True if any changes were detected."""
    print("=" * 70)
    print("Universal Model Registry — Auto-Update Detection Report")
    print("=" * 70)
    print(
        f"Registry: {len(MODELS)} models across {len({m['provider'] for m in MODELS.values()})} providers"
    )
    print()

    total_new = 0
    total_missing = 0
    has_errors = False

    for diff in diffs:
        print(f"--- {diff.provider} ---")

        if diff.error:
            print(f"  [ERR] API error: {diff.error}")
            has_errors = True
            print()
            continue

        if diff.skipped:
            print(f"  [SKIP] {diff.skip_reason}")
            print(f"  Registry models: {len(diff.registry_models)}")
            print()
            continue

        print(f"  API models (filtered): {len(diff.api_models)}")
        print(f"  Registry models:       {len(diff.registry_models)}")

        if diff.new_in_api:
            total_new += len(diff.new_in_api)
            print(f"  [NEW] {len(diff.new_in_api)} model(s) in API but NOT in registry:")
            for mid in sorted(diff.new_in_api):
                print(f"    + {mid}")

        if diff.missing_from_api:
            total_missing += len(diff.missing_from_api)
            print(f"  [GONE] {len(diff.missing_from_api)} model(s) in registry but NOT in API:")
            for mid in sorted(diff.missing_from_api):
                print(f"    - {mid}")

        if not diff.new_in_api and not diff.missing_from_api:
            print("  [OK] Registry is up to date")

        print()

    # Summary
    print("=" * 70)
    print("SUMMARY")
    print("=" * 70)
    checked = sum(1 for d in diffs if not d.skipped and not d.error)
    skipped = sum(1 for d in diffs if d.skipped)
    errored = sum(1 for d in diffs if d.error)
    print(f"  Providers checked: {checked}  |  Skipped: {skipped}  |  Errors: {errored}")
    print(f"  New models found:  {total_new}")
    print(f"  Missing from API:  {total_missing}")

    if total_new > 0:
        print("\nACTION NEEDED: New models detected — consider adding to models_data.py")
    if total_missing > 0:
        print("ACTION NEEDED: Models missing from API — consider marking as deprecated/legacy")
    if total_new == 0 and total_missing == 0 and not has_errors:
        print("\nNo changes detected. Registry is up to date.")

    return total_new > 0 or total_missing > 0


async def main() -> None:
    async with httpx.AsyncClient() as client:
        # Run all provider checks concurrently
        results = await asyncio.gather(
            detect_openai(client),
            detect_anthropic(client),
            detect_google(client),
            detect_xai(client),
            detect_deepseek(client),
            detect_mistral(client),
        )

    diffs = list(results) + [detect_meta()]
    has_changes = print_report(diffs)

    if has_changes:
        sys.exit(1)


if __name__ == "__main__":
    asyncio.run(main())
