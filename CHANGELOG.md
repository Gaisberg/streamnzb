# Changelog

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
