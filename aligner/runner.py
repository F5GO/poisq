"""Запуск конвейера выравнивания и сборка результата.

Конвейер — tools/aligneval/aligneval.py (в образе лежит как /app/aligneval.py): Demucs → ансамбль Whisper (Groq) →
контекстное выравнивание Qwen → арбитраж по MMS. Он запускается подпроцессом (изоляция памяти и падений,
например SIGILL на процессорах без AVX2) и пишет lines.json со всеми оценками по строкам.
"""
import hashlib
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import threading
import time

import confidence

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_ALIGNEVAL = next((p for p in (os.path.join(HERE, "aligneval.py"),
                                      os.path.join(HERE, "..", "tools", "aligneval", "aligneval.py")) if os.path.exists(p)),
                         os.path.join(HERE, "aligneval.py"))

LANG_NAMES = {"ru": "Russian", "en": "English", "de": "German", "fr": "French", "es": "Spanish", "it": "Italian",
              "pt": "Portuguese", "uk": "Ukrainian", "pl": "Polish", "nl": "Dutch", "tr": "Turkish", "sv": "Swedish",
              "ja": "Japanese", "ko": "Korean", "zh": "Chinese"}

# Временные файлы, которые не нужны после задачи (стем вокала и ответы Groq остаются для повторных запусков)
TEMP_PATTERNS = (r"^stems$", r"^track_mix\.wav$", r"^track_mix16k\.wav$", r"^(anchor|ctx\w*|gap|refine)_\d+\.wav$",
                 r"^chunk\.wav$", r"^vocals_fast\.wav$", r".*\.upload\.flac$")


class AlignError(Exception):
    """Ошибка конвейера с кодом для клиента: quota, invalid_key, cpu_unsupported, bad_request, timeout, failed."""

    def __init__(self, code, message):
        super().__init__(message)
        self.code, self.message = code, message


class Cancelled(Exception):
    pass


def classify_failure(returncode, output):
    low = output.lower()
    if "groq http 429" in low:
        return "quota"
    if "groq http 401" in low or "groq http 403" in low:
        return "invalid_key"
    if returncode in (-4, 132) or "illegal instruction" in low:
        return "cpu_unsupported"
    if "no space left" in low:
        return "disk"
    return "failed"


def is_progress_line(line):
    s = line.strip()
    return bool(s) and len(s) < 240 and s.startswith("[") and "ffmpeg" not in s


