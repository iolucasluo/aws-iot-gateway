#!/usr/bin/env python3
"""Pure contract tests for the staging Internal App smoke helper."""

import hashlib
import importlib.util
import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parent
MODULE_PATH = ROOT / "smoke_internal_apps.py"
SPEC = importlib.util.spec_from_file_location("smoke_internal_apps", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
SMOKE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SMOKE)


class HMACV1SmokeContractTest(unittest.TestCase):
    def test_expected_product_catalog_requires_both_seeded_pids(self):
        SMOKE.require_expected_products(
            {
                "products": [
                    {"pid": "feeder-x1", "name": "Feeder X1"},
                    {"pid": "minion-cam-x1", "name": "Minion Cam X1"},
                ]
            }
        )

        with self.assertRaisesRegex(SMOKE.SmokeError, "product catalog"):
            SMOKE.require_expected_products(
                {"products": [{"pid": "feeder-x1", "name": "Feeder X1"}]}
            )

    def test_build_signed_headers_matches_livpet_hmac_v1_known_answer(self):
        body = (
            b'{"wait_ack":true,"commands":[{"code":"feed",'
            b'"value":{"portions":1}}]}'
        )
        headers = SMOKE.build_signed_headers(
            api_key="ak_test_hmac_v2",
            api_secret="test-secret-hmac-v2",
            timestamp="00000011718000000000",
            nonce="550e8400-e29b-41d4-a716-446655440000",
            method="POST",
            path="/api/v1/openapi/devices/EXFDR%2f001/commands",
            raw_query="z=last&tag=b&space=hello+world&tag=a&empty=&encoded=%7e",
            body=body,
        )
        canonical = SMOKE.build_canonical_request(
            api_key="ak_test_hmac_v2",
            timestamp="00000011718000000000",
            nonce="550e8400-e29b-41d4-a716-446655440000",
            method="POST",
            path="/api/v1/openapi/devices/EXFDR%2f001/commands",
            raw_query="z=last&tag=b&space=hello+world&tag=a&empty=&encoded=%7e",
            body=body,
        )

        self.assertEqual(SMOKE.SIGNATURE_DOMAIN, "ExploreX-OpenAPI-HMAC-v1")
        self.assertEqual(headers["X-Signature"], "bee0a857d4d8f8f62c28fdfd27e5ca6055f87b784afee2fcd7d06055ca96f02b")
        self.assertTrue(canonical.endswith(hashlib.sha256(body).hexdigest()))
        self.assertNotIn("test-secret-hmac-v2", "\n".join(headers.values()))

    def test_timestamp_is_a_livpet_compatible_20_digit_value(self):
        self.assertEqual(SMOKE.timestamp_from_milliseconds(1718000000000), "00000011718000000000")


if __name__ == "__main__":
    unittest.main()
