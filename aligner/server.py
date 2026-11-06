#!/usr/bin/env python3
"""HTTP-сервис выравнивания текста по аудио (poisq-aligner).

API (JSON):
  GET    /healthz                 → {"ok": true, "queue": N, "running": id|null, "version": "..."}
  POST   /v1/jobs                 → 202 {"id": "...", "status": "queued"}
         тело: {"audio_path": "/music/.../01.flac", "lines": ["строка", ...], "lang": "ru", "track_id": "...",
                "groq_key": "...", "fresh": false}
  GET    /v1/jobs/<id>            → {"id", "status": queued|running|done|error|cancelled, "progress": [...],
                                    "error", "error_code", "result": {lines, lrc, summary}}
  DELETE /v1/jobs/<id>            → отмена (в очереди — удаление, выполняется — остановка конвейера)

Задачи выполняются по одной (конвейер нагружает процессор). Путь к аудио должен лежать внутри MUSIC_ROOT.
Если задан ALIGNER_TOKEN, нужен заголовок Authorization: Bearer <token>.
"""
import json
import os
import queue
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import runner as runner_mod

VERSION = "1.0.0"
MAX_LINES = 2000
MAX_LINE_LEN = 600
RESULT_TTL = 24 * 3600


class Job:
    def __init__(self, req):
        self.id = uuid.uuid4().hex[:16]
        self.req = req
        self.status = "queued"
        self.progress = []
        self.error = ""
        self.error_code = ""
        self.result = None
        self.created = time.time()
        self.started = None
        self.finished = None
        self.cancel = threading.Event()
        self.proc_holder = {}

    def add_progress(self, msg):
        self.progress.append({"t": round(time.time() - self.created, 1), "msg": msg})
        del self.progress[:-200]

    def public(self, with_result=True):
        d = {"id": self.id, "track_id": self.req.get("track_id", ""), "status": self.status, "progress": self.progress,
             "error": self.error, "error_code": self.error_code, "created": self.created, "started": self.started,
             "finished": self.finished}
        if with_result and self.result is not None:
            d["result"] = self.result
        return d


class Service:
    def __init__(self, runner, jobs_dir):
        self.runner = runner
        self.jobs_dir = jobs_dir
        os.makedirs(jobs_dir, exist_ok=True)
        self.jobs = {}
        self.lock = threading.Lock()
        self.q = queue.Queue()
        self.running_id = None
        self._load_finished()
        self.worker = threading.Thread(target=self._loop, daemon=True)
        self.worker.start()

    # --- хранение результатов --------------------------------------------------------------------------------
    def _path(self, jid):
        return os.path.join(self.jobs_dir, jid + ".json")

    def _persist(self, job):
        tmp = self._path(job.id) + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(job.public(), f, ensure_ascii=False)
        os.replace(tmp, self._path(job.id))

    def _load_finished(self):
        now = time.time()
        for name in os.listdir(self.jobs_dir):
            if not name.endswith(".json"):
                continue
            p = os.path.join(self.jobs_dir, name)
            try:
                if now - os.path.getmtime(p) > RESULT_TTL:
                    os.remove(p)
                    continue
                with open(p, encoding="utf-8") as f:
                    d = json.load(f)
                j = Job({"track_id": d.get("track_id", "")})
                j.id, j.status, j.progress = d["id"], d["status"], d.get("progress", [])
                j.error, j.error_code, j.result = d.get("error", ""), d.get("error_code", ""), d.get("result")
                j.created, j.started, j.finished = d.get("created", now), d.get("started"), d.get("finished")
                self.jobs[j.id] = j
            except (OSError, ValueError, KeyError):
                continue

    # --- API -----------------------------------------------------------------------------------------------------
    def submit(self, req):
        lines = req.get("lines")
        if not isinstance(lines, list) or not lines or not all(isinstance(x, str) for x in lines):
            raise runner_mod.AlignError("bad_request", "lines должен быть непустым списком строк")
        if len(lines) > MAX_LINES or any(len(x) > MAX_LINE_LEN for x in lines):
            raise runner_mod.AlignError("bad_request", "слишком много или слишком длинные строки текста")
        self.runner.validate_audio(req.get("audio_path"))
        with self.lock:
            tid = req.get("track_id") or ""
            if tid:
                for j in self.jobs.values():
                    if j.req.get("track_id") == tid and j.status in ("queued", "running"):
                        return j  # идемпотентность: для трека уже есть активная задача
            job = Job(req)
            self.jobs[job.id] = job
        self.q.put(job.id)
        return job

    def get(self, jid):
        with self.lock:
            return self.jobs.get(jid)

    def cancel(self, jid):
        with self.lock:
            job = self.jobs.get(jid)
            if job is None:
                return None
            job.cancel.set()
            if job.status == "queued":
                job.status, job.finished = "cancelled", time.time()
            proc = job.proc_holder.get("proc")
        if proc is not None and proc.poll() is None:
            try:
                import signal
                os.killpg(proc.pid, signal.SIGTERM)
            except (ProcessLookupError, OSError):
                pass
        return job

    def stats(self):
        return {"ok": True, "version": VERSION, "queue": self.q.qsize(), "running": self.running_id}

    # --- рабочий цикл --------------------------------------------------------------------------------------------
    def _loop(self):
        while True:
            jid = self.q.get()
            job = self.get(jid)
            if job is None or job.status != "queued":
                continue
            self.running_id = jid
            job.status, job.started = "running", time.time()
            try:
                job.result = self.runner.run({**job.req, "cancel": job.cancel, "proc_holder": job.proc_holder}, job.add_progress)
                job.status = "done"
                job.add_progress("Готово: строк %d, неуверенных %d" % (job.result["summary"]["lines"], job.result["summary"]["low"]))
            except runner_mod.Cancelled:
                job.status = "cancelled"
            except runner_mod.AlignError as e:
                job.status, job.error, job.error_code = "error", e.message, e.code
            except Exception as e:  # noqa: BLE001
                job.status, job.error, job.error_code = "error", "%s: %s" % (type(e).__name__, e), "failed"
            finally:
                job.finished = time.time()
                job.req = {k: v for k, v in job.req.items() if k != "groq_key"}  # ключ не хранится после задачи
                self.running_id = None
                try:
                    self._persist(job)
                except OSError:
                    pass


