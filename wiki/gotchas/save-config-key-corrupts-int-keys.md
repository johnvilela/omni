---
tags: [gotcha, cli, config]
---

Found while building `omni llm model` (see [[api]], [[sessions/4a8f703c-421a-438d-a77b-a6d786d01a6b]]). `saveConfigKey` in `cli/main.go` does a read-merge-write of `~/.config/omni/config.yaml` — but it unmarshaled the file into `map[string]string` before merging in the new key. `token_budget` is an int (`token_budget: 9000` in config.yaml), so any later `saveConfigKey` call — e.g. saving `<provider>_model` from the new `omni llm model` command — forced the whole file through that string-typed map and corrupted `token_budget` on rewrite (which would then make the server fail to read the config as an int).

This is a second, more subtle latent bug in the same function that was already patched once before: the original `saveToken` clobbered config.yaml with a fresh one-key map on every save, fixed by introducing `saveConfigKey` as a read-merge-write in [[sessions/18f39a98-4d2a-4686-86de-b307dfe4c7d8]] — that fix solved the clobber but kept the map string-typed, leaving this int-corruption trap behind.

Fixed by changing `saveConfigKey`'s map from `map[string]string` to `map[string]any`, with a regression test (`TestSaveConfigKey`) that pre-seeds `token_budget: 9000`, saves an unrelated key, and asserts `token_budget` still round-trips as `9000`.

Rule of thumb: before adding any new non-string config.yaml key (int, bool, nested structure), check that the code writing it doesn't merge through a string-typed map — it will silently corrupt the value on the next unrelated save.