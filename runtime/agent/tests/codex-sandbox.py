#!/usr/bin/env python3
"""Exercise the installed Codex sandbox inside an agent container, without a model.

Use a writable HOME outside /tmp: Codex refuses to create its helpers in /tmp.
All test data and configuration are isolated from the real shared Codex home.
"""

import os
from pathlib import Path
import socket
import subprocess
import tempfile


def main():
    with tempfile.TemporaryDirectory(prefix=".sandbox-test-", dir=Path.home()) as tmp:
        root = Path(tmp)
        workspace = root / "workspace"
        workspace.mkdir()
        for name in (".git", ".codex", ".agents"):
            (workspace / name).mkdir()
        codex_home = root / "codex-home"
        codex_home.mkdir()
        env = dict(os.environ, CODEX_HOME=str(codex_home))

        def sandbox(code, *args, profile=":workspace"):
            result = subprocess.run(
                ["codex", "sandbox", "--permission-profile", profile,
                 "--cd", str(workspace), "--", "python3", "-c", code, *args],
                env=env, capture_output=True, text=True, timeout=30,
            )
            if result.returncode:
                raise AssertionError(result.stdout + result.stderr)
            return result.stdout.strip()

        sandbox("from pathlib import Path; p=Path('allowed'); p.write_text('ok'); "
                "assert p.read_text()=='ok'; p.write_text('updated'); "
                "assert p.read_text()=='updated'; p.unlink()")
        print("PASS workspace create/read/update/delete")

        deny_write = """
import errno, pathlib, sys
try:
    pathlib.Path(sys.argv[1]).write_text('must not be written')
except OSError as e:
    assert e.errno in (errno.EACCES, errno.EPERM, errno.EROFS), e
else:
    raise AssertionError('sandbox allowed a forbidden write')
"""
        # The sibling is writable to the outer container, but outside the workspace.
        outside = root / "outside"
        outside.write_text("original")
        sandbox(deny_write, str(outside))
        assert outside.read_text() == "original"
        for name in (".git", ".codex", ".agents"):
            target = workspace / name / "forbidden"
            sandbox(deny_write, str(target))
            assert not target.exists()
        sandbox(deny_write, str(workspace / "readonly"), profile=":read-only")
        print("PASS sibling/protected-path/read-only writes denied")

        # An outer-container listener is reachable before sandboxing. A failed
        # sandbox connection therefore demonstrates Codex isolation, not egress.
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            port = listener.getsockname()[1]
            with socket.create_connection(("127.0.0.1", port), timeout=2):
                pass
            sandbox("""
import socket, sys
try:
    socket.create_connection(('127.0.0.1', int(sys.argv[1])), timeout=2)
except OSError:
    pass
else:
    raise AssertionError('sandbox reached outer-container network')
""", str(port))
        print("PASS network isolation")


if __name__ == "__main__":
    main()
