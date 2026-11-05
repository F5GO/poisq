# Стенд сравнения выравнивания текста по аудио (не часть сервиса poisq).
# Собирается один раз; дальше аудио и веса моделей берутся из примонтированных томов.
FROM python:3.12-slim

RUN apt-get update -qq && apt-get install -y -qq ffmpeg >/dev/null \
    && rm -rf /var/lib/apt/lists/*

# CPU-сборка PyTorch (работает на CPU без AVX2), затем Demucs и Qwen3-ForcedAligner
RUN pip install -q numpy torch torchaudio --index-url https://download.pytorch.org/whl/cpu \
        --extra-index-url https://pypi.org/simple \
    && pip install -q demucs soundfile qwen-asr \
    && pip install -q "audio-separator[cpu]"

# модели audio-separator кэшируются в примонтированный том (скачиваются при первом запуске)
ENV AUDIO_SEPARATOR_MODEL_DIR=/root/.cache/audio-separator-models

COPY aligneval.py /app/aligneval.py
ENTRYPOINT ["python", "/app/aligneval.py"]