class Runner:
    def __init__(self, cache_dir, music_root, aligneval=None, python=None, threads=0, keep_workdirs=30,
                 lead=0.5, max_sec=1500, timeout=5400, asr_variants="vocals,prompt,mix,chunks,turbo,fast",
                 demucs_device="", qwen_device="cpu"):
        self.cache_dir = os.path.abspath(cache_dir)
        self.work_root = os.path.join(self.cache_dir, "work")
        self.music_root = os.path.realpath(music_root)
        self.aligneval = aligneval or DEFAULT_ALIGNEVAL
        self.python = python or sys.executable
        self.threads = threads
        self.keep_workdirs = keep_workdirs
        self.lead = lead
        self.max_sec = max_sec
        self.timeout = timeout
        self.asr_variants = asr_variants
        self.demucs_device = demucs_device  # "", "cpu" или "mps" (Apple Silicon)
        self.qwen_device = qwen_device      # "cpu" или "mps"
        os.makedirs(self.work_root, exist_ok=True)

    # --- безопасность и пути -------------------------------------------------------------------------------
    def validate_audio(self, path):
        """Путь к аудио обязан лежать внутри каталога медиатеки (после раскрытия ссылок)."""
        if not path or not isinstance(path, str):
            raise AlignError("bad_request", "audio_path не задан")
        real = os.path.realpath(path)
        root = self.music_root.rstrip(os.sep) + os.sep
        if not (real + os.sep).startswith(root):
            raise AlignError("bad_request", "audio_path вне каталога медиатеки")
        if not os.path.isfile(real):
            raise AlignError("bad_request", "аудиофайл не найден: " + path)
        return real

    def workdir_for(self, audio):
        st = os.stat(audio)
        key = hashlib.sha1(("%s|%d|%d" % (audio, st.st_size, int(st.st_mtime))).encode("utf-8")).hexdigest()[:16]
        return os.path.join(self.work_root, key)

    def cleanup_workdir(self, wd):
        for name in os.listdir(wd):
            if any(re.match(p, name) for p in TEMP_PATTERNS):
                full = os.path.join(wd, name)
                shutil.rmtree(full, ignore_errors=True) if os.path.isdir(full) else os.remove(full)

    def evict_old_workdirs(self):
        dirs = [os.path.join(self.work_root, d) for d in os.listdir(self.work_root)]
        dirs = [d for d in dirs if os.path.isdir(d)]
        dirs.sort(key=os.path.getmtime, reverse=True)
        for d in dirs[self.keep_workdirs:]:
            shutil.rmtree(d, ignore_errors=True)

    # --- запуск -----------------------------------------------------------------------------------------------
    def build_command(self, audio, lines_file, wd, lang_name):
        cmd = [self.python, self.aligneval, audio, "--text", lines_file, "--work", wd,
               "--variants", "vocals", "--asr-anchor", "--only-anchor",
               "--asr-variants", self.asr_variants, "--fuse", "median",
               "--ctx-align", "--ctx-gap", "4", "--ctx-rounds", "1", "--mms-ctx",
               "--max-sec", str(self.max_sec)]
        if lang_name:
            cmd += ["--lang", lang_name]
        if self.demucs_device:
            cmd += ["--demucs-device", self.demucs_device]
        if self.qwen_device and self.qwen_device != "cpu":
            cmd += ["--device", self.qwen_device]
        return cmd

    def run(self, job, progress):
        """Выполняет задачу. job: audio_path, lines, lang, groq_key, fresh, cancel (threading.Event), proc_holder (dict).
        progress(msg) вызывается для заметных этапов. Возвращает результат (confidence.build_result)."""
        t0 = time.time()
        audio = self.validate_audio(job["audio_path"])
        lines = job["lines"]
        kept = confidence.kept_lines(lines)
        if len(kept) < 3:
            raise AlignError("bad_request", "в тексте слишком мало строк для выравнивания (нужно не менее 3)")
        key = job.get("groq_key") or os.environ.get("GROQ_API_KEY", "")
        if not key:
            raise AlignError("invalid_key", "не передан ключ Groq")
        wd = self.workdir_for(audio)
        if job.get("fresh"):
            shutil.rmtree(wd, ignore_errors=True)
        os.makedirs(wd, exist_ok=True)
        os.utime(wd, None)
        lines_file = os.path.join(wd, "lines_input.txt")
        with open(lines_file, "w", encoding="utf-8") as f:
            f.write("\n".join(t for _, t in kept) + "\n")
        lj = os.path.join(wd, "lines.json")
        if os.path.exists(lj):
            os.remove(lj)

        lang = (job.get("lang") or "").strip()
        lang_name = LANG_NAMES.get(lang.lower(), lang if lang[:1].isupper() else "")
        env = dict(os.environ)
        env.update({"GROQ_API_KEY": key, "PYTHONUNBUFFERED": "1", "PYTORCH_ENABLE_MPS_FALLBACK": "1"})
        if self.threads > 0:
            env.update({"OMP_NUM_THREADS": str(self.threads), "MKL_NUM_THREADS": str(self.threads)})

        progress("Старт конвейера выравнивания (строк: %d)" % len(kept))
        proc = subprocess.Popen(self.build_command(audio, lines_file, wd, lang_name), stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True, env=env, cwd=wd, start_new_session=True)
        job["proc_holder"]["proc"] = proc
        timed_out = {"v": False}

        def kill(reason):
            if proc.poll() is None:
                if reason == "timeout":
                    timed_out["v"] = True
                try:
                    os.killpg(proc.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass

        timer = threading.Timer(self.timeout, kill, args=("timeout",))
        timer.daemon = True
        timer.start()
        tail = []
        try:
            for raw in proc.stdout:
                line = raw.rstrip("\n")
                tail.append(line)
                del tail[:-40]
                if job["cancel"].is_set():
                    kill("cancel")
                if is_progress_line(line):
                    progress(line.strip())
            rc = proc.wait()
        finally:
            timer.cancel()
            if proc.poll() is None:
                kill("cancel")
        if job["cancel"].is_set():
            raise Cancelled()
        if timed_out["v"]:
            raise AlignError("timeout", "превышено время выполнения (%d с)" % self.timeout)
        if rc != 0:
            output = "\n".join(tail)
            raise AlignError(classify_failure(rc, output), "конвейер завершился с кодом %d: %s" % (rc, tail[-1] if tail else ""))
        if not os.path.exists(lj):
            raise AlignError("failed", "конвейер не создал lines.json")
        with open(lj, encoding="utf-8") as f:
            dump = json.load(f)
        result = confidence.build_result(lines, dump, self.lead)
        result["summary"]["seconds"] = round(time.time() - t0, 1)
        try:
            self.cleanup_workdir(wd)
            self.evict_old_workdirs()
        except OSError:
            pass
        return result
