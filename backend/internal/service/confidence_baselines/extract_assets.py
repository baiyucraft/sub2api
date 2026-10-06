"""Reproduce the authorized statistical assets and offline scoring fixtures.

No detector implementation is distributed. Run from this directory with Python
and NumPy; the checked-out reference supplies the independent fixture oracle.
"""
from pathlib import Path
import hashlib
import json
import math
import sys

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
REFERENCE = ROOT / ".upstream" / "gpt56_api_detector"
sys.path.insert(0, str(REFERENCE))
from gpt56_vnext.predictive import score_counts

FILES = {
    "responses": "meow-gpt-other-cap98-efficient--4.5.4-predictive.20261003.1.meow.json",
    "chat_completions": "meow-gpt-chat-compatible--4.5.4-chat.20261003.1.meow.json",
}


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)


for protocol, filename in FILES.items():
    source = json.loads((REFERENCE / "benchmarks" / "official" / filename).read_text(encoding="utf-8"))
    supplied = source["content_sha256"]
    calculated = hashlib.sha256(canonical({k: v for k, v in source.items() if k != "content_sha256"}).encode()).hexdigest()
    assert supplied == calculated
    asset = {"id": source["id"], "version": source["version"], "protocol": protocol,
             "source_content_sha256": supplied, "source_file": filename,
             "source_commit": "dc608d5c097abf96575d5b34f49952ddbd2c281",
             "fitted": source["fitted"], "high": source["tiers"]["high"], "probes": source["probes"]}
    raw = (json.dumps(asset, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False) + "\n").encode()
    (HERE / (protocol + ".json")).write_bytes(raw)
    print(protocol, hashlib.sha256(raw).hexdigest())
    fixtures = []
    for name in source["fitted"]["sources"]:
        counts = {}
        for identity, requested in source["tiers"]["high"]["counts"].items():
            cell = source["fitted"]["cells"][identity]
            prior = 1 / len(cell["categories"])
            measured = [max(0, value - prior) for value in cell["alpha"][name]]
            exact = [requested * value / sum(measured) for value in measured]
            rounded = [math.floor(value) for value in exact]
            for index in sorted(range(len(exact)), key=lambda i: (-exact[i] + rounded[i], i))[:requested - sum(rounded)]:
                rounded[index] += 1
            counts[identity] = {category: number for category, number in zip(cell["categories"], rounded) if number}
        result = score_counts(source["fitted"], counts, source["tiers"]["high"]["counts"],
                              source["tiers"]["high"]["thresholds"], claimed_model="gpt-6.1-sol")
        fixtures.append({"name": name, "counts": counts, "expected": result})
    path = HERE / "fixtures" / (protocol + ".json")
    path.parent.mkdir(exist_ok=True)
    path.write_text(json.dumps(fixtures, ensure_ascii=False, sort_keys=True, indent=2) + "\n", encoding="utf-8")
(HERE / "LICENSE.reference.txt").write_bytes((REFERENCE / "LICENSE").read_bytes())
