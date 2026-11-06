import json
import os
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
import runner as runner_mod  # noqa: E402
import server as server_mod  # noqa: E402

FAKE = r'''
import json, os, sys, time
args = sys.argv[1:]
work = args[args.index("--work") + 1]
text = args[args.index("--text") + 1]
mode = os.environ.get("FAKE_MODE", "ok")
print("[   0.0s] микс подготовлен", flush=True)
print("+ ffmpeg -i x", flush=True)
if mode == "quota":
    print("Groq HTTP 429: rate limit", flush=True); sys.exit(1)
if mode == "sleep":
    print("[asr-anchor] долгий этап", flush=True); time.sleep(60)
if mode == "sigill":
    os.kill(os.getpid(), 4)
lines = [l.strip() for l in open(text, encoding="utf-8") if l.strip()]
dump = []
for i, l in enumerate(lines):
    t = 10.0 + i * 4
    dump.append({"line": i, "text": " ".join(__import__("re").findall(r"\w+", l.lower())), "found": True, "status": "ASR",
                 "filled": False, "stretch_fixed": False, "asr": t, "qwen": t, "ctx": t, "ctxm": t, "pre": t,
                 "final": t, "end": t + 3, "support": 6, "of": 6})
json.dump(dump, open(os.path.join(work, "lines.json"), "w", encoding="utf-8"))
print("[asr-anchor] найдено в записи строк: %d из %d" % (len(lines), len(lines)), flush=True)
'''


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.music = os.path.join(self.tmp.name, "music")
        os.makedirs(os.path.join(self.music, "A"))
        self.audio = os.path.join(self.music, "A", "song.flac")
        with open(self.audio, "wb") as f:
            f.write(b"fLaC")
        self.fake = os.path.join(self.tmp.name, "fake_aligneval.py")
        with open(self.fake, "w", encoding="utf-8") as f:
            f.write(FAKE)
        self.cache = os.path.join(self.tmp.name, "cache")
        self.rn = runner_mod.Runner(self.cache, self.music, aligneval=self.fake, lead=0.5, timeout=30)
        self.svc = server_mod.Service(self.rn, os.path.join(self.cache, "jobs"))

    def tearDown(self):
        os.environ.pop("FAKE_MODE", None)
        self.tmp.cleanup()

    def wait(self, job, timeout=20):
        end = time.time() + timeout
        while time.time() < end and job.status in ("queued", "running"):
            time.sleep(0.05)
        return job

    def req(self, **kw):
        d = {"audio_path": self.audio, "lines": ["First line here", "Second line here", "Third line here"],
             "lang": "en", "track_id": "t1", "groq_key": "SECRET-KEY"}
        d.update(kw)
        return d


class RunnerTests(Base):
    def test_audio_must_be_inside_music_root(self):
        with self.assertRaises(runner_mod.AlignError) as c:
            self.rn.validate_audio("/etc/passwd")
        self.assertEqual(c.exception.code, "bad_request")
        with self.assertRaises(runner_mod.AlignError):
            self.rn.validate_audio(os.path.join(self.music, "..", "cache", "x.flac"))
        with self.assertRaises(runner_mod.AlignError):
            self.rn.validate_audio(os.path.join(self.music, "A", "missing.flac"))

    def test_symlink_escape_is_rejected(self):
        outside = os.path.join(self.tmp.name, "secret.flac")
        with open(outside, "wb") as f:
            f.write(b"x")
        link = os.path.join(self.music, "A", "link.flac")
        os.symlink(outside, link)
        with self.assertRaises(runner_mod.AlignError):
            self.rn.validate_audio(link)

    def test_classify_failure(self):
        self.assertEqual(runner_mod.classify_failure(1, "Groq HTTP 429: x"), "quota")
        self.assertEqual(runner_mod.classify_failure(1, "Groq HTTP 401: x"), "invalid_key")
        self.assertEqual(runner_mod.classify_failure(-4, ""), "cpu_unsupported")
        self.assertEqual(runner_mod.classify_failure(1, "boom"), "failed")

    def test_progress_filter(self):
        self.assertTrue(runner_mod.is_progress_line("[asr-anchor] найдено"))
        self.assertFalse(runner_mod.is_progress_line("+ ffmpeg -i x"))
        self.assertFalse(runner_mod.is_progress_line("   строка 1 ..."))


