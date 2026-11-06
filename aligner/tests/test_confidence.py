import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
import confidence as c  # noqa: E402


def line(text, **kw):
    d = {"text": " ".join(c.tokens(text)), "found": True, "final": 10.0, "end": 12.0, "asr": 10.0, "qwen": 10.05,
         "ctx": 10.0, "ctxm": 10.1, "pre": 10.02, "filled": False, "stretch_fixed": False, "support": 6, "of": 6, "status": ""}
    d.update(kw)
    return d


class Classify(unittest.TestCase):
    def test_agreeing_estimates_are_high(self):
        self.assertEqual(c.classify(line("a b"))[0], "high")

    def test_gap_to_mms_levels(self):
        self.assertEqual(c.classify(line("a b", ctxm=10.4))[0], "medium")
        self.assertEqual(c.classify(line("a b", ctxm=10.7))[0], "low")
        self.assertEqual(c.classify(line("a b", ctxm=9.3))[0], "low")

    def test_repeats_raise_to_low_only_with_disagreement(self):
        self.assertEqual(c.classify(line("a b"), repeats=7)[0], "high")
        self.assertEqual(c.classify(line("a b", ctxm=10.4), repeats=7)[0], "low")
        self.assertEqual(c.classify(line("a b", ctxm=10.4), repeats=2)[0], "medium")

    def test_weak_estimates_alone_do_not_flag(self):
        # Whisper и одиночный Qwen на треке бывают систематически неточны: сами по себе строку не портят
        self.assertEqual(c.classify(line("a b", asr=11.5, qwen=9.0))[0], "high")

    def test_strong_spread_is_medium(self):
        self.assertEqual(c.classify(line("a b", pre=11.0, ctx=10.0, ctxm=10.1, final=10.05))[0], "medium")

    def test_missing_and_filled_are_low(self):
        self.assertEqual(c.classify({"found": False})[0], "low")
        self.assertEqual(c.classify(line("a b", filled=True))[0], "low")

    def test_arbitration_and_low_support_are_medium(self):
        self.assertEqual(c.classify(line("a b", status="ASR (MMS +0.5)"))[0], "medium")
        self.assertEqual(c.classify(line("a b", support=2, of=6))[0], "medium")

    def test_without_mms_uses_spread_fallback(self):
        self.assertEqual(c.classify(line("a b", ctxm=None, asr=10.3))[0], "high")
        self.assertEqual(c.classify(line("a b", ctxm=None, asr=10.8))[0], "medium")
        self.assertEqual(c.classify(line("a b", ctxm=None, asr=11.5))[0], "low")

    def test_reason_is_human_readable(self):
        level, reason = c.classify(line("a b", ctxm=10.7))
        self.assertIn("независимый", reason)


class Estimate(unittest.TestCase):
    def test_interpolates_between_neighbours_by_text_length(self):
        times = [[1.0, 3.0], [None, None], [None, None], [10.0, 12.0]]
        texts = ["a b", "one two three", "x", "z"]
        c.estimate_missing(times, texts)
        self.assertGreaterEqual(times[1][0], 3.0)
        self.assertLessEqual(times[2][1], 10.0)
        self.assertAlmostEqual(times[1][1], times[2][0], places=6)  # подряд, без дыр
        self.assertGreater(times[1][1] - times[1][0], times[2][1] - times[2][0])  # длиннее текст — длиннее строка

    def test_missing_at_edges(self):
        times = [[None, None], [5.0, 7.0], [None, None]]
        c.estimate_missing(times, ["a", "b", "c"])
        self.assertLess(times[0][1], 5.0)
        self.assertGreater(times[2][0], 7.0)
        self.assertGreaterEqual(times[0][0], 0.0)

    def test_all_missing(self):
        times = [[None, None], [None, None]]
        c.estimate_missing(times, ["a", "b"])
        self.assertLess(times[0][0], times[1][0])


class Build(unittest.TestCase):
    def test_lrc_uses_lead_and_original_indices(self):
        lines = ["[Chorus]", "Hello world", "♪", "Second line"]
        # "[Chorus]" содержит слово, "♪" нет
        kept = c.kept_lines(lines)
        self.assertEqual([i for i, _ in kept], [0, 1, 3])
        dump = [line("Chorus", final=1.0, end=2.0), line("Hello world", final=12.34, end=14.0), line("Second line", final=20.0, end=22.0)]
        res = c.build_result(lines, dump, lead=0.5)
        self.assertEqual(res["lrc"].splitlines()[1], "[00:11.84]Hello world")
        self.assertEqual([x["index"] for x in res["lines"]], [0, 1, 3])
        self.assertEqual(res["summary"]["lines"], 3)

    def test_missing_lines_are_kept_with_estimated_time(self):
        lines = ["one", "two", "three"]
        dump = [line("one", final=5.0, end=6.0), {"text": "two", "found": False}, line("three", final=15.0, end=16.0)]
        res = c.build_result(lines, dump, lead=0.0)
        mid = res["lines"][1]
        self.assertTrue(mid["estimated"])
        self.assertEqual(mid["confidence"], "low")
        self.assertTrue(6.0 <= mid["start"] <= 15.0)
        self.assertEqual(res["summary"]["estimated"], 1)

    def test_order_is_enforced(self):
        lines = ["a", "b"]
        dump = [line("a", final=10.0, end=11.0), line("b", final=9.0, end=9.5)]
        res = c.build_result(lines, dump, lead=0.0)
        self.assertGreaterEqual(res["lines"][1]["start"], res["lines"][0]["start"])
        self.assertEqual(res["lines"][1]["confidence"], "low")

    def test_length_mismatch_raises(self):
        with self.assertRaises(ValueError):
            c.build_result(["a", "b"], [line("a")])

    def test_lrc_time_format(self):
        self.assertEqual(c.fmt_lrc_time(0), "00:00.00")
        self.assertEqual(c.fmt_lrc_time(61.234), "01:01.23")
        self.assertEqual(c.fmt_lrc_time(-3), "00:00.00")


if __name__ == "__main__":
    unittest.main()
