# Changelog

## [4.0.0](https://github.com/Gaisberg/streamnzb/compare/v3.7.0...v4.0.0) (2026-04-02)


### ⚠ BREAKING CHANGES

* **streams:** overhaul stream model and settings ui

### Features

* cleanup ui a bit ([5cd8bf4](https://github.com/Gaisberg/streamnzb/commit/5cd8bf49490874fbe87f0baa58f5915f3ce95c43))
* easynews indexer ([416accf](https://github.com/Gaisberg/streamnzb/commit/416accf1a86359ebd21351ea9451feffa65b0199))
* **streams:** improve search handling, caching, and settings UX ([#107](https://github.com/Gaisberg/streamnzb/issues/107)) ([12d6eb9](https://github.com/Gaisberg/streamnzb/commit/12d6eb961903359f367c4caab90ccee5d0a66894))
* **streams:** overhaul stream model and settings ui ([b760493](https://github.com/Gaisberg/streamnzb/commit/b7604930b90a1c807541f2f75148f5b794acc1d6))
* toggle nntp proxy on and off (Fixes [#101](https://github.com/Gaisberg/streamnzb/issues/101)) ([b62657b](https://github.com/Gaisberg/streamnzb/commit/b62657b78870ebaabf72dc7f1eeebac5f13e69a1))


### Bug Fixes

* accept 201 greetings response on NNTP connection (Fixes [#104](https://github.com/Gaisberg/streamnzb/issues/104)) ([5cd8bf4](https://github.com/Gaisberg/streamnzb/commit/5cd8bf49490874fbe87f0baa58f5915f3ce95c43))

## [Unreleased]

### ⚠ BREAKING CHANGES

* legacy device entries are no longer migrated into the new stream model

### Notes

* global configuration, providers, and indexers are kept during upgrade
* legacy `devices` / `users` state is reset intentionally
* streams must be recreated in the UI after upgrading from older device-based versions

## [3.7.0](https://github.com/Gaisberg/streamnzb/compare/v3.6.0...v3.7.0) (2026-03-22)


### Features

* **availnzb:** auto-recover API key when IP already has one registered ([acad7f9](https://github.com/Gaisberg/streamnzb/commit/acad7f931e3387cf5d8708d365dbe6d4287e3eab))
* remove redundant indexer settings ([a7827b1](https://github.com/Gaisberg/streamnzb/commit/a7827b172c7e336132751787f7c4ddd0239ca435))
* **search:** add per-indexer toggle for ID and string search methods ([d320d87](https://github.com/Gaisberg/streamnzb/commit/d320d87e2b0883a176fa5808b935103d06a60fcb))


### Bug Fixes

* **media:** remove m2ts and ts from supported video extensions ([1c0969b](https://github.com/Gaisberg/streamnzb/commit/1c0969ba76b71c06d24d42432278d90e6f85498a))
* **nzb:** null bytes in nzb broke parser ([71cb895](https://github.com/Gaisberg/streamnzb/commit/71cb895041cc7ae8ea825bcf7e42d4eca5c7769a))
* **unpack:** single episode releases with obfuscated name were skipped ([71cb895](https://github.com/Gaisberg/streamnzb/commit/71cb895041cc7ae8ea825bcf7e42d4eca5c7769a))

## [3.6.0](https://github.com/Gaisberg/streamnzb/compare/v3.5.5...v3.6.0) (2026-03-19)


### Features

* use AppData/Local/streamnzb as data dir on Windows ([043509b](https://github.com/Gaisberg/streamnzb/commit/043509b9417ea36ae5fd589859d774f27cd64f79))

## [3.5.5](https://github.com/Gaisberg/streamnzb/compare/v3.5.4...v3.5.5) (2026-03-19)


### Bug Fixes

* matching filter availnzb results (bad data) ([718ffcf](https://github.com/Gaisberg/streamnzb/commit/718ffcf54de0f8359a1f894b87c27130a4d1150e))
* remove hardcoded 5 count limit ([cbc7d9c](https://github.com/Gaisberg/streamnzb/commit/cbc7d9cf1b311e9ef7056cd735b3807cb1af08cf))


### Performance Improvements

* improve playback performance, dramatically simplify prefetching ([00eff86](https://github.com/Gaisberg/streamnzb/commit/00eff8690be71e28e175779231421136a7303e4f))

## [3.5.4](https://github.com/Gaisberg/streamnzb/compare/v3.5.3...v3.5.4) (2026-03-11)


### Bug Fixes

* fail closed when pack playback cannot match requested episode ([01451ce](https://github.com/Gaisberg/streamnzb/commit/01451ce626bb1856c70fd503870c62ff08ed1c0e))
* logging for production ([a41c14d](https://github.com/Gaisberg/streamnzb/commit/a41c14de93496be6bb4f807a6dc28771ca74efaa))
* **search:** matched hyphens better ( gotta catch em all ) ([b15e677](https://github.com/Gaisberg/streamnzb/commit/b15e6778dd80124257615988f4f6e8ca422f82d6))
* serialize db writes through on shared lock ([d35e0f9](https://github.com/Gaisberg/streamnzb/commit/d35e0f94f9da704ffdcd3ee67f187c7c8c283582))

## [3.5.3](https://github.com/Gaisberg/streamnzb/compare/v3.5.2...v3.5.3) (2026-03-10)


### Bug Fixes

* remove save button from devices that would overwrite config ([f83e96f](https://github.com/Gaisberg/streamnzb/commit/f83e96f5b5d17e9f3c95332ae7a52262e387ad6a))

## [3.5.2](https://github.com/Gaisberg/streamnzb/compare/v3.5.1...v3.5.2) (2026-03-10)


### Bug Fixes

* **availnzb:** make API key registration fail-open during startup ([c6dedbc](https://github.com/Gaisberg/streamnzb/commit/c6dedbcd98980d6af869d898ef8c047195348cc9))


### Performance Improvements

* **unpack:** avoid per-volume segment detection for split 7z parts ([c0612b9](https://github.com/Gaisberg/streamnzb/commit/c0612b905dcbcf293119abfeb4f04e8a9e40cfcf))
* **unpack:** avoid per-volume segment detection when aggregating RAR continuations ([578207b](https://github.com/Gaisberg/streamnzb/commit/578207bb2d003a2c5100f9863400c0387fc94757))

## [3.5.1](https://github.com/Gaisberg/streamnzb/compare/v3.5.0...v3.5.1) (2026-03-09)


### Bug Fixes

* availnzb update ([4c35fcf](https://github.com/Gaisberg/streamnzb/commit/4c35fcf5dafe4df17c76a040017f984ce3923fdf))
* **loader:** use actual last segment size for decoded mapping ([e9bc649](https://github.com/Gaisberg/streamnzb/commit/e9bc6494c848130796d7027220ff91e8bae56646))
* **newznab:** authenticate deferred NZB downloads from keyless grab urls ([0bf62d0](https://github.com/Gaisberg/streamnzb/commit/0bf62d01f777434a542f5957d135f611daba6fea))
* reset daily usage correctly ([1a2bf32](https://github.com/Gaisberg/streamnzb/commit/1a2bf323dfd1a196d93005815f96f78582753607))

## [3.5.0](https://github.com/Gaisberg/streamnzb/compare/v3.4.0...v3.5.0) (2026-03-08)


### Features

* **indexers:** add configurable per-indexer request timeouts ([d733fd9](https://github.com/Gaisberg/streamnzb/commit/d733fd9c5aac637439734ee826a001889f541ec9))
* **troubleshooting:** add log download and bad match report actions ([500f32d](https://github.com/Gaisberg/streamnzb/commit/500f32de5252c9f7fe213c75e4b0241d7f54010a))


### Bug Fixes

* **config:** validate unresolved prowlarr indexer placeholder ([8c3cc1f](https://github.com/Gaisberg/streamnzb/commit/8c3cc1f2076311cbe24690ce34565c1dbb91bfd5))

## [3.4.0](https://github.com/Gaisberg/streamnzb/compare/v3.3.0...v3.4.0) (2026-03-07)


### Features

* episode pack support ([89716d0](https://github.com/Gaisberg/streamnzb/commit/89716d0946dd6afdae2b7166d0d6c35a8a43eecf))
* redact sensitive information from logger ([6b2eda1](https://github.com/Gaisberg/streamnzb/commit/6b2eda179af2666bf2604a8bea987ad7cc086d1f))


### Bug Fixes

* improve search matching even more ([11cd359](https://github.com/Gaisberg/streamnzb/commit/11cd3594c35cbe2d59758124ff3020da7140507c))
* improve search performance some more ([b834342](https://github.com/Gaisberg/streamnzb/commit/b8343420377d46b6a85b56f5e05824a22dff4acd))
* **loader:** prevent seek from canceling active segment reads ([5ab93c2](https://github.com/Gaisberg/streamnzb/commit/5ab93c25048786c61ff91a2d5f38778bc9de4562))


### Performance Improvements

* improve playback and seek performance ([2531fad](https://github.com/Gaisberg/streamnzb/commit/2531fadcd1c53e1758148092da82d60dc1f345eb))
* improve series matching ([e8964e1](https://github.com/Gaisberg/streamnzb/commit/e8964e13034bfa3bc1292413701e94b4a7f1dd7b))

## [3.3.0](https://github.com/Gaisberg/streamnzb/compare/v3.2.0...v3.3.0) (2026-03-05)


### Features

* **availnzb:** add configuration option ([72f0bd6](https://github.com/Gaisberg/streamnzb/commit/72f0bd61f2e7963155d4d22aaf50a4071aad0ca0))
* configurable per device streams ([b852f24](https://github.com/Gaisberg/streamnzb/commit/b852f240ed1d1c52b4797e417822f57f2f0b1dc7))
* nzb history, sqlite persistence, failover race condition fix ([0a48c42](https://github.com/Gaisberg/streamnzb/commit/0a48c424731e8704376fc00dc0d913f3401cb510))


### Bug Fixes

* **search:** tighten fuzzy title match so companion titles don't match main title ([0f54b41](https://github.com/Gaisberg/streamnzb/commit/0f54b41f1532daa71b8ba8a90693c754f8bf3326))


### Performance Improvements

* cap stremio prefetchs ([b2ffd43](https://github.com/Gaisberg/streamnzb/commit/b2ffd437440f5a49e875cae2ada198461ac29010))
* improve failover performance ([0f54b41](https://github.com/Gaisberg/streamnzb/commit/0f54b41f1532daa71b8ba8a90693c754f8bf3326))

## [3.2.0](https://github.com/Gaisberg/streamnzb/compare/v3.1.0...v3.2.0) (2026-03-04)


### Features

* **logging:** rotate to streamnzb.log and add keep-log-files setting ([e2f183b](https://github.com/Gaisberg/streamnzb/commit/e2f183b20da9944c7d91bc4ae92bc46335126f6b))


### Bug Fixes

* fix memory leak, configurable memory limit (512mb default) ([eba23d8](https://github.com/Gaisberg/streamnzb/commit/eba23d8fcd9ee80e4654860273f45897ec86bea8))
* improve search matching using fuzzy matching ([304d78f](https://github.com/Gaisberg/streamnzb/commit/304d78f3508f633f187eba03a0c8d08112845af8))
* **memory:** cap segment cache and expire slotFailedDuringPlayback ([3a44fe0](https://github.com/Gaisberg/streamnzb/commit/3a44fe06123bc8f022c81dc47404d68be730a546))
* **memory:** expire failover order (24h TTL), cap segment size estimator ([65cacbc](https://github.com/Gaisberg/streamnzb/commit/65cacbcf476fd29e040d2fe19ee07d5d0462c92a))
* **session:** evict sessions after max playback duration (Phase 2) ([5a79017](https://github.com/Gaisberg/streamnzb/commit/5a79017999b90917faf799208c09abd6c2c5d4c0))

## [3.1.0](https://github.com/Gaisberg/streamnzb/compare/v3.0.0...v3.1.0) (2026-03-03)


### Features

* nzbhydra and prowlarr are back baby ([5ac0d41](https://github.com/Gaisberg/streamnzb/commit/5ac0d41e46ba363f39e2a5dbf5f663a6b09a1eff))
* **stremio:** STAT first segment before play for faster 430 failover ([5d6f455](https://github.com/Gaisberg/streamnzb/commit/5d6f45594e0f9ab36ad678d674f1681681af90ec))


### Bug Fixes

* **7z:** properly sort 7z volumes passing filenames through ExtractFilename ([18ec2c1](https://github.com/Gaisberg/streamnzb/commit/18ec2c158c9dde02d6d6607ff592565c52c80bb9))
* **aiostreams:** handle failoverorder request from aiostreams ([d5ae1f9](https://github.com/Gaisberg/streamnzb/commit/d5ae1f9e8359530dfa4cb96b23a0e236fbc69128))
* **aiostreams:** user failoverId now correctly maps aiostreams results to streamnzb results ([71535d4](https://github.com/Gaisberg/streamnzb/commit/71535d4093e5835ad568a16785a3ec527b586348))
* remove max size from aiostreams config for now ([ddc5462](https://github.com/Gaisberg/streamnzb/commit/ddc54625a22e9b52641604a9ab82d7ddbbc58b4e))
* **stremio:** include device token in redirect path after fallback ([eb24de9](https://github.com/Gaisberg/streamnzb/commit/eb24de99c04cb51cf4fa36d47e163440a96a8973))
* **stremio:** only add in-range slots to session fallback list ([697c2a2](https://github.com/Gaisberg/streamnzb/commit/697c2a2c7719c5102e81df9d2d7d91d72efc6b83))
* **stremio:** use accurate mime types for fallback streams instead of forced mp4 ([1b2c108](https://github.com/Gaisberg/streamnzb/commit/1b2c108b2fdcfb4085dbd966d5156fc348b4c00c))


### Performance Improvements

* improve failover performance ([7ba6137](https://github.com/Gaisberg/streamnzb/commit/7ba613759c7cf4006d9d4359d3c2b595bea4a1a1))
* **streaming:** parallel first-segment probe for segment 0 ([5808ec4](https://github.com/Gaisberg/streamnzb/commit/5808ec4a6931c4ae459131a664c66436fbe1b5e9))
* **stremio:** prefetch next fallback NZB in background during play ([2739d0e](https://github.com/Gaisberg/streamnzb/commit/2739d0ec05a5cc8c17b5b0dc997c79f6a8b0e012))
* **unpack:** avoid pre-calling EnsureSegmentMap for all volumes in full RAR scan ([c86f231](https://github.com/Gaisberg/streamnzb/commit/c86f23120d05ab4fa5b275fe09d17dd2799110ad))

## [3.0.0](https://github.com/Gaisberg/streamnzb/compare/v2.2.0...v3.0.0) (2026-02-27)


### ⚠ BREAKING CHANGES

* Introducing streams, automatic failover dont sweat when choosing over a release

### Features

* aiostreams support ([2417210](https://github.com/Gaisberg/streamnzb/commit/241721039d0e3cefdfae8032d0e20c5eece497dc))
* Introducing streams, automatic failover dont sweat when choosing over a release ([b92b4dc](https://github.com/Gaisberg/streamnzb/commit/b92b4dcd54f6b638ae4f05da6bafd1ceefe2b477))
* **seek:** experimental seek after failover ([4ba6297](https://github.com/Gaisberg/streamnzb/commit/4ba62979956dc8785b8986f0138c0cd217947fc2))
* show all streams mode for streams ([ebeadf6](https://github.com/Gaisberg/streamnzb/commit/ebeadf63986775b5120e99b7fe223be0095e8dc1))
* **ui:** profile page ([6111940](https://github.com/Gaisberg/streamnzb/commit/6111940a79267b3cde10c8a9ef0531cbdbe9c736))
* **ui:** search page, availnzb changes ([af7733b](https://github.com/Gaisberg/streamnzb/commit/af7733be01cbd8679ccbb7e1fe223d14bff658ac))


### Bug Fixes

* **availnzb:** send NNTP hostnames to AvailNZB, not provider display names ([5c02b85](https://github.com/Gaisberg/streamnzb/commit/5c02b85e438620e413c3ed609d64d8ff8c7f3977))
* **frontend:** normalize checkboxes persist when unchecked ([18622ec](https://github.com/Gaisberg/streamnzb/commit/18622ec0d4e13fb313a55dfd6c1b7ca58bbb07a8))
* **media:** wait for prefetch drain in Seek before returning ([b3e0595](https://github.com/Gaisberg/streamnzb/commit/b3e0595bdfab11cf1b5bc1cccf89fd75b69d4e3a))
* priority overrides availnzb ([8adc873](https://github.com/Gaisberg/streamnzb/commit/8adc8737b9b0d56f052a07edc777af6a5d48abba))
* **search:** filter ID search results by content title/year ([2781f84](https://github.com/Gaisberg/streamnzb/commit/2781f8476177f18e2230cc572ebcb8ee8091e7f1))
* **search:** match titles by letters/digits only in FilterResults ([ec2eb12](https://github.com/Gaisberg/streamnzb/commit/ec2eb1234149a563f8407643d9612ef7aaa21058))
* **stremio:** hide stream configs with no candidates after filtering ([3f5cfd1](https://github.com/Gaisberg/streamnzb/commit/3f5cfd1bce2d0b3a01318344643e70585ccab26a))
* **stremio:** set QuerySource=id for AvailNZB releases so triage doesn't push them to bottom ([4586c02](https://github.com/Gaisberg/streamnzb/commit/4586c02d3a4fda061ccee367d0a5b3bf146a8e32))
* string search as t=search instead of tvsearch as per api specs ([a8613d0](https://github.com/Gaisberg/streamnzb/commit/a8613d01b6fea4a7fa7799cd81e25372d086dc48))
* **triage:** whole-word release group match; paste comma list in UI ([de37698](https://github.com/Gaisberg/streamnzb/commit/de376984231bedb31c15b437b6b4dcc52fa05303))


### Performance Improvements

* improve playback with a buffered response ([8578af6](https://github.com/Gaisberg/streamnzb/commit/8578af6d2e6979656924f8e9e18e4cb5ea9db6cf))

## [2.2.0](https://github.com/Gaisberg/streamnzb/compare/v2.1.0...v2.2.0) (2026-02-25)


### Features

* auto failover configuration option ([a9f62f4](https://github.com/Gaisberg/streamnzb/commit/a9f62f4e9fce8b01f7ef13d53b81f9e740ae1283))
* fallback to next possible stream instead of error ([bcec24b](https://github.com/Gaisberg/streamnzb/commit/bcec24b6b101e7b564ab9209ddfff25ff45dc3e7))
* password protected archive support ([ca4a9de](https://github.com/Gaisberg/streamnzb/commit/ca4a9deafe9beb67b59cb2dc8b4c8dfa9d7b8adc))


### Bug Fixes

* **stremio:** set req.IMDbID when resolving IMDb from TMDB for movie requests ([8996409](https://github.com/Gaisberg/streamnzb/commit/8996409bb7837032f4a909aeccb61f20bf1ab831))

## [2.1.0](https://github.com/Gaisberg/streamnzb/compare/v2.0.0...v2.1.0) (2026-02-24)


### Features

* **availnzb:** use backbones API and status/url, status/imdb, status/tvdb ([21462f0](https://github.com/Gaisberg/streamnzb/commit/21462f03ef88a149146902e4cce8902bf6d2e2b2))
* **indexer:** newznab search feedback fixes (t=search, ID-only q, S01E01, season/ep option) ([b92b9cf](https://github.com/Gaisberg/streamnzb/commit/b92b9cf1a57164d3dc98fb14833aa218ff32764f))
* **indexer:** per-indexer and per-device-per-indexer search settings ([4d8fde1](https://github.com/Gaisberg/streamnzb/commit/4d8fde13a5b90a9b8e6a071286c6fee03d764c9a))
* **search:** add Newznab CAPS discovery, per-indexer categories, and search query terms ([1b3cf47](https://github.com/Gaisberg/streamnzb/commit/1b3cf476c689208646c932d22ed43946fc9ad163))


### Bug Fixes

* comma seperated inputs were not working ([2b14276](https://github.com/Gaisberg/streamnzb/commit/2b1427668de8a5144462b280bb9315817c26d516))
* **search:** make allowed_languages filter work with parser and indexer formats ([d91d03d](https://github.com/Gaisberg/streamnzb/commit/d91d03d3c57ffe34704d5fbe4e1addee135b371e))
* **search:** strict series title filter to avoid wrong show matches ([6ef470c](https://github.com/Gaisberg/streamnzb/commit/6ef470c882edcd6a61a59f9150cb0cb804d23f85))
* **search:** stricter movie text filter by phrase and year ([0be7206](https://github.com/Gaisberg/streamnzb/commit/0be720661121c5b6ef9bed632836a6251bf85ae7))

## [2.0.0](https://github.com/Gaisberg/streamnzb/compare/v1.3.0...v2.0.0) (2026-02-20)


### ⚠ BREAKING CHANGES

* remove nzbhydra and prowlarr

### Features

* add extended BODY+yEnc probe to AvailNZB cache warming ([f60a88d](https://github.com/Gaisberg/streamnzb/commit/f60a88da01b3dd1dc7e02f52bc4169e55f8ff28a))
* **env:** default User-Agent StreamNZB/version for indexer requests (nzbfinder ping required user-agent) ([07f7966](https://github.com/Gaisberg/streamnzb/commit/07f79662401757c1fb72b5fb384367a49b9a5810))
* **indexer:** add possibility to disabled indexers ([07f7966](https://github.com/Gaisberg/streamnzb/commit/07f79662401757c1fb72b5fb384367a49b9a5810))
* rar is back, report all providers as bad etc... ([d3eee06](https://github.com/Gaisberg/streamnzb/commit/d3eee063e4b64f84c8f7da05ecfa23f61e2a2bfc))
* remove nzbhydra and prowlarr ([86a5442](https://github.com/Gaisberg/streamnzb/commit/86a5442aa63f3c0dcebd4e7683ce4ef31708b977))


### Bug Fixes

* daily indexer usage reset doubling counts for unlimited accounts ([484aed1](https://github.com/Gaisberg/streamnzb/commit/484aed16e14b299e18d5962e1f5d79556dd2bb5d))
* improve validation scanning, slower streams but better overall quality ([871a0e2](https://github.com/Gaisberg/streamnzb/commit/871a0e2a86e0f15e472112d8f600a180f38970d4))
* **nzb:** fall back to poster when subject is empty for CompressionType ([181e26f](https://github.com/Gaisberg/streamnzb/commit/181e26f4cf671fa54bca5480cda0696aba7eadd1))
* **unpack:** use exact rardecode PackedSize for RAR continuation volumes ([3de3699](https://github.com/Gaisberg/streamnzb/commit/3de3699145d66cabae4766bbd54e591f7d3fa0f8))

## [1.3.0](https://github.com/Gaisberg/streamnzb/compare/v1.2.0...v1.3.0) (2026-02-18)


### Features

* configurable user agents (see .env.example) ([11aac28](https://github.com/Gaisberg/streamnzb/commit/11aac288c7a50c848bdac08eb1f669c829785b01))
* text query support ([d87f19b](https://github.com/Gaisberg/streamnzb/commit/d87f19be20a4d5b198d936fc179853127d71df17))


### Bug Fixes

* device sorting and filtering defaults ([9aa8969](https://github.com/Gaisberg/streamnzb/commit/9aa8969e100f0e1ac77f5602baebc8831a187491))

## [1.2.0](https://github.com/Gaisberg/streamnzb/compare/v1.1.0...v1.2.0) (2026-02-17)


### Features

* max streams per resolution ([5a111d3](https://github.com/Gaisberg/streamnzb/commit/5a111d314af1a227c3728bc396982199a1d1fdba))
* provider priority support ([a2ad009](https://github.com/Gaisberg/streamnzb/commit/a2ad0093e186f2383d20a7d2987abdd94da94101))


### Bug Fixes

* lets not support rar for now ([13ac20f](https://github.com/Gaisberg/streamnzb/commit/13ac20f39e4a0e92695d99b8eef96266e5d1711c))
* preserve ldflags values ([1cc1f8b](https://github.com/Gaisberg/streamnzb/commit/1cc1f8b564a9b2fa18b4cbe891a273c9c90219e8))


### Performance Improvements

* greatly improve seeking performance with binary search instead of linear search with archived releases ([344d2e4](https://github.com/Gaisberg/streamnzb/commit/344d2e48227a5444b611a675925d3c00902a5014))
* overall stream performance improvements, code quality improvements ([d3a6d3e](https://github.com/Gaisberg/streamnzb/commit/d3a6d3e14de0c56ed2b3e0e6bf7389f1b34f816e))

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
