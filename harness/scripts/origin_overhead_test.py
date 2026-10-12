#!/usr/bin/env python3

import importlib.util
from pathlib import Path
import unittest


SCRIPT_DIR = Path(__file__).resolve().parent


class OriginOverheadTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = SCRIPT_DIR / "origin_overhead.py"
        spec = importlib.util.spec_from_file_location("origin_overhead", path)
        if spec is None or spec.loader is None:
            raise RuntimeError(f"cannot load {path}")
        cls.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.module)

    def test_five_alternating_pairs_enforce_two_percent_median_gate(self):
        document = {
            "schema_version": 1,
            "cpu_affinity": "2-5",
            "pairs": [
                {
                    "pair": index + 1,
                    "first": "disabled" if index % 2 == 0 else "enabled",
                    "disabled_rps": 100_000.0,
                    "enabled_rps": enabled,
                }
                for index, enabled in enumerate((99_000, 99_500, 98_500, 99_250, 99_100))
            ],
        }

        result = self.module.analyze_pairs(document)

        self.assertTrue(result["eligible"])
        self.assertGreaterEqual(result["median_relative_delta"], -0.02)
        self.assertEqual(len(result["paired_relative_deltas"]), 5)

        for pair in document["pairs"]:
            pair["enabled_rps"] = 97_000.0
        failed = self.module.analyze_pairs(document)
        self.assertFalse(failed["eligible"])
        self.assertIn(
            "instrumented_median_more_than_2_percent_below_control",
            failed["ineligibility_reasons"],
        )


if __name__ == "__main__":
    unittest.main()
