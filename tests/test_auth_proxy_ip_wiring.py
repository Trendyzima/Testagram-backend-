#!/usr/bin/env python3
"""Regression checks for Cloudflare-to-Caddy-to-Backend client IP handling."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
CLOUDFLARE_CIDRS = {
    "173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22",
    "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
    "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
    "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
    "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
    "2400:cb00::/32", "2606:4700::/32", "2803:f800::/32",
    "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29",
    "2c0f:f248::/32",
}


class AuthProxyIPWiringTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.caddy = (ROOT / "deploy/self-hosted/Caddyfile").read_text()
        cls.auth = (ROOT / "cmd/api/auth.go").read_text()

    def test_caddy_trusts_only_cloudflare_edge_ranges(self):
        line = next(
            (line.strip() for line in self.caddy.splitlines()
             if line.strip().startswith("trusted_proxies static ")),
            "",
        )
        configured = set(line.removeprefix("trusted_proxies static ").split())
        self.assertEqual(configured, CLOUDFLARE_CIDRS)
        self.assertIn("trusted_proxies_strict", self.caddy)
        self.assertIn("client_ip_headers CF-Connecting-IP X-Forwarded-For", self.caddy)

    def test_caddy_overwrites_backend_real_ip_with_verified_client_ip(self):
        self.assertEqual(self.caddy.count("header_up X-Real-IP {client_ip}"), 2)
        self.assertNotIn("header_up X-Real-IP {remote_host}", self.caddy)

    def test_backend_accepts_real_ip_only_from_private_proxy_hops(self):
        self.assertIn("remoteIP.IsPrivate() || remoteIP.IsLoopback()", self.auth)
        self.assertIn('r.Header.Get("X-Real-IP")', self.auth)
        self.assertIn("net.ParseIP(strings.TrimSpace", self.auth)


if __name__ == "__main__":
    unittest.main()
