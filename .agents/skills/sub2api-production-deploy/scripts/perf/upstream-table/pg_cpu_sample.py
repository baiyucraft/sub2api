"""Read-only Linux cgroup CPU sampling between the fixture's measurement markers."""
from __future__ import annotations

import json
import pathlib
import sys
import time


def main():
    output, cpu_file = map(pathlib.Path, sys.argv[1:3])
    result = {"scope": "entire PostgreSQL container cgroup", "poll_seconds": .1,
              "status": "measurement_not_started"}

    def cpu_seconds():
        if cpu_file.name == "cpuacct.usage":
            return int(cpu_file.read_text()) / 1e9
        fields = dict(line.split() for line in cpu_file.read_text().splitlines())
        return int(fields["usage_usec"]) / 1e6

    try:
        while not (output / "measurement.start").exists():
            if (output / "process.finished").exists():
                return
            time.sleep(.1)
        before, started = cpu_seconds(), time.monotonic()
        while not (output / "measurement.end").exists():
            if (output / "process.finished").exists():
                result["status"] = "measurement_incomplete"
                return
            time.sleep(.1)
        after, ended = cpu_seconds(), time.monotonic()
        result.update(status="ok", cpu_seconds=after-before,
                      observed_wall_seconds=ended-started,
                      start_marker=(output / "measurement.start").read_text(),
                      end_marker=(output / "measurement.end").read_text())
    except (OSError, ValueError, KeyError) as error:
        # Paths and connection details are deliberately excluded from results.
        result.update(status="unavailable", error_type=type(error).__name__)
    finally:
        (output / "postgres_cpu.json").write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    main()
