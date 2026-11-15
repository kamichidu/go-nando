#!/usr/bin/env python3
"""Install the Debian package in an ephemeral systemd container and exercise its system unit."""

from __future__ import annotations

import argparse
import pathlib
import subprocess
import sys
import tempfile
import time
import uuid


def run(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, check=check, text=True, capture_output=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("package", type=pathlib.Path)
    options = parser.parse_args()
    package = options.package.resolve(strict=True)
    name = f"go-nando-system-smoke-{uuid.uuid4().hex[:10]}"
    image = f"go-nando-system-smoke:{uuid.uuid4().hex[:10]}"
    try:
        with tempfile.TemporaryDirectory(prefix="go-nando-systemd-image-") as context:
            dockerfile = pathlib.Path(context, "Dockerfile")
            dockerfile.write_text(
                "FROM debian:bookworm-slim\n"
                "RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends systemd systemd-sysv ca-certificates && rm -rf /var/lib/apt/lists/*\n"
                "CMD [\"/sbin/init\"]\n",
                encoding="utf-8",
            )
            run("docker", "build", "--tag", image, context)
        run("docker", "run", "--detach", "--privileged", "--cgroupns=host",
            "--name", name, "--volume", f"{package}:/tmp/go-nando.deb:ro",
            image)
        for _ in range(30):
            state = subprocess.run(
                ["docker", "exec", name, "systemctl", "is-system-running"],
                check=False,
                text=True,
                capture_output=True,
            )
            if state.stdout.strip() in {"running", "degraded"}:
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"systemd did not become ready: {state.stderr.strip()}")
        run("docker", "exec", name, "sh", "-euxc", r"""
dpkg -i /tmp/go-nando.deb
install -d -o root -g nando -m 0750 /etc/nando
cat > /etc/nando/smoke.toml <<'EOF'
[repository]
path = "/var/lib/nando/smoke"
[daemon]
listen = "unix:///run/nando-smoke/control.sock"
EOF
install -d -o nando -g nando -m 0700 /var/lib/nando
runuser -u nando -- /usr/bin/nando --config /etc/nando/smoke.toml init
systemctl enable --now nando@smoke.service
systemctl is-active --quiet nando@smoke.service
runuser -u nando -- /usr/bin/nando --config /etc/nando/smoke.toml doctor
systemctl stop nando@smoke.service
""")
        print("Debian package system service started, answered doctor, and stopped successfully.")
        return 0
    except subprocess.CalledProcessError as error:
        sys.stderr.write(error.stdout or "")
        sys.stderr.write(error.stderr or "")
        return error.returncode
    finally:
        subprocess.run(["docker", "rm", "--force", name], check=False, capture_output=True, text=True)
        subprocess.run(["docker", "image", "rm", "--force", image], check=False, capture_output=True, text=True)


if __name__ == "__main__":
    raise SystemExit(main())
