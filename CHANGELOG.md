# Changelog

## [1.1.0](https://github.com/Gaisberg/streamnzb/compare/v1.0.3...v1.1.0) (2026-02-16)


### Features

* configurable admin account (may result in broken manifests, so reinstall the addon) ([3b2dbd4](https://github.com/Gaisberg/streamnzb/commit/3b2dbd44593869efc4b09f4eaa2129163eaeac7e))

## [1.0.3](https://github.com/Gaisberg/streamnzb/compare/v1.0.2...v1.0.3) (2026-02-15)


### Bug Fixes

* nzbhydra and prowlarr indexers ([cd34c58](https://github.com/Gaisberg/streamnzb/commit/cd34c586907644b433b59039ebf9890672b66649))

## [1.0.2](https://github.com/Gaisberg/streamnzb/compare/v1.0.1...v1.0.2) (2026-02-15)


### Bug Fixes

* availnzb changes, much faster results for reported releases ([d5fe21f](https://github.com/Gaisberg/streamnzb/commit/d5fe21ffaae89780704b9fa5a9b1ec3c7cf459cd))
* cleanup() deadlock when expiring a session (pkg/session/manager.go) — fixed (likely root cause) ([41a1316](https://github.com/Gaisberg/streamnzb/commit/41a13168ff96b782a009bd5dfe7c902cb4606c33))
* **loader:** add maximum timeout for segment downloads to prevent worker exhaustion ([387bd54](https://github.com/Gaisberg/streamnzb/commit/387bd540f651714db16bc5539232bc9b2e711465))
* **loader:** add timeout wrapper for decode.DecodeToBytes to prevent blocking ([c784b1f](https://github.com/Gaisberg/streamnzb/commit/c784b1fc71f90bdde64fd513bfbea386bf3dd26c))
* **loader:** cancel downloads for cleared segments to release connections promptly ([8d82f82](https://github.com/Gaisberg/streamnzb/commit/8d82f826512999b183b0cde017645bf3aa7a150a))
* **loader:** discard NNTP client on decode timeout to avoid connection reuse panic ([4fc3653](https://github.com/Gaisberg/streamnzb/commit/4fc3653969db3922a246ced12cd33397f31e37e6))
* **loader:** improve condition variable wait with periodic context checks ([38300cb](https://github.com/Gaisberg/streamnzb/commit/38300cbe0e8a3247a2ea1bbc15c41be55bec41b3))
* **loader:** prevent deadlock and memory leak in SmartStream when paused ([aa339de](https://github.com/Gaisberg/streamnzb/commit/aa339de07c8e82da0a8f24480b105c69855efaee))
* more possible hanging fixes ([47e9e8a](https://github.com/Gaisberg/streamnzb/commit/47e9e8a6bcf3cd5189786f1b79c05a92b16ac742))
* **nntp:** add deadline to body reads to prevent indefinite blocking ([69ba448](https://github.com/Gaisberg/streamnzb/commit/69ba4484f07c2cc064dcfa5e78d6cbcb1fee6817))
* persist env vars on ui changes ([6eab92b](https://github.com/Gaisberg/streamnzb/commit/6eab92ba8a177c0e2fb6a28dc92b9dca597bb6d1))
* prevent hangs and resource exhaustion during long runs ([0ab5bfd](https://github.com/Gaisberg/streamnzb/commit/0ab5bfd8cc335f71d866224196df599ec05415d5))
* **session:** prevent cleanup of sessions with active playback ([abf7c61](https://github.com/Gaisberg/streamnzb/commit/abf7c6194c57bbacf8a24ee11e010ceeafb0240c))
* **stremio:** cancel session context when HTTP request is cancelled ([629861e](https://github.com/Gaisberg/streamnzb/commit/629861ed2b72f1673096f79bbe2f612e3b4ea109))
* **stremio:** implement StreamMonitor.Close() to properly close underlying stream ([1ae7722](https://github.com/Gaisberg/streamnzb/commit/1ae77221ab56f404637d208c67afd8ae2cdd9390))
* various stuff ([560aade](https://github.com/Gaisberg/streamnzb/commit/560aadea27358e9a397742e5a9d7705f4cda89aa))

## [1.0.1](https://github.com/Gaisberg/streamnzb/compare/v1.0.0...v1.0.1) (2026-02-13)


### Bug Fixes

* install button after auth changes ([f60510a](https://github.com/Gaisberg/streamnzb/commit/f60510a71da3db58fedb878b98611297b4768e6f))
* serve embedded failure video instead of big buck bunny ([a7ae387](https://github.com/Gaisberg/streamnzb/commit/a7ae3871e6a566323daea602ee87814fe072bac3))
* use tvdb then tmdb as fallback to enhance queries ([dba103f](https://github.com/Gaisberg/streamnzb/commit/dba103f0aae3b7c88f8525ac1f8e7d6bd8f6d517))

## [1.0.0](https://github.com/Gaisberg/streamnzb/compare/v0.7.0...v1.0.0) (2026-02-13)


### ⚠ BREAKING CHANGES

* login auth, device management with seperate filters and sorting

### Features

* add Easynews indexer support (experimental) ([df3c92a](https://github.com/Gaisberg/streamnzb/commit/df3c92a6ed9b60f9e173e952c92c61aba622f9fa))
* add NZBHydra2 indexer discovery ([df3c92a](https://github.com/Gaisberg/streamnzb/commit/df3c92a6ed9b60f9e173e952c92c61aba622f9fa))
* enforce indexer limits and add persistent provider usage tracking ([7bfa5e8](https://github.com/Gaisberg/streamnzb/commit/7bfa5e874399bd6964e5298ce79490c72edebfe1))
* improve visual tagging filtering (3D) ([82a3b44](https://github.com/Gaisberg/streamnzb/commit/82a3b44658809b66e84a017c697d23505afa42f0))
* **indexer:** internal newznab indexers ([aa24293](https://github.com/Gaisberg/streamnzb/commit/aa242936053be2cd7bfaf2552ea1f3c9137eb42d))
* login auth, device management with seperate filters and sorting ([d6666ed](https://github.com/Gaisberg/streamnzb/commit/d6666ed28fba6a8d3a21b6ee159c6b6feb44f243))
* **ui:** seperate indexer tab, tracking, ui improvements for providers ([aa24293](https://github.com/Gaisberg/streamnzb/commit/aa242936053be2cd7bfaf2552ea1f3c9137eb42d))


### Bug Fixes

* **config:** clear legacy indexer fields when migrated indexers are removed ([211dece](https://github.com/Gaisberg/streamnzb/commit/211dece03246f09f1e2f2dfa8d0dd124889b138a))
* disable auto-scroll to logs section on homepage ([df3c92a](https://github.com/Gaisberg/streamnzb/commit/df3c92a6ed9b60f9e173e952c92c61aba622f9fa))
* migrated prowlarr url ([70c6b71](https://github.com/Gaisberg/streamnzb/commit/70c6b71857b67dae0078754d518e4c3ab60002ad))
* pass admin token to created stream url ([0a521af](https://github.com/Gaisberg/streamnzb/commit/0a521aff9ab7723c576a71c6e612e05f4ea13510))
* respect limits for hydra and prowlarr as well ([6361cb0](https://github.com/Gaisberg/streamnzb/commit/6361cb0c01f262004ba9c448cecb19ee4f7a72c2))
* respect TZ env variable ([d6666ed](https://github.com/Gaisberg/streamnzb/commit/d6666ed28fba6a8d3a21b6ee159c6b6feb44f243))
* **session:** pass context around to stop hanging sessions when closing ([aa24293](https://github.com/Gaisberg/streamnzb/commit/aa242936053be2cd7bfaf2552ea1f3c9137eb42d))
* **validation:** add timeouts to prevent instance hangs ([211dece](https://github.com/Gaisberg/streamnzb/commit/211dece03246f09f1e2f2dfa8d0dd124889b138a))

## [0.7.0](https://github.com/Gaisberg/streamnzb/compare/v0.6.2...v0.7.0) (2026-02-09)


### Features

* filtering with ptt attributes ([6319ac4](https://github.com/Gaisberg/streamnzb/commit/6319ac49c2dc6f0355b9683dc896a673fcf9e5c1))
* **triage:** add release deduplication to eliminate duplicate search results ([83bd249](https://github.com/Gaisberg/streamnzb/commit/83bd24951e83db0da22e8d0e45c6d8eff17b6a8b))
* **ui:** reorganize settings page with tabbed interface, add sorting and max streams ([83bd249](https://github.com/Gaisberg/streamnzb/commit/83bd24951e83db0da22e8d0e45c6d8eff17b6a8b))


### Bug Fixes

* max streams ([739321f](https://github.com/Gaisberg/streamnzb/commit/739321f5dc99e832954135f07845f58c70a742bc))
* **nzbhydra:** resolve actual indexer GUID from internal API ([d15e0bb](https://github.com/Gaisberg/streamnzb/commit/d15e0bb604a6079e77278feb8de6fd14a1032a69))
* **stremio:** ensure failed prevalidations don't prevent trying more releases ([83bd249](https://github.com/Gaisberg/streamnzb/commit/83bd24951e83db0da22e8d0e45c6d8eff17b6a8b))
* **stremio:** show 'Size Unknown' for missing indexer file sizes ([da6c87b](https://github.com/Gaisberg/streamnzb/commit/da6c87b82989cf5ff46810439b9864a3db3b2dd6))
* **triage:** reject unknown resolution/codec when filters are configured ([da6c87b](https://github.com/Gaisberg/streamnzb/commit/da6c87b82989cf5ff46810439b9864a3db3b2dd6))

## [0.6.2](https://github.com/Gaisberg/streamnzb/compare/v0.6.1...v0.6.2) (2026-02-07)


### Miscellaneous Chores

* release 0.6.2 ([e20fd8d](https://github.com/Gaisberg/streamnzb/commit/e20fd8d4ee0748b838384f68c97d252922fd0ab8))

## [0.6.1](https://github.com/Gaisberg/streamnzb/compare/v0.6.0...v0.6.1) (2026-02-07)


### Performance Improvements

* various performance improvement and clarifications, prefer most grabbed releases ([52c2d69](https://github.com/Gaisberg/streamnzb/commit/52c2d690ef92f1bf7aa8a0c54ed03522cc118df0))

## [0.6.0](https://github.com/Gaisberg/streamnzb/compare/v0.5.1...v0.6.0) (2026-02-06)


### Features

* **search:** implement tmdb integration and optimize validation ([27453a5](https://github.com/Gaisberg/streamnzb/commit/27453a5ebeaf01b3ea8dc6d17af75c615661a19b))


### Performance Improvements

* optimize 7z streaming ([4cab433](https://github.com/Gaisberg/streamnzb/commit/4cab433d00bf30d5eb80f58dc4e97452ace5962a))

## [0.5.1](https://github.com/Gaisberg/streamnzb/compare/v0.5.0...v0.5.1) (2026-02-06)


### Bug Fixes

* load correct log level after boot from config ([b1fcbab](https://github.com/Gaisberg/streamnzb/commit/b1fcbabb6be3455899cd3080c61755f166458c04))


### Performance Improvements

* **indexer:** optimize availability checks and implement load balancing ([516d688](https://github.com/Gaisberg/streamnzb/commit/516d68869eeab5f7c50826445958a95ba9a47b84))

## [0.5.0](https://github.com/Gaisberg/streamnzb/compare/v0.4.0...v0.5.0) (2026-02-06)


### Features

* console ui component, included more ui configurations, include nntp proxy in metrics ([0b86f67](https://github.com/Gaisberg/streamnzb/commit/0b86f670bd71afa55e3d7ac27aaea3dc68a720a2))

## [0.4.0](https://github.com/Gaisberg/streamnzb/compare/v0.3.0...v0.4.0) (2026-02-05)


### Features

* **ui:** install on stremio button ([f4ea16d](https://github.com/Gaisberg/streamnzb/commit/f4ea16d0ace287c5e06dc7f751d8032f7694e042))


### Bug Fixes

* default config path to /app/data if folder exists ([226bd79](https://github.com/Gaisberg/streamnzb/commit/226bd79b665f6592b65e697107651d85d8336889))

## [0.3.0](https://github.com/Gaisberg/streamnzb/compare/v0.2.0...v0.3.0) (2026-02-05)


### Features

* **frontend:** implement ui ([e8b80d2](https://github.com/Gaisberg/streamnzb/commit/e8b80d2272a9e6d32e2508155a923016649703e5))


### Bug Fixes

* ensure config saves to loaded path and support /app/data ([a62a7b5](https://github.com/Gaisberg/streamnzb/commit/a62a7b52c153d0108c1dd81bd7c4f65133555a75))
* session keep-alive to show active streams correctly ([32b612a](https://github.com/Gaisberg/streamnzb/commit/32b612ab5ff2db75186448b2f0f6f740d58d5156))


### Performance Improvements

* **backend:** actually utilize multiple connections for streaming ([e8b80d2](https://github.com/Gaisberg/streamnzb/commit/e8b80d2272a9e6d32e2508155a923016649703e5))

## [0.2.0](https://github.com/Gaisberg/streamnzb/compare/v0.1.0...v0.2.0) (2026-02-04)


### Features

* **core:** enhance availability checks and archive scanning ([7beb9ca](https://github.com/Gaisberg/streamnzb/commit/7beb9cad52c046651f5f13830f302afd1595e73a))
* prowlarr indexer support ([82a28ef](https://github.com/Gaisberg/streamnzb/commit/82a28eff052c6248fdfdc9f423cc64eed7bb43b6))
* **unpack:** add heuristic support for obfuscated releases ([e0c606d](https://github.com/Gaisberg/streamnzb/commit/e0c606dacb6d51cf8ba86cc865d3ca2e735d576a))
* **unpack:** implement nested archive support with recursive scanning ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))


### Bug Fixes

* **loader:** relax seek bounds to support scanner behavior ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))
* **stremio:** improve error handling, ID parsing, and concurrency ([7beb9ca](https://github.com/Gaisberg/streamnzb/commit/7beb9cad52c046651f5f13830f302afd1595e73a))
* **unpack:** improve file detection and extraction ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))
* **unpack:** smart selection for 7z archives ([7beb9ca](https://github.com/Gaisberg/streamnzb/commit/7beb9cad52c046651f5f13830f302afd1595e73a))


### Performance Improvements

* **loader:** optimize reading with OpenReaderAt ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))
* **loader:** optimize stream cancellation and connection usage ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))

## [0.1.0](https://github.com/Gaisberg/streamnzb/compare/v0.0.2...v0.1.0) (2026-02-04)


### Features

* bootstrapper for startup initialization, give javi11 some recognition in readme ([2cb5cdc](https://github.com/Gaisberg/streamnzb/commit/2cb5cdcd8ea896281f14df0b9f86f08eddaf48e4))

## 0.0.2 (2026-02-04)


### Features

* Initial release ([105d94d](https://github.com/Gaisberg/streamnzb/commit/105d94daba23675d467ea641b23f412199c04102))


### Bug Fixes

* use release-type in release workflow ([c5087c7](https://github.com/Gaisberg/streamnzb/commit/c5087c76b2c9197f6f22b7fb1b5e555f3fc59d1c))


### Miscellaneous Chores

* initial release ([a730030](https://github.com/Gaisberg/streamnzb/commit/a7300307876a0a29bfbfb5067fbf3a538bcc7133))
* Release 0.0.2 ([2281714](https://github.com/Gaisberg/streamnzb/commit/228171467da9c7861d6aa93675b8fb405c245078))

## [0.0.2](https://github.com/Gaisberg/streamnzb/compare/streamnzb-v0.0.1...streamnzb-v0.0.2) (2026-02-04)


### Features

* Initial release ([105d94d](https://github.com/Gaisberg/streamnzb/commit/105d94daba23675d467ea641b23f412199c04102))


### Miscellaneous Chores

* Initial release ([a730030](https://github.com/Gaisberg/streamnzb/commit/a7300307876a0a29bfbfb5067fbf3a538bcc7133))
* Release 0.0.2 ([2281714](https://github.com/Gaisberg/streamnzb/commit/228171467da9c7861d6aa93675b8fb405c245078))

## [0.0.1](https://github.com/Gaisberg/streamnzb/compare/streamnzb-v0.0.1...streamnzb-v0.0.1) (2026-02-04)


### Features

* Initial release ([105d94d](https://github.com/Gaisberg/streamnzb/commit/105d94daba23675d467ea641b23f412199c04102))


### Miscellaneous Chores

* Initial release ([a730030](https://github.com/Gaisberg/streamnzb/commit/a7300307876a0a29bfbfb5067fbf3a538bcc7133))

## 0.0.1 (2026-02-04)


### Features

* Initial release ([105d94d](https://github.com/Gaisberg/streamnzb/commit/105d94daba23675d467ea641b23f412199c04102))


### Miscellaneous Chores

* Initial release ([a730030](https://github.com/Gaisberg/streamnzb/commit/a7300307876a0a29bfbfb5067fbf3a538bcc7133))
