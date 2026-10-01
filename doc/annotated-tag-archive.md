# Annotated-tag messages, archived

Seven release tags — v2.1.0 and v2.2.1 through v2.2.7 — were created as
**annotated** tags between 2026-08-01 and 2026-09-06, while the other
forty-three are lightweight. On 2026-10-01 they were converted to
lightweight so the whole set is consistent, which was safe because
nothing before 3.0.0 is consumed by anything outside this repository.

Converting discards the tag *object*, and with it the message the tagger
wrote. The messages are recorded here so nothing is irrecoverable. None
of it is unique — every release has a CHANGELOG entry, and v2.1.0's own
body says as much — but a release summary written at the moment of the
cut is worth keeping where it can be read.

The conversion moved no tag: each lightweight tag points at exactly the
commit its annotated predecessor dereferenced to, which is the
`target-commit` below. `tag-object` is the discarded object's hash, kept
only so an old clone's `^{}` output can be matched up.

To regenerate this file's shape for a future conversion:

    git cat-file tag <tag>


## v2.1.0

- target-commit: `166ff2a1c558528bc1301bf761286b4952fedf5f`
- tag-object (discarded): `42ed6568bcd80dbc567be4e796f48763ef644246`
- tagger: Ionut Nechita <ionut_n2001@yahoo.com> — 2026-08-01 17:42:26 +0300

```text
slinit v2.1.0 — journal pipeline + dinit-parity + self-introspection

See CHANGELOG.md for full details.

Three headline themes:
 - Full journal pipeline (JSONL + binary + FSS + sd_journal API)
 - 100% dinit-parity closure (protocol + directives + env vars)
 - slinit-supports self-introspection CLI + doc/features.md
```

## v2.2.1

- target-commit: `177ece1bfde86b2ba0eeb38d49ec4be52aed42aa`
- tag-object (discarded): `b653a5d75552af50cf087b6bb294f7b7976a9d13`
- tagger: Ionut Nechita <ionut_n2001@yahoo.com> — 2026-08-13 22:59:57 +0300

```text
v2.2.1 — hostnamectl + timedatectl + serial rescue-shell fixes
```

## v2.2.3

- target-commit: `f660499762f83c28bab8f7fea4a35eb5e87ed9c6`
- tag-object (discarded): `8f3f1076696f7ffb24ef09a03055eb1fa2d83884`
- tagger: Ionut Nechita <ionut_n2001@yahoo.com> — 2026-09-01 21:27:07 +0300

```text
v2.2.3 — test-only fix for 116-lock-personality race
```

## v2.2.4

- target-commit: `063efd753041bb8a053346d27b457cffa3606dcf`
- tag-object (discarded): `ddf5616471b14f9da5a03557906a23d3b13550bc`
- tagger: Ionut Nechita <ionut_n2001@yahoo.com> — 2026-09-03 23:11:16 +0300

```text
v2.2.4 — journalctl full parity + nspawn integration + fuzz-caught fixes
```

## v2.2.5

- target-commit: `1d7401378b9c77fb98620f5303485a0aaa2c27bb`
- tag-object (discarded): `57e7731db339672a2e3242d3c75c16b718a13140`
- tagger: Ionut Nechita <ionut_n2001@yahoo.com> — 2026-09-04 10:50:05 +0300

```text
v2.2.5 — journal multi-boot listing + demo --persist + PID-1 ECHILD fixes
```

## v2.2.6

- target-commit: `7e4a8f11c59fe96edd11ec520931c2970fb9d693`
- tag-object (discarded): `fdc281bae13a15cb071f6a69f12eca25c564c59a`
- tagger: Ionut Nechita <ionut_n2001@yahoo.com> — 2026-09-05 20:49:14 +0300

```text
v2.2.6 — journal recovery hardening + 4 QEMU perf harnesses + enable/disable workflow
```

## v2.2.7

- target-commit: `d1573aa83defc82410fd027f5909170752e9b24c`
- tag-object (discarded): `6ef966b772e0dac8e58438bb90ede05b81ab836a`
- tagger: Ionut Nechita <ionut_n2001@yahoo.com> — 2026-09-06 15:37:11 +0300

```text
v2.2.7 — DirLoader concurrent-map fix (PID-1 panic) + pprof endpoint + 88 SSH perf cases
```
