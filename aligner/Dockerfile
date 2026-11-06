# poisq-aligner: сервис акустического выравнивания текста по аудио (тайминги строк для .lrc).
# Контекст сборки — корень репозитория:  docker build -f aligner/Dockerfile -t poisq-aligner .
#
# Конвейер: Demucs (вокал) → ансамбль Whisper (Groq) → контекстное выравнивание Qwen3-ForcedAligner →
# арбитраж по независимому CTC-выравнивателю MMS. Нужен только CPU; AVX2 не требуется.
FROM python:3.12-slim

RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends ffmpeg >/dev/null \
    && rm -rf /var/lib/apt/lists/*

# CPU-сборка PyTorch (работает без AVX2), затем Demucs, Qwen3-ForcedAligner и клиент Groq.
# torchvision не нужен (только аудио) и при несовпадении версий ломает импорт transformers.
RUN pip install --no-cache-dir -q numpy torch torchaudio --index-url https://download.pytorch.org/whl/cpu \
        --extra-index-url https://pypi.org/simple \
    && pip install --no-cache-dir -q demucs soundfile qwen-asr requests \
    && (pip uninstall -y -q torchvision || true)

# Модели (Demucs, Qwen3-ForcedAligner, MMS_FA) скачиваются при первом запуске в том /cache и дальше берутся оттуда
ENV HF_HOME=/cache/hf \
    TORCH_HOME=/cache/torch \
    ALIGNER_CACHE_DIR=/cache \
    MUSIC_ROOT=/music \
    ALIGNER_PORT=4540 \
    PYTHONUNBUFFERED=1

WORKDIR /app
COPY tools/aligneval/aligneval.py /app/aligneval.py
COPY aligner/confidence.py aligner/runner.py aligner/server.py /app/

VOLUME ["/cache"]
EXPOSE 4540
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:4540/healthz', timeout=4)" || exit 1

CMD ["python", "/app/server.py"]
