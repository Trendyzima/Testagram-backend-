from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ROOT / "deploy" / "self-hosted" / "docker-compose.yml"


def section(source: str, start: str, end: str) -> str:
    first = source.index(start)
    last = source.index(end, first + len(start))
    return source[first:last]


def main() -> None:
    source = COMPOSE.read_text(encoding="utf-8")
    postgres = section(source, "  postgres:\n", "  control-plane:\n")
    api = section(source, "  api:\n", "  admin-console:\n")
    caddy = section(source, "  caddy:\n", "  node-exporter:\n")
    networks = source.split("\nnetworks:\n", 1)[1]

    assert "video-private: {}" in postgres, (
        "PostgreSQL must be reachable by the private video service without exposing the shared platform network"
    )
    assert "networks: [edge, data, monitoring, platform, egress, video-private]" in api, (
        "Backend API must join video-private for the private video proxy"
    )
    assert "networks: [edge, control-plane, platform]" in caddy, (
        "Caddy must retain Federation/platform connectivity"
    )
    assert "video-private" not in caddy, "Caddy must not reach the private video network"
    assert "video-private:\n    name: ${TESTAGRAM_VIDEO_NETWORK:-testagram-video-private}\n    internal: true" in networks, (
        "video-private must remain an internal-only Docker network"
    )
    print("Private video/PostgreSQL network topology: PASS")


if __name__ == "__main__":
    main()
