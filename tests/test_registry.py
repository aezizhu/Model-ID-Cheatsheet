"""Tests for the Universal Model Registry tools."""

from models_data import MODELS
from registry import (
    _format_table,
    _model_detail,
    list_models as _list_models,
    get_model_info as _get_model_info,
    recommend_model as _recommend_model,
    check_model_status as _check_model_status,
)

# FastMCP @mcp.tool() wraps functions in FunctionTool objects.
# Unwrap to get the raw callable for testing.
list_models = _list_models.fn
get_model_info = _get_model_info.fn
recommend_model = _recommend_model.fn
check_model_status = _check_model_status.fn


# ── Data integrity ────────────────────────────────────────────────────────


class TestModelsData:
    """Verify every model entry has the required schema."""

    REQUIRED_KEYS = {
        "id", "display_name", "provider", "context_window",
        "max_output_tokens", "vision", "reasoning", "pricing_input",
        "pricing_output", "knowledge_cutoff", "release_date", "status", "notes",
    }

    def test_all_models_have_required_keys(self):
        for model_id, model in MODELS.items():
            missing = self.REQUIRED_KEYS - set(model.keys())
            assert not missing, f"{model_id} missing keys: {missing}"

    def test_model_id_matches_dict_key(self):
        for key, model in MODELS.items():
            assert key == model["id"], f"Key {key!r} != model id {model['id']!r}"

    def test_status_values_are_valid(self):
        valid = {"current", "legacy", "deprecated"}
        for model_id, model in MODELS.items():
            assert model["status"] in valid, f"{model_id} has invalid status: {model['status']}"

    def test_pricing_is_non_negative(self):
        for model_id, model in MODELS.items():
            assert model["pricing_input"] >= 0, f"{model_id} has negative input pricing"
            assert model["pricing_output"] >= 0, f"{model_id} has negative output pricing"

    def test_context_window_is_positive(self):
        for model_id, model in MODELS.items():
            assert model["context_window"] > 0, f"{model_id} has non-positive context window"

    def test_at_least_three_providers(self):
        providers = {m["provider"] for m in MODELS.values()}
        assert len(providers) >= 3, f"Only {len(providers)} providers found"


# ── list_models ───────────────────────────────────────────────────────────


class TestListModels:
    def test_no_filters_returns_all(self):
        result = list_models()
        for model_id in MODELS:
            assert model_id in result

    def test_filter_by_provider(self):
        result = list_models(provider="Anthropic")
        assert "Anthropic" in result
        assert "OpenAI" not in result

    def test_filter_by_provider_case_insensitive(self):
        result = list_models(provider="anthropic")
        assert "Anthropic" in result

    def test_filter_by_status(self):
        result = list_models(status="deprecated")
        # All rows should be deprecated
        for line in result.split("\n")[2:]:  # skip header
            if line.strip():
                assert "deprecated" in line

    def test_filter_by_vision(self):
        result = list_models(capability="vision")
        # Should not contain models without vision (check table cell to avoid substring matches)
        non_vision = [m["id"] for m in MODELS.values() if not m["vision"]]
        for mid in non_vision:
            assert f"| {mid} |" not in result

    def test_filter_by_reasoning(self):
        result = list_models(capability="reasoning")
        non_reasoning = [m["id"] for m in MODELS.values() if not m["reasoning"]]
        for mid in non_reasoning:
            assert f"| {mid} |" not in result

    def test_no_results(self):
        result = list_models(provider="Nonexistent")
        assert "No models found" in result


# ── get_model_info ────────────────────────────────────────────────────────


class TestGetModelInfo:
    def test_exact_match(self):
        result = get_model_info("gpt-5")
        assert "GPT-5" in result
        assert "OpenAI" in result

    def test_case_insensitive(self):
        result = get_model_info("GPT-5")
        assert "GPT-5" in result

    def test_partial_match(self):
        result = get_model_info("opus-4-6")
        assert "Claude Opus 4.6" in result

    def test_not_found(self):
        result = get_model_info("nonexistent-model")
        assert "not found" in result


# ── recommend_model ───────────────────────────────────────────────────────


class TestRecommendModel:
    def test_coding_task(self):
        result = recommend_model("coding")
        assert "Recommendations for" in result
        # Should have numbered recommendations
        assert "1." in result

    def test_vision_task(self):
        result = recommend_model("image analysis")
        assert "vision" in result.lower()

    def test_cheap_budget(self):
        result = recommend_model("general tasks", budget="cheap")
        assert "Budget:** cheap" in result

    def test_reasoning_task(self):
        result = recommend_model("complex math reasoning")
        assert "reasoning" in result.lower()


# ── check_model_status ────────────────────────────────────────────────────


class TestCheckModelStatus:
    def test_current_model(self):
        result = check_model_status("gpt-5")
        assert "current" in result.lower()

    def test_legacy_model(self):
        result = check_model_status("gpt-4o")
        assert "legacy" in result.lower()
        assert "replacement" in result.lower()

    def test_deprecated_model(self):
        result = check_model_status("gpt-4o-mini")
        assert "deprecated" in result.lower()

    def test_not_found(self):
        result = check_model_status("fake-model")
        assert "not found" in result.lower()
