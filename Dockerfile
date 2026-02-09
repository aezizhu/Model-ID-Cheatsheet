FROM python:3.12-slim

LABEL org.opencontainers.image.source="https://github.com/aezi/universal-model-registry"
LABEL org.opencontainers.image.title="universal-model-registry"
LABEL org.opencontainers.image.description="MCP server exposing a curated registry of current AI models"

COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

WORKDIR /app

# Copy dependency files first for better layer caching
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev

# Copy application source
COPY models_data.py registry.py ./

# Create non-root user and switch to it
RUN useradd --create-home --shell /bin/bash app \
    && chown -R app:app /app
USER app

ENV MCP_TRANSPORT=sse
ENV PORT=8000

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD python -c "import urllib.request, os; urllib.request.urlopen(f'http://localhost:{os.environ.get(\"PORT\", \"8000\")}/sse')"

CMD ["uv", "run", "registry.py"]
