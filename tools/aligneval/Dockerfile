# Стенд сравнения выравнивания текста по аудио (не часть сервиса poisq).
# Собирается один раз; дальше аудио и веса моделей берутся из примонтированных томов.
FROM python:3.12-slim

RUN apt-get update -qq && apt-get install -y -qq ffmpeg >/dev/null \
    && rm -rf /var/lib/apt/lists/*

# CPU-сборка PyTorch (работает на CPU без AVX2), затем Demucs и Qwen3-ForcedAligner
RUN pip install -q numpy torch torchaudio --index-url https://download.pytorch.org/whl/cpu \
        --extra-index-url https://pypi.org/simple \
    && pip install -q demucs soundfile qwen-asr

# audio-separator тянет diffq, который собирается из исходников на C (в slim-образе нет компилятора).
# Отдельный слой: тяжёлый слой с PyTorch выше остаётся в кэше.
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends build-essential >/dev/null \
    && pip install -q "audio-separator[cpu]" \
    && apt-get purge -y -qq build-essential >/dev/null && apt-get autoremove -y -qq >/dev/null \
    && rm -rf /var/lib/apt/lists/*

# audioread не объявлен в зависимостях audio-separator (0.48.0), но импортируется при запуске; setuptools нужен для pkg_resources
RUN pip install -q audioread "setuptools<81"

# torchvision здесь не нужен (только аудио), а после установки audio-separator его бинарные операторы
# перестают соответствовать torch: transformers (через qwen-asr) при импорте падает с «torchvision::nms does not exist»
RUN pip uninstall -y -q torchvision || true

# модели audio-separator кэшируются в примонтированный том (скачиваются при первом запуске)
ENV AUDIO_SEPARATOR_MODEL_DIR=/root/.cache/audio-separator-models

COPY aligneval.py /app/aligneval.py
ENTRYPOINT ["python", "/app/aligneval.py"]