class ServiceTests(Base):
    def test_full_job_produces_lrc_and_drops_key(self):
        job = self.wait(self.svc.submit(self.req()))
        self.assertEqual(job.status, "done", job.error)
        res = job.result
        self.assertEqual(res["summary"]["lines"], 3)
        self.assertEqual(res["lrc"].splitlines()[0], "[00:09.50]First line here")  # lead 0.5
        self.assertTrue(any("найдено в записи" in p["msg"] for p in job.progress))
        self.assertFalse(any("ffmpeg" in p["msg"] for p in job.progress))
        self.assertNotIn("groq_key", job.req)
        with open(os.path.join(self.cache, "jobs", job.id + ".json"), encoding="utf-8") as f:
            persisted = json.load(f)
        self.assertNotIn("SECRET-KEY", json.dumps(persisted))

    def test_quota_error_code(self):
        os.environ["FAKE_MODE"] = "quota"
        job = self.wait(self.svc.submit(self.req()))
        self.assertEqual(job.status, "error")
        self.assertEqual(job.error_code, "quota")

    def test_sigill_error_code(self):
        os.environ["FAKE_MODE"] = "sigill"
        job = self.wait(self.svc.submit(self.req()))
        self.assertEqual(job.error_code, "cpu_unsupported")

    def test_missing_key_and_too_few_lines(self):
        job = self.wait(self.svc.submit(self.req(groq_key="")))
        self.assertEqual(job.error_code, "invalid_key")
        job = self.wait(self.svc.submit(self.req(track_id="t2", lines=["a", "b"])))
        self.assertEqual(job.error_code, "bad_request")

    def test_submit_validates_input(self):
        with self.assertRaises(runner_mod.AlignError):
            self.svc.submit(self.req(lines=[]))
        with self.assertRaises(runner_mod.AlignError):
            self.svc.submit(self.req(audio_path="/etc/passwd"))

    def test_cancel_running_job(self):
        os.environ["FAKE_MODE"] = "sleep"
        job = self.svc.submit(self.req())
        end = time.time() + 10
        while time.time() < end and job.status != "running":
            time.sleep(0.05)
        time.sleep(0.5)
        self.svc.cancel(job.id)
        self.wait(job, 15)
        self.assertEqual(job.status, "cancelled")

    def test_same_track_is_idempotent_while_active(self):
        os.environ["FAKE_MODE"] = "sleep"
        a = self.svc.submit(self.req())
        b = self.svc.submit(self.req())
        self.assertEqual(a.id, b.id)
        self.svc.cancel(a.id)
        self.wait(a, 15)

    def test_workdir_cleanup_keeps_only_reusable_files(self):
        job = self.wait(self.svc.submit(self.req()))
        self.assertEqual(job.status, "done")
        wd = self.rn.workdir_for(self.rn.validate_audio(self.audio))
        names = set(os.listdir(wd))
        self.assertIn("lines.json", names)
        self.assertFalse(any(n.startswith("anchor_") for n in names))


class HttpTests(Base):
    def setUp(self):
        super().setUp()
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), server_mod.make_handler(self.svc, "tok"))
        self.port = self.httpd.server_address[1]
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()

    def tearDown(self):
        self.httpd.shutdown()
        super().tearDown()

    def call(self, method, path, body=None, token="tok"):
        req = urllib.request.Request("http://127.0.0.1:%d%s" % (self.port, path), method=method,
                                     data=json.dumps(body).encode() if body is not None else None)
        if token:
            req.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                return r.status, json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read().decode())

    def test_health_needs_no_token(self):
        self.assertEqual(self.call("GET", "/healthz", token="")[0], 200)

    def test_token_required_for_jobs(self):
        self.assertEqual(self.call("POST", "/v1/jobs", self.req(), token="wrong")[0], 401)
        self.assertEqual(self.call("GET", "/v1/jobs/abc", token="")[0], 401)

    def test_http_flow(self):
        code, body = self.call("POST", "/v1/jobs", self.req())
        self.assertEqual(code, 202)
        job = self.svc.get(body["id"])
        self.wait(job)
        code, got = self.call("GET", "/v1/jobs/" + body["id"])
        self.assertEqual(code, 200)
        self.assertEqual(got["status"], "done")
        self.assertEqual(got["result"]["summary"]["lines"], 3)

    def test_bad_requests(self):
        self.assertEqual(self.call("POST", "/v1/jobs", self.req(lines="not a list"))[0], 400)
        self.assertEqual(self.call("GET", "/v1/jobs/unknown")[0], 404)
        self.assertEqual(self.call("DELETE", "/v1/jobs/unknown")[0], 404)

    def test_finished_jobs_survive_restart(self):
        job = self.wait(self.svc.submit(self.req()))
        svc2 = server_mod.Service(self.rn, os.path.join(self.cache, "jobs"))
        again = svc2.get(job.id)
        self.assertIsNotNone(again)
        self.assertEqual(again.status, "done")


if __name__ == "__main__":
    unittest.main()
