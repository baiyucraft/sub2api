# GPT‑6.1 Sol statistical reference assets

Source: [chen-006/gpt56_api_detector](https://github.com/chen-006/gpt56_api_detector), commit `dc608d5c097abf96575d5b34f49952ddbd2c281`.

The owner confirmed an existing commercial authorization for using this baseline in this implementation. This confirmation does not relicense the upstream data or grant downstream commercial rights. Keep the upstream required notice and [license](LICENSE.reference.txt) with the distributed statistical assets.

Required Notice: Copyright 2026 chen-006 and contributors. Original project: https://github.com/chen-006/gpt56_api_detector

`responses.json` and `chat_completions.json` contain only the fitted Dirichlet parameters, original question contract, and high-tier quotas/thresholds from the 2026-10-03 native and Chat packages. Their source package content hashes, versions, filenames and commit are included in each asset. The runtime verifies the SHA-256 of each extracted file before scoring. The Go predictive implementation is independent and does not distribute the upstream Python scorer.

`extract_assets.py` regenerates these files from the local upstream checkout. It verifies the upstream canonical content checksum first, then uses the upstream scorer solely as an offline oracle for the fixtures under `fixtures/`. Reproduction requires Python and NumPy; runtime requires neither.

Chat uses the separately versioned compatibility package. The upstream Chat package reuses native sampling/calibration; protocol separation does not imply independent Chat sampling. Scores describe relative behavioral fit, not model-identity probabilities. Development simulation results are not production accuracy guarantees.