def make_handler(service, token):
    class Handler(BaseHTTPRequestHandler):
        server_version = "poisq-aligner/" + VERSION

        def log_message(self, fmt, *args):  # тише: только ошибки и важные события
            pass

        def _send(self, code, obj):
            body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def _authorized(self):
            if not token:
                return True
            return self.headers.get("Authorization", "") == "Bearer " + token

        def _guard(self):
            if not self._authorized():
                self._send(401, {"error": "unauthorized", "error_code": "unauthorized"})
                return False
            return True

        def do_GET(self):
            if self.path == "/healthz":
                return self._send(200, service.stats())
            if not self._guard():
                return
            if self.path.startswith("/v1/jobs/"):
                job = service.get(self.path.rsplit("/", 1)[1])
                if job is None:
                    return self._send(404, {"error": "job not found", "error_code": "not_found"})
                return self._send(200, job.public())
            self._send(404, {"error": "not found"})

        def do_POST(self):
            if self.path != "/v1/jobs":
                return self._send(404, {"error": "not found"})
            if not self._guard():
                return
            try:
                n = int(self.headers.get("Content-Length", "0"))
                req = json.loads(self.rfile.read(n).decode("utf-8")) if n else {}
                job = service.submit(req)
            except runner_mod.AlignError as e:
                return self._send(400, {"error": e.message, "error_code": e.code})
            except (ValueError, UnicodeDecodeError):
                return self._send(400, {"error": "некорректный JSON", "error_code": "bad_request"})
            self._send(202, {"id": job.id, "status": job.status})

        def do_DELETE(self):
            if not self._guard():
                return
            if self.path.startswith("/v1/jobs/"):
                job = service.cancel(self.path.rsplit("/", 1)[1])
                if job is None:
                    return self._send(404, {"error": "job not found", "error_code": "not_found"})
                return self._send(200, {"id": job.id, "status": job.status})
            self._send(404, {"error": "not found"})

    return Handler


def main():
    cache = os.environ.get("ALIGNER_CACHE_DIR", "/cache")
    rn = runner_mod.Runner(
        cache_dir=cache,
        music_root=os.environ.get("MUSIC_ROOT", "/music"),
        aligneval=os.environ.get("ALIGNEVAL_PATH") or None,
        threads=int(os.environ.get("ALIGNER_THREADS", "0")),
        keep_workdirs=int(os.environ.get("ALIGNER_KEEP_WORKDIRS", "30")),
        lead=float(os.environ.get("ALIGNER_LEAD", "0.5")),
        max_sec=float(os.environ.get("ALIGNER_MAX_SEC", "1500")),
        timeout=float(os.environ.get("ALIGNER_JOB_TIMEOUT", "5400")),
        asr_variants=os.environ.get("ALIGNER_ASR_VARIANTS", "vocals,prompt,mix,chunks,turbo,fast"),
        demucs_device=os.environ.get("ALIGNER_DEMUCS_DEVICE", ""),
        qwen_device=os.environ.get("ALIGNER_QWEN_DEVICE", "cpu"),
    )
    svc = Service(rn, os.path.join(cache, "jobs"))
    addr = os.environ.get("ALIGNER_LISTEN", "0.0.0.0")
    port = int(os.environ.get("ALIGNER_PORT", "4540"))
    httpd = ThreadingHTTPServer((addr, port), make_handler(svc, os.environ.get("ALIGNER_TOKEN", "")))
    print("poisq-aligner %s слушает %s:%d (музыка: %s, кэш: %s)" % (VERSION, addr, port, rn.music_root, cache), flush=True)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        sys.exit(0)


if __name__ == "__main__":
    main()
